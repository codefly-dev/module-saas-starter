// Package connectortest is the datasource conformance suite: the envelope's
// clauses as tests any connector's own test file runs against a scripted fake
// of its provider. The host's registry test requires every conformant
// connector to run it, so a connector cannot be registered as conformant
// without passing — the same shape as the boundary gate.
package connectortest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"accounts/pkg/datasource/connector"
	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
)

// FilesFixture is a scripted provider for one files connector. Each subtest
// gets a fresh fixture from the factory, so fixtures need no reset.
type FilesFixture interface {
	// Connector is the connector under test, pointed at this fixture's provider.
	Connector() connector.FilesConnector
	// Source is a new source over the fixture's provider content, owned by org.
	// Two sources of one fixture share provider content and nothing else.
	Source(t *testing.T, org string) connector.Source
	// Apply makes one new provider version: puts maps an item id (a path) to
	// its content; deletes and moves (from → to) apply after the puts.
	Apply(t *testing.T, puts map[string]string, deletes []string, moves map[string]string)
	// Rewrite rewrites the provider's history so every version the connector
	// has seen so far is no longer an ancestor of the current one.
	Rewrite(t *testing.T)
	// ProviderCalls counts the network requests the provider has served.
	ProviderCalls() int64
	// RateLimit makes every provider request fail as rate limited until the
	// returned function is called.
	RateLimit(t *testing.T) (clear func())
	// Credential is the sentinel credential the provider requires.
	Credential() string
	// CredentialPresented reports whether the provider received the credential
	// on its requests (so a connector cannot pass by never authenticating).
	CredentialPresented() bool
	// MaxItemBytes is the item limit of the connector under test, which the
	// fixture may have lowered so an over-limit item is cheap to script.
	MaxItemBytes() int64
}

// RunFiles runs the envelope's clauses against a files connector.
func RunFiles(t *testing.T, newFixture func(t *testing.T) FilesFixture) {
	t.Helper()
	tests := []struct {
		name string
		run  func(t *testing.T, f FilesFixture)
	}{
		{"01 descriptor is complete and conformant", testDescriptor},
		{"02 snapshot carries identity, tenancy, provenance and readers", testSnapshot},
		{"03 the same provider item in two tenants never collides", testTenancy},
		{"04 readers fail closed and match the declared model", testReaders},
		{"05 a provider permission change surfaces without refetching", testReadersChanged},
		{"06 the credential never leaves the connector", testCredential},
		{"07 changes from A to B are exact, empty for A to A, idempotent", testIncremental},
		{"08 a rewritten history requires a resync, then a complete snapshot", testResync},
		{"09 a deleted item is never found again", testDeletion},
		{"10 fetched provenance matches the change", testProvenance},
		{"11 a bulk fetch costs a bounded number of provider calls", testBulk},
		{"12 a provider rate limit is typed with its reset time", testRateLimit},
		{"14 an over-limit item or batch is refused typed, never truncated", testLimits},
		{"15 cancellation stops the connector", testCancel},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newFixture(t))
		})
	}
	// Clause 7 (audit) is not a connector's: the host emits one vocabulary for
	// every connector, and its own tests hold it.
}

const org1, org2 = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return c
}

func testDescriptor(t *testing.T, f FilesFixture) {
	d := f.Connector().Descriptor()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !d.Conformant || d.Interface != connector.InterfaceFiles {
		t.Fatalf("a files connector under the suite must be conformant and declare the files interface, got %+v", d)
	}
	if err := connector.NewRegistry().Register(f.Connector()); err != nil {
		t.Fatalf("the registry refuses it: %v", err)
	}
}

