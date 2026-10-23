package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"

	"accounts/pkg/datasource/connector"
)

// fakeService is the object-storage service's bound reads as its proto states
// them: keys relative to the binding's prefix, a listing paged by
// next_page_token, a GetMany at each item's If-Match that streams a header per
// item and the bytes of each one found at it. It checks the bound credential,
// can be throttled, counts every call, and records every binding it was handed.
type fakeService struct {
	storagev0.UnimplementedObjectStorageServer
	mu            sync.Mutex
	objects       map[string]string // full key -> content
	pageSize      int
	maxBatch      int    // max_get_many_items; 0 means 1000
	secret        string // when set, the only secret the store accepts
	err           error  // returned by every call when set
	unbound       bool   // a release from before the binding: Capabilities reports no bound caps
	unbatched     bool   // a release from before GetMany: no max_get_many_items
	misorder      bool   // answers the first object under the second's index
	truncate      bool   // sends one byte less of each object than its size
	throttled     atomic.Bool
	authenticated atomic.Bool
	calls         atomic.Int64
	bindings      []*storagev0.Binding
	listReqs      []*storagev0.ListRequest
	getManys      []*storagev0.GetManyRequest
}

// call is what every RPC does first: count, record, then refuse as the
// service would — its own error, a refused credential, a throttle.
func (f *fakeService) call(b *storagev0.Binding) error {
	f.calls.Add(1)
	f.mu.Lock()
	f.bindings = append(f.bindings, b)
	err := f.err
	f.mu.Unlock()
	switch {
	case err != nil:
		return err
	case f.secret != "" && b.GetCredential().GetAccessKey().GetSecret() != f.secret:
		return status.Error(codes.PermissionDenied, "access denied")
	case f.throttled.Load():
		return status.Error(codes.ResourceExhausted, "slow down")
	}
	f.authenticated.Store(true)
	return nil
}

func (f *fakeService) Capabilities(_ context.Context, req *storagev0.CapabilitiesRequest) (*storagev0.BackendCapabilities, error) {
	if err := f.call(req.GetBinding()); err != nil {
		return nil, err
	}
	if f.unbound {
		return &storagev0.BackendCapabilities{Backend: "minio"}, nil
	}
	batch := f.maxBatch
	switch {
	case f.unbatched:
		batch = 0
	case batch == 0:
		batch = 1000
	}
	return &storagev0.BackendCapabilities{Backend: "s3", MaxObjectBytes: MaxObjectBytes, MaxListLimit: int32(f.pageSize), MaxGetManyItems: int32(batch)}, nil
}

func (f *fakeService) List(_ context.Context, req *storagev0.ListRequest) (*storagev0.ListResult, error) {
	if err := f.call(req.GetBinding()); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReqs = append(f.listReqs, req)
	folder := req.GetBinding().GetPrefix()
	var rel []string
	for key := range f.objects {
		if r, ok := strings.CutPrefix(key, folder); ok && strings.HasPrefix(r, req.GetPrefix()) {
			rel = append(rel, r)
		}
	}
	sort.Strings(rel)
	start := 0
	if req.GetPageToken() != "" {
		fmt.Sscan(req.GetPageToken(), &start)
	}
	end := min(start+f.pageSize, len(rel))
	res := &storagev0.ListResult{}
	for _, r := range rel[start:end] {
		res.Objects = append(res.Objects, &storagev0.ObjectInfo{Key: r, Etag: etagOf(f.objects[folder+r]), Size: int64(len(f.objects[folder+r]))})
	}
	if end < len(rel) {
		res.NextPageToken = fmt.Sprint(end)
	}
	return res, nil
}

