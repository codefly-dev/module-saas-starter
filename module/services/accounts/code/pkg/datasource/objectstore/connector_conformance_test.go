package objectstore

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/connector/connectortest"
)

// TestFilesConnectorConformance runs the datasource conformance suite against
// the object-storage connector, over a scripted bucket that requires a
// sentinel credential and counts every call: one per listing, one per batched
// read, which is the contract Bucket states.
func TestFilesConnectorConformance(t *testing.T) {
	connectortest.RunFiles(t, newConformanceFixture)
}

const conformanceCredential = "sentinel-secret-key-4c1d"
const conformanceMaxItemBytes = 4 << 10

// The suite lists 120 objects in one snapshot: the source is configured at the
// ceiling, past the 100 an unconfigured source takes.
var conformanceConfig = SourceConfig{Endpoint: "https://store.example.com", Region: "us-east-1", Bucket: "acme-docs", Prefix: "kb/", Scope: Scope{MaxObjects: maxObjectsCeiling}}

type conformanceFixture struct {
	store     *fakeStore
	manifests *memoryManifests
	conn      *FilesConnector
	sources   int
}

func newConformanceFixture(t *testing.T) connectortest.FilesFixture {
	f := &conformanceFixture{store: &fakeStore{objects: map[string]string{}}, manifests: &memoryManifests{}}
	f.conn = NewFilesConnector(func(context.Context, connector.Source) (Bucket, error) {
		// The host resolves the credential per call; here it is the sentinel.
		return &fakeBucket{store: f.store, credential: conformanceCredential}, nil
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

// fakeStore is one bucket's objects, shared by every source of a fixture.
type fakeStore struct {
	mu            sync.Mutex
	objects       map[string]string
	calls         atomic.Int64
	throttled     atomic.Bool
	authenticated atomic.Bool
}

func etagOf(content string) string {
	sum := md5.Sum([]byte(content))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// fakeBucket is the store as one call's credential sees it.
type fakeBucket struct {
	store      *fakeStore
	credential string
}

var errDenied = errors.New("fake store: access denied")

func (b *fakeBucket) call(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.store.calls.Add(1)
	if b.credential != conformanceCredential {
		return fmt.Errorf("%w: %w", connector.ErrCredentialRefused, errDenied)
	}
	b.store.authenticated.Store(true)
	if b.store.throttled.Load() {
		// SlowDown carries no reset; the connector estimates one.
		return &connector.RateLimitedError{Scope: "credential", Cause: errors.New("fake store: SlowDown")}
	}
	return nil
}

func (b *fakeBucket) List(ctx context.Context) ([]Item, error) {
	if err := b.call(ctx); err != nil {
		return nil, err
	}
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	items := make([]Item, 0, len(b.store.objects))
	for key, content := range b.store.objects {
		items = append(items, Item{Key: key, ETag: etagOf(content), Size: int64(len(content))})
	}
	return items, nil
}

func (b *fakeBucket) Read(ctx context.Context, items []Item, visit func(Item, string, io.Reader) error) error {
	if err := b.call(ctx); err != nil {
		return err
	}
	b.store.mu.Lock()
	contents := make([]string, len(items))
	for i, it := range items {
		content, ok := b.store.objects[it.Key]
		if !ok || etagOf(content) != it.ETag {
			b.store.mu.Unlock()
			return ErrObjectChanged
		}
		contents[i] = content
	}
	b.store.mu.Unlock()
	for i, it := range items {
		if err := visit(it, "text/plain; charset=utf-8", strings.NewReader(contents[i])); err != nil {
			return err
		}
	}
	return nil
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