// snapshot returns a complete snapshot, following nothing: a connector returns
// a snapshot in one change set.
func snapshot(t *testing.T, f FilesFixture, src connector.Source) connector.ChangeSet {
	t.Helper()
	cs, err := f.Connector().Changes(ctx(t), src, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !cs.Complete || cs.From != "" || cs.To == "" || cs.SourceID != src.ID {
		t.Fatalf("a snapshot is complete, from nothing, to a version, of its source: %+v", cs)
	}
	return cs
}

func byID(cs connector.ChangeSet) map[string]connector.Change {
	out := map[string]connector.Change{}
	for _, c := range cs.Changes {
		out[c.Key.ItemID] = c
	}
	return out
}

func fetchAll(t *testing.T, f FilesFixture, src connector.Source, version string, refs []connector.FileRef) (map[string]connector.File, map[string]string) {
	t.Helper()
	files, bodies := map[string]connector.File{}, map[string]string{}
	var order []string
	err := f.Connector().FetchFiles(ctx(t), src, version, refs, func(file connector.File, r io.Reader) error {
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if int64(len(b)) != file.Size {
			return fmt.Errorf("file %s: size %d, read %d", file.Provenance.GetItemId(), file.Size, len(b))
		}
		id := file.Provenance.GetItemId()
		files[id], bodies[id] = file, string(b)
		order = append(order, id)
		return nil
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for i, ref := range refs {
		if i >= len(order) || order[i] != ref.ItemID {
			t.Fatalf("files must arrive in request order: got %v for %v", order, refs)
		}
	}
	return files, bodies
}

func refsOf(cs connector.ChangeSet) []connector.FileRef {
	var refs []connector.FileRef
	for _, c := range cs.Changes {
		if c.Kind != connector.ChangeDeleted {
			refs = append(refs, connector.FileRef{ItemID: c.Key.ItemID, ItemVersion: c.ItemVersion})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ItemID < refs[j].ItemID })
	return refs
}

func testSnapshot(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha", "docs/b.md": "bravo"}, nil, nil)
	src := f.Source(t, org1)
	cs := snapshot(t, f, src)
	got := byID(cs)
	if len(got) != 2 {
		t.Fatalf("snapshot lists %d items, want 2: %+v", len(got), cs.Changes)
	}
	for id, c := range got {
		want := connector.KeyFor(src, id)
		if c.Key != want || c.Kind != connector.ChangeAdded || c.ItemVersion == "" || c.Locator == "" {
			t.Fatalf("snapshot change %+v: want key %+v, kind added, an item version and a locator", c, want)
		}
		if msg := connector.ReadersError(c.Readers); msg != "" {
			t.Fatalf("change %s: %s", id, msg)
		}
	}
	files, bodies := fetchAll(t, f, src, cs.To, refsOf(cs))
	if bodies["a.md"] != "alpha" || bodies["docs/b.md"] != "bravo" {
		t.Fatalf("fetched content = %v", bodies)
	}
	for id, file := range files {
		p := file.Provenance
		if p.GetSourceId() != src.ID || p.GetOrgId() != src.OrgID || p.GetBoundaryNodeId() != src.BoundaryNodeID ||
			p.GetVersion() != cs.To || p.GetItemId() != id || p.GetItemVersion() != got[id].ItemVersion {
			t.Fatalf("file %s provenance %v does not name its source, version, item and item version", id, p)
		}
		if msg := connector.ReadersError(file.Readers); msg != "" {
			t.Fatalf("file %s: %s", id, msg)
		}
	}
}

func testTenancy(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"shared.md": "same bytes"}, nil, nil)
	s1, s2 := f.Source(t, org1), f.Source(t, org2)
	if s1.ID == s2.ID {
		t.Fatal("fixture handed out one source twice")
	}
	c1, c2 := byID(snapshot(t, f, s1))["shared.md"], byID(snapshot(t, f, s2))["shared.md"]
	if c1.Key == c2.Key || c1.Key.OrgID != org1 || c2.Key.OrgID != org2 {
		t.Fatalf("keys of one provider item in two tenants must differ by source and org: %+v / %+v", c1.Key, c2.Key)
	}
	files, _ := fetchAll(t, f, s2, snapshot(t, f, s2).To, []connector.FileRef{{ItemID: "shared.md"}})
	if p := files["shared.md"].Provenance; p.GetSourceId() != s2.ID || p.GetOrgId() != org2 {
		t.Fatalf("a fetch through the second source must name it: %v", p)
	}
}

func testReaders(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha"}, nil, nil)
	d := f.Connector().Descriptor()
	src := f.Source(t, org1)
	for _, c := range snapshot(t, f, src).Changes {
		switch d.Readers {
		case connector.ReadersSourceScoped:
			if c.Readers.GetBasis() != accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_SOURCE_SCOPED {
				t.Fatalf("a source-scoped connector emitted readers %v", c.Readers)
			}
		case connector.ReadersTranslated:
			if c.Readers.GetBasis() == accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_SOURCE_SCOPED {
				t.Fatalf("a translating connector may not widen an item to its boundary: %v", c.Readers)
			}
		}
	}
	// A personal source's items are its owner's alone, whatever the connector says.
	personal := src
	personal.PersonalOwnerUserID = "33333333-3333-3333-3333-333333333333"
	for _, c := range snapshot(t, f, personal).Changes {
		r := connector.ApplySourcePolicy(personal, c.Readers)
		if len(r.GetUserIds()) != 1 || r.GetUserIds()[0] != personal.PersonalOwnerUserID || r.GetBoundaryReaders() || len(r.GetGroupIds()) > 0 {
			t.Fatalf("a personal source's item must be readable by its owner only: %v", r)
		}
	}
}

func testReadersChanged(t *testing.T, f FilesFixture) {
	if f.Connector().Descriptor().Readers == connector.ReadersSourceScoped {
		t.Skip("a source-scoped connector has no per-item access list to change")
	}
	t.Fatal("a translating files connector needs a fixture that scripts access-list changes; none is defined yet")
}

func testCredential(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha"}, nil, nil)
	src := f.Source(t, org1)
	secret := f.Credential()
	var seen []string
	d := f.Connector().Descriptor()
	seen = append(seen, fmt.Sprintf("%+v", d))
	cs := snapshot(t, f, src)
	seen = append(seen, fmt.Sprintf("%+v", cs), fmt.Sprintf("%+v", src))
	files, _ := fetchAll(t, f, src, cs.To, refsOf(cs))
	seen = append(seen, fmt.Sprintf("%+v", files))
	// Error paths: a refused item, an unknown version, and a rate limit.
	collect := func(err error) {
		if err != nil {
			seen = append(seen, err.Error(), fmt.Sprintf("%+v", err))
		}
	}
	collect(f.Connector().FetchFiles(ctx(t), src, cs.To, []connector.FileRef{{ItemID: "missing.md"}}, discard))
	collect(f.Connector().FetchFiles(ctx(t), src, strings.Repeat("0", len(cs.To)), []connector.FileRef{{ItemID: "a.md"}}, discard))
	clear := f.RateLimit(t)
	_, err := f.Connector().Changes(ctx(t), src, cs.To)
	collect(err)
	clear()
	for _, s := range seen {
		if strings.Contains(s, secret) {
			t.Fatalf("the credential leaked into connector output: %q", s)
		}
	}
	if !f.CredentialPresented() {
		t.Fatal("the provider never received the credential: the connector did not authenticate through the host")
	}
}

func discard(connector.File, io.Reader) error { return nil }

func testIncremental(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha", "b.md": "bravo", "d.md": "delta"}, nil, nil)
	src := f.Source(t, org1)
	a := snapshot(t, f, src)
	f.Apply(t, map[string]string{"a.md": "alpha 2", "c.md": "charlie"}, []string{"b.md"}, map[string]string{"d.md": "e.md"})
	cs, err := f.Connector().Changes(ctx(t), src, a.To)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Complete || cs.From != a.To || cs.To == a.To {
		t.Fatalf("an incremental change set goes from A to a new B and is not complete: %+v", cs)
	}
	got := byID(cs)
	want := map[string]connector.ChangeKind{"a.md": connector.ChangeModified, "c.md": connector.ChangeAdded, "b.md": connector.ChangeDeleted, "e.md": connector.ChangeMoved}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v, want exactly %v", cs.Changes, want)
	}
	for id, kind := range want {
		c, ok := got[id]
		if !ok || c.Kind != kind {
			t.Fatalf("change for %s = %+v, want %s", id, c, kind)
		}
		if kind != connector.ChangeDeleted {
			if msg := connector.ReadersError(c.Readers); msg != "" {
				t.Fatalf("change %s: %s", id, msg)
			}
		}
	}
	if got["e.md"].PreviousItemID != "d.md" {
		t.Fatalf("a move names where it came from: %+v", got["e.md"])
	}
	if got["a.md"].ItemVersion == byID(a)["a.md"].ItemVersion {
		t.Fatal("an edit keeps the item's id and moves its version")
	}
	same, err := f.Connector().Changes(ctx(t), src, cs.To)
	if err != nil || len(same.Changes) != 0 || same.To != cs.To || same.From != cs.To {
		t.Fatalf("B to B is empty: %+v, %v", same, err)
	}
	again, err := f.Connector().Changes(ctx(t), src, a.To)
	if err != nil || again.IdempotencyKey() != cs.IdempotencyKey() || len(again.Changes) != len(cs.Changes) {
		t.Fatalf("replaying A to B yields the same change set under the same key: %+v, %v", again, err)
	}
}

