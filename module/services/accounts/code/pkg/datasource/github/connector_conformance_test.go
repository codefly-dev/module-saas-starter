package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/connector/connectortest"
)

// TestFilesConnectorConformance runs the datasource conformance suite against
// the GitHub connector, over a local git smart-HTTP server that requires a
// sentinel credential and counts every request.
func TestFilesConnectorConformance(t *testing.T) {
	connectortest.RunFiles(t, newConformanceFixture)
}

const conformanceToken = "sentinel-credential-7f3a"
const conformanceMaxItemBytes = 4 << 10

type conformanceFixture struct {
	gh       *fakeGitHub
	commit   func(map[string]*string) string
	conn     *FilesConnector
	sources  int
	rewrites int
}

func newConformanceFixture(t *testing.T) connectortest.FilesFixture {
	gh := newFakeGitHub(t)
	gh.requireAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+conformanceToken))
	f := &conformanceFixture{gh: gh, commit: gh.repo("docs")}
	cacheRoot := t.TempDir()
	f.conn = NewFilesConnector(func(context.Context, connector.Source) (Remote, error) {
		// The host resolves the credential per call; here it is the sentinel.
		return New(conformanceToken, gh.server.URL, WithCacheRoot(cacheRoot)), nil
	}, WithMaxItemBytes(conformanceMaxItemBytes))
	return f
}

func (f *conformanceFixture) Connector() connector.FilesConnector { return f.conn }

func (f *conformanceFixture) Source(t *testing.T, org string) connector.Source {
	f.sources++
	return connector.Source{
		ID:             fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", f.sources),
		OrgID:          org,
		BoundaryNodeID: fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", f.sources),
		Config:         SourceConfig{Repo: "acme/docs", Branch: "main", InScope: func(string) bool { return true }},
	}
}

func (f *conformanceFixture) Apply(t *testing.T, puts map[string]string, deletes []string, moves map[string]string) {
	t.Helper()
	files := map[string]*string{}
	for path, content := range puts {
		files[path] = str(content)
	}
	for _, path := range deletes {
		files[path] = nil
	}
	f.commit(files)
	if len(moves) > 0 {
		work := filepath.Join(f.gh.work, "docs")
		for from, to := range moves {
			f.gh.gitCmd(work, "mv", from, to)
		}
		f.commit(map[string]*string{})
	}
}

func (f *conformanceFixture) Rewrite(t *testing.T) {
	t.Helper()
	f.rewrites++
	work := filepath.Join(f.gh.work, "docs")
	// A new root: nothing the connector saw before is an ancestor of it.
	f.gh.gitCmd(work, "checkout", "--quiet", "--orphan", fmt.Sprintf("rewrite-%d", f.rewrites))
	f.commit(map[string]*string{"rewritten.md": str("history was rewritten")})
}

func (f *conformanceFixture) ProviderCalls() int64 {
	return f.gh.gitRequests.Load() + f.gh.restCalls.Load()
}

func (f *conformanceFixture) RateLimit(t *testing.T) func() {
	f.gh.refuse.Store(http.StatusTooManyRequests)
	return func() { f.gh.refuse.Store(0) }
}

func (f *conformanceFixture) Credential() string { return conformanceToken }

func (f *conformanceFixture) CredentialPresented() bool {
	f.gh.mu.Lock()
	defer f.gh.mu.Unlock()
	for _, h := range f.gh.authHeaders {
		if h == f.gh.requireAuth {
			return true
		}
	}
	return false
}

func (f *conformanceFixture) MaxItemBytes() int64 { return conformanceMaxItemBytes }

// TestFilesConnectorScopesItems holds the connector to the host's scope: an
// out-of-scope file is neither listed nor fetchable, and a move across the
// scope's edge is an addition or a deletion, never a move.
func TestFilesConnectorScopesItems(t *testing.T) {
	f := newConformanceFixture(t).(*conformanceFixture)
	f.Apply(t, map[string]string{"docs/a.md": "a", "src/b.go": "b", "docs/c.md": "c"}, nil, nil)
	src := f.Source(t, "11111111-1111-1111-1111-111111111111")
	src.Config = SourceConfig{Repo: "acme/docs", Branch: "main", InScope: func(p string) bool { return strings.HasPrefix(p, "docs/") }}
	ctx := context.Background()
	a, err := f.conn.Changes(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Changes) != 2 {
		t.Fatalf("snapshot = %+v, want the two docs/ files", a.Changes)
	}
	if err := f.conn.FetchFiles(ctx, src, a.To, []connector.FileRef{{ItemID: "src/b.go"}}, func(connector.File, io.Reader) error { return nil }); err == nil {
		t.Fatal("an out-of-scope file must not be fetchable")
	}
	f.Apply(t, nil, nil, map[string]string{"docs/a.md": "src/a.md", "src/b.go": "docs/b.go"})
	b, err := f.conn.Changes(ctx, src, a.To)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]connector.ChangeKind{}
	for _, c := range b.Changes {
		kinds[c.Key.ItemID] = c.Kind
	}
	if len(kinds) != 2 || kinds["docs/a.md"] != connector.ChangeDeleted || kinds["docs/b.go"] != connector.ChangeAdded {
		t.Fatalf("moves across the scope edge = %v, want docs/a.md deleted and docs/b.go added", kinds)
	}
}