func (f *fakeService) GetMany(req *storagev0.GetManyRequest, stream grpc.ServerStreamingServer[storagev0.GetManyResponse]) error {
	if err := f.call(req.GetBinding()); err != nil {
		return err
	}
	f.mu.Lock()
	f.getManys = append(f.getManys, req)
	contents := make([]*string, len(req.GetItems()))
	for i, item := range req.GetItems() {
		if content, ok := f.objects[req.GetBinding().GetPrefix()+item.GetKey()]; ok {
			contents[i] = &content
		}
	}
	f.mu.Unlock()
	for i, item := range req.GetItems() {
		header := &storagev0.GetManyHeader{Index: int32(i)}
		if f.misorder && i == 0 {
			header.Index = 1
		}
		switch content := contents[i]; {
		case content == nil:
			header.Outcome = storagev0.GetManyOutcome_GET_MANY_OUTCOME_NOT_FOUND
		case item.GetIfMatch() != "" && item.GetIfMatch() != etagOf(*content):
			header.Outcome = storagev0.GetManyOutcome_GET_MANY_OUTCOME_CHANGED
		case len(*content) > MaxObjectBytes:
			header.Outcome = storagev0.GetManyOutcome_GET_MANY_OUTCOME_TOO_LARGE
		default:
			header.Outcome = storagev0.GetManyOutcome_GET_MANY_OUTCOME_OK
			header.Info = &storagev0.ObjectInfo{Key: item.GetKey(), Etag: etagOf(*content), Size: int64(len(*content)), ContentType: "text/markdown"}
		}
		if err := stream.Send(&storagev0.GetManyResponse{Kind: &storagev0.GetManyResponse_Header{Header: header}}); err != nil {
			return err
		}
		if header.Outcome != storagev0.GetManyOutcome_GET_MANY_OUTCOME_OK {
			continue
		}
		body := []byte(*contents[i])
		if f.truncate {
			body = body[:len(body)-1]
		}
		for chunk := range slicesChunk(body, 3) {
			if err := stream.Send(&storagev0.GetManyResponse{Kind: &storagev0.GetManyResponse_Data{Data: chunk}}); err != nil {
				return err
			}
		}
	}
	return nil
}

func slicesChunk(b []byte, n int) func(func([]byte) bool) {
	return func(yield func([]byte) bool) {
		for len(b) > 0 {
			k := min(n, len(b))
			if !yield(b[:k]) {
				return
			}
			b = b[k:]
		}
	}
}

func serve(t *testing.T, f *fakeService) storagev0.ObjectStorageClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	storagev0.RegisterObjectStorageServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return storagev0.NewObjectStorageClient(cc)
}

var serviceConfig = SourceConfig{Endpoint: "https://store.example.com", Region: "eu-west-1", Bucket: "acme-docs", Prefix: "kb/2026-"}

// TestServiceBucketListsTheSourcesKeyPrefixThroughTheBinding: a source's
// prefix is a key prefix, the binding's a folder. The binding takes the folder
// and the listing the rest, every page is read, and every item comes back
// under its full key — the identity the host's deliveries carry.
func TestServiceBucketListsTheSourcesKeyPrefixThroughTheBinding(t *testing.T) {
	f := &fakeService{pageSize: 2, objects: map[string]string{
		"kb/2026-a.md": "alpha", "kb/2026-b.md": "bravo", "kb/2026-c.md": "charlie", "kb/2025-z.md": "old", "other/2026-x.md": "elsewhere",
	}}
	b := NewServiceBucket(serve(t, f), serviceConfig, "AKIAEXAMPLE", "sentinel-secret")
	items, err := b.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
		if it.ETag != etagOf(f.objects[it.Key]) || it.Size != int64(len(f.objects[it.Key])) {
			t.Fatalf("item %+v does not carry its ETag and size", it)
		}
	}
	if want := []string{"kb/2026-a.md", "kb/2026-b.md", "kb/2026-c.md"}; strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("listed %v, want %v", keys, want)
	}
	if len(f.listReqs) != 2 || f.listReqs[0].GetPrefix() != "2026-" || f.listReqs[1].GetPageToken() == "" {
		t.Fatalf("list requests = %v, want two pages filtered on the prefix's last segment", f.listReqs)
	}
	bound := f.bindings[0]
	if bound.GetKind() != "s3" || bound.GetEndpoint() != serviceConfig.Endpoint || bound.GetRegion() != "eu-west-1" ||
		bound.GetBucket() != "acme-docs" || bound.GetPrefix() != "kb/" ||
		bound.GetCredential().GetAccessKey().GetId() != "AKIAEXAMPLE" || bound.GetCredential().GetAccessKey().GetSecret() != "sentinel-secret" {
		t.Fatalf("binding = %v", bound)
	}
}

func TestServiceBucketRefusesAListingPastItsPageBound(t *testing.T) {
	f := &fakeService{pageSize: 1, objects: map[string]string{}}
	for i := 0; i <= maxListPages; i++ {
		f.objects[fmt.Sprintf("kb/2026-%02d.md", i)] = "x"
	}
	_, err := NewServiceBucket(serve(t, f), serviceConfig, "id", "secret").List(context.Background())
	if !errors.Is(err, ErrListingIncomplete) {
		t.Fatalf("a listing of %d pages = %v, want ErrListingIncomplete", maxListPages+1, err)
	}
}