func testResync(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha"}, nil, nil)
	src := f.Source(t, org1)
	a := snapshot(t, f, src)
	f.Rewrite(t)
	_, err := f.Connector().Changes(ctx(t), src, a.To)
	if !errors.Is(err, connector.ErrResyncRequired) {
		t.Fatalf("changes from a rewritten-away version = %v, want ErrResyncRequired", err)
	}
	if _, err := f.Connector().Changes(ctx(t), src, "not-a-version"); !errors.Is(err, connector.ErrResyncRequired) {
		t.Fatalf("changes from an unknown version = %v, want ErrResyncRequired", err)
	}
	snapshot(t, f, src)
}

func testDeletion(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"keep.md": "k", "gone.md": "g"}, nil, nil)
	src := f.Source(t, org1)
	a := snapshot(t, f, src)
	f.Apply(t, nil, []string{"gone.md"}, nil)
	b, err := f.Connector().Changes(ctx(t), src, a.To)
	if err != nil || byID(b)["gone.md"].Kind != connector.ChangeDeleted {
		t.Fatalf("deleting an item is a DELETED change: %+v, %v", b, err)
	}
	full := snapshot(t, f, src)
	if _, ok := byID(full)["gone.md"]; ok {
		t.Fatal("a complete snapshot lists a deleted item")
	}
	err = f.Connector().FetchFiles(ctx(t), src, full.To, []connector.FileRef{{ItemID: "gone.md"}}, discard)
	if !errors.Is(err, connector.ErrItemNotFound) {
		t.Fatalf("fetching a deleted item = %v, want ErrItemNotFound", err)
	}
	f.Apply(t, map[string]string{"keep.md": "k2"}, nil, nil)
	c, err := f.Connector().Changes(ctx(t), src, b.To)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byID(c)["gone.md"]; ok {
		t.Fatal("a deleted item came back in a later change set")
	}
}

