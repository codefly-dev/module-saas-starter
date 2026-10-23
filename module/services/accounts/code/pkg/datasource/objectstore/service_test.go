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
// next_page_token, a Get at If-Match that streams a header then data. It
// records every binding it was handed.
type fakeService struct {
	storagev0.UnimplementedObjectStorageServer
	mu       sync.Mutex
	objects  map[string]string // full key -> content
	pageSize int
	err      error // returned by every call when set
	unbound  bool  // a release from before the binding: Capabilities reports no bound caps
	bindings []*storagev0.Binding
	listReqs []*storagev0.ListRequest
	gets     []*storagev0.GetRequest
}

func (f *fakeService) record(b *storagev0.Binding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindings = append(f.bindings, b)
}

func (f *fakeService) Capabilities(_ context.Context, req *storagev0.CapabilitiesRequest) (*storagev0.BackendCapabilities, error) {
	f.record(req.GetBinding())
	if f.err != nil {
		return nil, f.err
	}
	if f.unbound {
		return &storagev0.BackendCapabilities{Backend: "minio"}, nil
	}
	return &storagev0.BackendCapabilities{Backend: "s3", MaxObjectBytes: MaxObjectBytes, MaxListLimit: int32(f.pageSize)}, nil
}

func (f *fakeService) List(_ context.Context, req *storagev0.ListRequest) (*storagev0.ListResult, error) {
	f.record(req.GetBinding())
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReqs = append(f.listReqs, req)
	if f.err != nil {
		return nil, f.err
	}
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

func (f *fakeService) Get(req *storagev0.GetRequest, stream grpc.ServerStreamingServer[storagev0.GetResponse]) error {
	f.record(req.GetBinding())
	f.mu.Lock()
	f.gets = append(f.gets, req)
	content, ok := f.objects[req.GetBinding().GetPrefix()+req.GetKey()]
	err := f.err
	f.mu.Unlock()
	switch {
	case err != nil:
		return err
	case !ok:
		return status.Error(codes.NotFound, "no such object")
	case req.GetIfMatch() != etagOf(content):
		return status.Error(codes.FailedPrecondition, "etag mismatch")
	}
	if err := stream.Send(&storagev0.GetResponse{Kind: &storagev0.GetResponse_Header{Header: &storagev0.GetHeader{
		Info: &storagev0.ObjectInfo{Key: req.GetKey(), Etag: etagOf(content), Size: int64(len(content)), ContentType: "text/markdown"},
	}}}); err != nil {
		return err
	}
	for chunk := range slicesChunk([]byte(content), 3) {
		if err := stream.Send(&storagev0.GetResponse{Kind: &storagev0.GetResponse_Data{Data: chunk}}); err != nil {
			return err
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

// TestServiceBucketReadsEachObjectAtItsListedETag: each object is one Get of
// its key relative to the binding, at If-Match its listed ETag — N objects are
// N calls, which is the gap the provider is registered with — and an object
// rewritten or deleted since its listing is ErrObjectChanged.
func TestServiceBucketReadsEachObjectAtItsListedETag(t *testing.T) {
	f := &fakeService{pageSize: 10, objects: map[string]string{"kb/2026-a.md": "alpha", "kb/2026-b.md": "bravo"}}
	b := NewServiceBucket(serve(t, f), serviceConfig, "id", "secret")
	items, err := b.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	err = b.Read(context.Background(), items, func(it Item, contentType string, r io.Reader) error {
		body, _ := io.ReadAll(r)
		got[it.Key] = contentType + ":" + string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["kb/2026-a.md"] != "text/markdown:alpha" || got["kb/2026-b.md"] != "text/markdown:bravo" {
		t.Fatalf("read %v", got)
	}
	if len(f.gets) != 2 || f.gets[0].GetKey() != "2026-a.md" || f.gets[0].GetIfMatch() != etagOf("alpha") {
		t.Fatalf("gets = %v, want one per object, relative key, at If-Match", f.gets)
	}

	f.objects["kb/2026-a.md"] = "alpha, rewritten"
	delete(f.objects, "kb/2026-b.md")
	for _, it := range items {
		if err := b.Read(context.Background(), []Item{it}, func(Item, string, io.Reader) error { return nil }); !errors.Is(err, ErrObjectChanged) {
			t.Fatalf("reading %s after it changed = %v, want ErrObjectChanged", it.Key, err)
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
	if !errors.Is(err, ErrBindingNotHonoured) || len(f.listReqs) != 0 || len(f.gets) != 0 {
		t.Fatalf("Read = %v after %d lists and %d gets; an unbinding service is never listed or read", err, len(f.listReqs), len(f.gets))
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