// TestServiceBucketReadsABatchInOneCall: a batch is one GetMany of each key
// relative to the binding, at If-Match its listed ETag, answered in request
// order; a batch past the service's max_get_many_items is split; an object
// rewritten or deleted since its listing is ErrObjectChanged, and one over the
// service's cap is the envelope's ErrItemTooLarge.
func TestServiceBucketReadsABatchInOneCall(t *testing.T) {
	f := &fakeService{pageSize: 10, objects: map[string]string{"kb/2026-a.md": "alpha", "kb/2026-b.md": "bravo", "kb/2026-c.md": "charlie"}}
	b := NewServiceBucket(serve(t, f), serviceConfig, "id", "secret")
	items, err := b.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	read := func(b *ServiceBucket, items []Item) (map[string]string, error) {
		got := map[string]string{}
		err := b.Read(context.Background(), items, func(it Item, contentType string, r io.Reader) error {
			body, _ := io.ReadAll(r)
			got[it.Key] = contentType + ":" + string(body)
			return nil
		})
		return got, err
	}
	got, err := read(b, items)
	if err != nil {
		t.Fatal(err)
	}
	if got["kb/2026-a.md"] != "text/markdown:alpha" || got["kb/2026-b.md"] != "text/markdown:bravo" || got["kb/2026-c.md"] != "text/markdown:charlie" {
		t.Fatalf("read %v", got)
	}
	if len(f.getManys) != 1 || len(f.getManys[0].GetItems()) != 3 ||
		f.getManys[0].GetItems()[0].GetKey() != "2026-a.md" || f.getManys[0].GetItems()[0].GetIfMatch() != etagOf("alpha") {
		t.Fatalf("GetMany calls = %v, want one for the batch, relative keys, at If-Match", f.getManys)
	}

	small := &fakeService{pageSize: 10, maxBatch: 2, objects: f.objects}
	if got, err := read(NewServiceBucket(serve(t, small), serviceConfig, "id", "secret"), items); err != nil || len(got) != 3 || len(small.getManys) != 2 {
		t.Fatalf("a batch past max_get_many_items: read %d objects in %d calls (%v), want 3 in 2", len(got), len(small.getManys), err)
	}

	f.objects["kb/2026-a.md"] = "alpha, rewritten"
	delete(f.objects, "kb/2026-b.md")
	for _, it := range items[:2] {
		if _, err := read(b, []Item{it}); !errors.Is(err, ErrObjectChanged) {
			t.Fatalf("reading %s after it changed = %v, want ErrObjectChanged", it.Key, err)
		}
	}
	f.objects["kb/2026-c.md"] = strings.Repeat("x", MaxObjectBytes+1)
	big := []Item{{Key: "kb/2026-c.md", ETag: etagOf(f.objects["kb/2026-c.md"]), Size: MaxObjectBytes + 1}}
	if _, err := read(b, big); !errors.Is(err, connector.ErrItemTooLarge) {
		t.Fatalf("reading an object over the service's cap = %v, want ErrItemTooLarge", err)
	}
}

// TestServiceBucketRefusesAnAnswerItCannotAttribute: an object answered out of
// request order, or whose bytes stop short of its listed size, is refused —
// never delivered as another object's content or as a truncated file.
func TestServiceBucketRefusesAnAnswerItCannotAttribute(t *testing.T) {
	objects := map[string]string{"kb/2026-a.md": "alpha", "kb/2026-b.md": "bravo"}
	items := []Item{{Key: "kb/2026-a.md", ETag: etagOf("alpha"), Size: 5}, {Key: "kb/2026-b.md", ETag: etagOf("bravo"), Size: 5}}
	for name, f := range map[string]*fakeService{
		"out of order": {pageSize: 10, misorder: true, objects: objects},
		"short":        {pageSize: 10, truncate: true, objects: objects},
	} {
		delivered := 0
		err := NewServiceBucket(serve(t, f), serviceConfig, "id", "secret").Read(context.Background(), items, func(Item, string, io.Reader) error {
			delivered++
			return nil
		})
		if err == nil || delivered != 0 {
			t.Fatalf("%s answer: Read = %v after delivering %d objects, want a refusal before any", name, err, delivered)
		}
	}
}

// TestServiceBucketRefusesAServiceThatIgnoresTheBinding: a service from before
// the binding drops the field and answers for its own bucket. Nothing it would
// list or read is taken for the source's.
func TestServiceBucketRefusesAServiceThatIgnoresTheBinding(t *testing.T) {
	f := &fakeService{pageSize: 10, unbound: true, objects: map[string]string{"kb/2026-a.md": "alpha"}}
	b := NewServiceBucket(serve(t, f), serviceConfig, "id", "secret")
	if _, err := b.List(context.Background()); !errors.Is(err, ErrBindingNotHonoured) {
		t.Fatalf("List on an unbinding service = %v, want ErrBindingNotHonoured", err)
	}
	err := b.Read(context.Background(), []Item{{Key: "kb/2026-a.md", ETag: etagOf("alpha"), Size: 5}}, func(Item, string, io.Reader) error { return nil })
	if !errors.Is(err, ErrBindingNotHonoured) || len(f.listReqs) != 0 || len(f.getManys) != 0 {
		t.Fatalf("Read = %v after %d lists and %d reads; an unbinding service is never listed or read", err, len(f.listReqs), len(f.getManys))
	}
	unbatched := &fakeService{pageSize: 10, unbatched: true, objects: f.objects}
	if _, err := NewServiceBucket(serve(t, unbatched), serviceConfig, "id", "secret").List(context.Background()); !errors.Is(err, ErrBindingNotHonoured) {
		t.Fatalf("List on a service with no batched read = %v, want ErrBindingNotHonoured", err)
	}
}