func testProvenance(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha"}, nil, nil)
	src := f.Source(t, org1)
	a := snapshot(t, f, src)
	f.Apply(t, map[string]string{"a.md": "alpha 2"}, nil, nil)
	b, err := f.Connector().Changes(ctx(t), src, a.To)
	if err != nil {
		t.Fatal(err)
	}
	change := byID(b)["a.md"]
	files, bodies := fetchAll(t, f, src, b.To, []connector.FileRef{{ItemID: "a.md", ItemVersion: change.ItemVersion}})
	if bodies["a.md"] != "alpha 2" || files["a.md"].Provenance.GetItemVersion() != change.ItemVersion || files["a.md"].Provenance.GetVersion() != b.To {
		t.Fatalf("the fetched item is the version its change named: %+v %q", files["a.md"], bodies["a.md"])
	}
	// The old item version is no longer in scope at B.
	err = f.Connector().FetchFiles(ctx(t), src, b.To, []connector.FileRef{{ItemID: "a.md", ItemVersion: byID(a)["a.md"].ItemVersion}}, discard)
	if !errors.Is(err, connector.ErrItemNotFound) {
		t.Fatalf("fetching a superseded item version = %v, want ErrItemNotFound", err)
	}
	if err := f.Connector().FetchFiles(ctx(t), src, strings.Repeat("0", len(b.To)), []connector.FileRef{{ItemID: "a.md"}}, discard); !errors.Is(err, connector.ErrVersionNotFound) {
		t.Fatalf("fetching at a version the provider never had = %v, want ErrVersionNotFound", err)
	}
}

