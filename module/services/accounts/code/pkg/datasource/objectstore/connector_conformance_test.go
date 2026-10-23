package objectstore

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/connector/connectortest"
)

// TestFilesConnectorConformance runs the datasource conformance suite against
// the object-storage connector reading through ServiceBucket, the host's client
// of the object-storage service, over an in-process fake of the service's gRPC
// API: the binding, the paged listing and the batched read on the wire, a
// sentinel credential the store checks, and every RPC counted — so clause 8's
// bound is measured on the calls the host actually makes.
func TestFilesConnectorConformance(t *testing.T) {
	connectortest.RunFiles(t, newConformanceFixture)
}

const conformanceCredential = "sentinel-secret-key-4c1d"
const conformanceMaxItemBytes = 4 << 10

// The suite lists 120 objects in one snapshot: the source is configured at the
// ceiling, past the 100 an unconfigured source takes. Its keys are written
// without a prefix, so the source reads the whole bucket.
var conformanceConfig = SourceConfig{Endpoint: "https://store.example.com", Region: "us-east-1", Bucket: "acme-docs", Scope: Scope{MaxObjects: maxObjectsCeiling}}

type conformanceFixture struct {
	store     *fakeService
	manifests *memoryManifests
	conn      *FilesConnector
	sources   int
}

func newConformanceFixture(t *testing.T) connectortest.FilesFixture {
	f := &conformanceFixture{store: &fakeService{objects: map[string]string{}, pageSize: 50, secret: conformanceCredential}, manifests: &memoryManifests{}}
	client := serve(t, f.store)
	f.conn = NewFilesConnector(func(_ context.Context, src connector.Source) (Bucket, error) {
		// The host resolves the credential per call; here it is the sentinel.
		cfg, _ := src.Config.(SourceConfig)
		return NewServiceBucket(client, cfg, "conformance-key", conformanceCredential), nil
	}, f.manifests, WithMaxItemBytes(conformanceMaxItemBytes))
	return f
}

func (f *conformanceFixture) Connector() connector.FilesConnector { return f.conn }

func (f *conformanceFixture) Source(t *testing.T, org string) connector.Source {
	f.sources++
	return connector.Source{
		ID:             fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", f.sources),
		OrgID:          org,
		BoundaryNodeID: fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", f.sources),
		Config:         conformanceConfig,
	}
}

// Apply writes the provider's next state. A store has no rename: a move is the
// object written at its new key and removed from its old one.
func (f *conformanceFixture) Apply(t *testing.T, puts map[string]string, deletes []string, moves map[string]string) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for key, content := range puts {
		f.store.objects[key] = content
	}
	for _, key := range deletes {
		delete(f.store.objects, key)
	}
	for from, to := range moves {
		f.store.objects[to] = f.store.objects[from]
		delete(f.store.objects, from)
	}
}

// Rewrite: a bucket has no history to rewrite. What makes a version
// undiffable is that the listing it names is no longer retained, so the
// fixture drops every retained listing.
func (f *conformanceFixture) Rewrite(t *testing.T) { f.manifests.forget() }

func (f *conformanceFixture) ProviderCalls() int64 { return f.store.calls.Load() }

func (f *conformanceFixture) RateLimit(t *testing.T) func() {
	f.store.throttled.Store(true)
	return func() { f.store.throttled.Store(false) }
}

func (f *conformanceFixture) Credential() string { return conformanceCredential }

func (f *conformanceFixture) CredentialPresented() bool { return f.store.authenticated.Load() }

func (f *conformanceFixture) MaxItemBytes() int64 { return conformanceMaxItemBytes }

func etagOf(content string) string {
	sum := md5.Sum([]byte(content))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// memoryManifests retains every listing until forget.
type memoryManifests struct {
	mu   sync.Mutex
	kept map[string][]Item
}

func (m *memoryManifests) Put(_ context.Context, src connector.Source, version string, items []Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kept == nil {
		m.kept = map[string][]Item{}
	}
	m.kept[src.ID+"/"+version] = append([]Item(nil), items...)
	return nil
}

func (m *memoryManifests) Get(_ context.Context, src connector.Source, version string) ([]Item, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, ok := m.kept[src.ID+"/"+version]
	return items, ok, nil
}

func (m *memoryManifests) forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kept = nil
}

// TestChangesResyncAcrossABinding holds a version to the bucket and prefix it
// was listed under: after the source is pointed elsewhere, its old version is
// neither diffed against nor read from.
func TestChangesResyncAcrossABinding(t *testing.T) {
	f := newConformanceFixture(t).(*conformanceFixture)
	f.Apply(t, map[string]string{"kb/a.md": "alpha"}, nil, nil)
	src := f.Source(t, "11111111-1111-1111-1111-111111111111")
	ctx := context.Background()
	a, err := f.conn.Changes(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	moved := conformanceConfig
	moved.Prefix = "other/"
	src.Config = moved
	if _, err := f.conn.Changes(ctx, src, a.To); !errors.Is(err, connector.ErrResyncRequired) {
		t.Fatalf("changes from a version of another prefix = %v, want ErrResyncRequired", err)
	}
	err = f.conn.FetchFiles(ctx, src, a.To, []connector.FileRef{{ItemID: "kb/a.md"}}, func(connector.File, io.Reader) error { return nil })
	if !errors.Is(err, connector.ErrVersionNotFound) {
		t.Fatalf("fetching a version of another prefix = %v, want ErrVersionNotFound", err)
	}
}

// TestFetchRefusesARewrittenObject: an object rewritten after it was listed is
// not at the listed version any more, so a fetch at that version refuses it
// rather than serve content the version never had.
func TestFetchRefusesARewrittenObject(t *testing.T) {
	f := newConformanceFixture(t).(*conformanceFixture)
	f.Apply(t, map[string]string{"kb/a.md": "alpha"}, nil, nil)
	src := f.Source(t, "11111111-1111-1111-1111-111111111111")
	ctx := context.Background()
	a, err := f.conn.Changes(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	f.Apply(t, map[string]string{"kb/a.md": "alpha, rewritten"}, nil, nil)
	err = f.conn.FetchFiles(ctx, src, a.To, []connector.FileRef{{ItemID: "kb/a.md"}}, func(connector.File, io.Reader) error { return nil })
	if !errors.Is(err, connector.ErrItemNotFound) {
		t.Fatalf("fetching an object rewritten since its listing = %v, want ErrItemNotFound", err)
	}
}

// TestRateLimitIsEstimatedWhenTheStoreGivesNoReset: a store's throttle names
// no reset, so the connector gives it one rather than a zero time.
func TestRateLimitIsEstimatedWhenTheStoreGivesNoReset(t *testing.T) {
	f := newConformanceFixture(t).(*conformanceFixture)
	clear := f.RateLimit(t)
	defer clear()
	_, err := f.conn.Changes(context.Background(), f.Source(t, "11111111-1111-1111-1111-111111111111"), "")
	var limited *connector.RateLimitedError
	if !errors.As(err, &limited) || !limited.Estimated || !limited.ResetAt.After(time.Now()) {
		t.Fatalf("a throttle without a reset = %v, want an estimated reset in the future", err)
	}
}