// TestServiceBucketTypesTheServicesRefusals: a refused credential or endpoint
// is the envelope's credential refusal, a throttle is a rate limit carrying the
// service's retry delay, and anything else keeps its status.
func TestServiceBucketTypesTheServicesRefusals(t *testing.T) {
	throttle, _ := status.New(codes.ResourceExhausted, "slow down").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(30 * time.Second)})
	for _, tc := range []struct {
		name  string
		err   error
		check func(error) bool
	}{
		{"permission denied", status.Error(codes.PermissionDenied, `binding endpoint "10.0.0.1": destination is not a public address`), func(err error) bool {
			return errors.Is(err, connector.ErrCredentialRefused)
		}},
		{"throttled", throttle.Err(), func(err error) bool {
			var limited *connector.RateLimitedError
			return errors.As(err, &limited) && limited.ResetAt.After(time.Now().Add(20*time.Second))
		}},
		// A missing bucket is the store's error, never an empty listing: an
		// empty one would be a version that names no object, and the sync's
		// closing listing would tombstone the whole collection against it.
		{"missing bucket", status.Error(codes.NotFound, "no such bucket"), func(err error) bool {
			return status.Code(err) == codes.NotFound
		}},
		{"invalid binding", status.Error(codes.InvalidArgument, "bad bucket"), func(err error) bool {
			return status.Code(err) == codes.InvalidArgument && !errors.Is(err, connector.ErrCredentialRefused)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeService{pageSize: 10, err: tc.err}
			if _, err := NewServiceBucket(serve(t, f), serviceConfig, "id", "secret").List(context.Background()); !tc.check(err) {
				t.Fatalf("List under %v = %v", tc.err, err)
			}
		})
	}
}

// TestFilesConnectorOverTheService runs the connector on the service-backed
// Bucket: a snapshot, a fetch at its version, and a deletion seen by the next
// change set — what a sync does, through the service's own wire.
func TestFilesConnectorOverTheService(t *testing.T) {
	f := &fakeService{pageSize: 1, objects: map[string]string{"kb/2026-a.md": "alpha", "kb/2026-b.md": "bravo", "kb/2026-draft.md": "wip"}}
	client := serve(t, f)
	cfg := serviceConfig
	cfg.Scope = Scope{InScope: func(key string) bool { return !strings.HasSuffix(key, "draft.md") }}
	conn := NewFilesConnector(func(context.Context, connector.Source) (Bucket, error) {
		return NewServiceBucket(client, cfg, "id", "secret"), nil
	}, &memoryManifests{})
	src := connector.Source{ID: "aaaaaaaa-0000-0000-0000-000000000001", OrgID: "11111111-1111-1111-1111-111111111111", BoundaryNodeID: "bbbbbbbb-0000-0000-0000-000000000001", Config: cfg}
	ctx := context.Background()
	first, err := conn.Changes(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) != 2 {
		t.Fatalf("snapshot = %+v, want the two objects the scope admits", first.Changes)
	}
	var refs []connector.FileRef
	for _, ch := range first.Changes {
		refs = append(refs, connector.FileRef{ItemID: ch.Locator, ItemVersion: ch.ItemVersion})
	}
	fetched := map[string]string{}
	if err := conn.FetchFiles(ctx, src, first.To, refs, func(file connector.File, r io.Reader) error {
		body, _ := io.ReadAll(r)
		fetched[file.Locator] = string(body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if fetched["kb/2026-a.md"] != "alpha" || fetched["kb/2026-b.md"] != "bravo" {
		t.Fatalf("fetched %v", fetched)
	}
	delete(f.objects, "kb/2026-b.md")
	next, err := conn.Changes(ctx, src, first.To)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Changes) != 1 || next.Changes[0].Kind != connector.ChangeDeleted || next.Changes[0].Locator != "kb/2026-b.md" {
		t.Fatalf("after a delete, changes = %+v, want kb/2026-b.md deleted", next.Changes)
	}
}