// bulkConstant is the fixed provider-call overhead a bulk fetch may spend on
// top of one call per MaxItemsPerCall items (resolving a version, say).
const bulkConstant = 3

func testBulk(t *testing.T, f FilesFixture) {
	const n = 120
	puts := map[string]string{}
	for i := 0; i < n; i++ {
		puts[fmt.Sprintf("bulk/%03d.md", i)] = fmt.Sprintf("file %d", i)
	}
	f.Apply(t, puts, nil, nil)
	src := f.Source(t, org1)
	cs := snapshot(t, f, src)
	refs := refsOf(cs)
	max := f.Connector().Descriptor().Budget.MaxItemsPerCall
	before := f.ProviderCalls()
	_, bodies := fetchAll(t, f, src, cs.To, refs)
	calls := f.ProviderCalls() - before
	if len(bodies) != n {
		t.Fatalf("fetched %d of %d", len(bodies), n)
	}
	if limit := int64((n+max-1)/max + bulkConstant); calls > limit {
		t.Fatalf("fetching %d items cost %d provider calls, over %d: the fetch is per item", n, calls, limit)
	}
	var tooMany []connector.FileRef
	for i := 0; i <= max; i++ {
		tooMany = append(tooMany, connector.FileRef{ItemID: fmt.Sprintf("x/%d", i)})
	}
	if err := f.Connector().FetchFiles(ctx(t), src, cs.To, tooMany, discard); !errors.Is(err, connector.ErrBatchTooLarge) {
		t.Fatalf("a batch past MaxItemsPerCall = %v, want ErrBatchTooLarge", err)
	}
}

func testRateLimit(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha"}, nil, nil)
	src := f.Source(t, org1)
	clear := f.RateLimit(t)
	defer clear()
	start := time.Now()
	before := f.ProviderCalls()
	_, err := f.Connector().Changes(ctx(t), src, "")
	var limited *connector.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("a rate-limited provider = %v, want *RateLimitedError", err)
	}
	if !limited.ResetAt.After(start) || limited.Scope == "" {
		t.Fatalf("a rate limit names a reset in the future and what it meters: %+v", limited)
	}
	if calls := f.ProviderCalls() - before; calls > bulkConstant {
		t.Fatalf("a rate-limited connector made %d calls: it must stop, not retry", calls)
	}
}

func testLimits(t *testing.T, f FilesFixture) {
	max := f.MaxItemBytes()
	f.Apply(t, map[string]string{"big.bin": strings.Repeat("x", int(max)+1), "ok.md": "fine"}, nil, nil)
	src := f.Source(t, org1)
	cs := snapshot(t, f, src)
	visited := 0
	err := f.Connector().FetchFiles(ctx(t), src, cs.To, []connector.FileRef{{ItemID: "ok.md"}, {ItemID: "big.bin"}}, func(connector.File, io.Reader) error {
		visited++
		return nil
	})
	if !errors.Is(err, connector.ErrItemTooLarge) {
		t.Fatalf("an over-limit item = %v, want ErrItemTooLarge", err)
	}
	if visited != 0 {
		t.Fatal("a batch that cannot be served whole is refused before its first file")
	}
	dup := []connector.FileRef{{ItemID: "ok.md"}, {ItemID: "ok.md"}}
	if err := f.Connector().FetchFiles(ctx(t), src, cs.To, dup, discard); err == nil {
		t.Fatal("a batch naming one item twice is refused")
	}
	if err := f.Connector().FetchFiles(ctx(t), src, cs.To, nil, discard); err == nil {
		t.Fatal("an empty batch is refused")
	}
}

func testCancel(t *testing.T, f FilesFixture) {
	f.Apply(t, map[string]string{"a.md": "alpha"}, nil, nil)
	src := f.Source(t, org1)
	cs := snapshot(t, f, src)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Connector().Changes(cancelled, src, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("changes under a cancelled context = %v, want context.Canceled", err)
	}
	if err := f.Connector().FetchFiles(cancelled, src, cs.To, refsOf(cs), discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch under a cancelled context = %v, want context.Canceled", err)
	}
}
