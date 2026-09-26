package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var ws = Workspace{Org: "0190aaaa-0000-7000-8000-000000000001", Source: "0190aaaa-0000-7000-8000-000000000002"}

func inDocsMarkdown(p string) bool {
	return strings.HasPrefix(p, "docs/") && strings.HasSuffix(p, ".md")
}

func TestDefaultBranchAndResolveCommitOverGit(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	commit := gh.repo("docs")(map[string]*string{"README.md": str("hi")})
	c := gh.client("")

	branch, err := c.DefaultBranch(context.Background(), "acme/docs")
	if err != nil || branch != "main" {
		t.Fatalf("default branch = %q, %v", branch, err)
	}
	got, err := c.ResolveCommit(context.Background(), "acme/docs", "main")
	if err != nil || got != commit {
		t.Fatalf("resolve = %q, %v; want %s", got, err, commit)
	}
	if _, err := c.ResolveCommit(context.Background(), "acme/docs", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing branch err = %v, want ErrNotFound", err)
	}
	if rest, _ := c.Usage(); rest != 0 || gh.restCalls.Load() != 0 {
		t.Fatalf("resolving a branch spent %d REST calls, want 0", gh.restCalls.Load())
	}
}

// A snapshot of many files is a bounded number of requests, whatever the file
// count: the commit, its trees, and one batch of blobs. Nothing goes to REST.
func TestSnapshotOfManyFilesIsABoundedNumberOfRequests(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	files := map[string]*string{"README.md": str("root"), "src/main.go": str("package main")}
	for i := range 120 {
		files[fmt.Sprintf("docs/section-%03d.md", i)] = str(fmt.Sprintf("# Section %d\n\nbody %d\n", i, i))
	}
	files["docs/image.png"] = str("not markdown")
	files["documents/other.md"] = str("outside the prefix")
	commit := gh.repo("docs")(files)
	c := gh.client("")

	repo, err := c.OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	listed, err := repo.List(context.Background(), commit, inDocsMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 120 {
		t.Fatalf("listed %d files, want the 120 docs/*.md", len(listed))
	}
	ids := make([]string, 0, len(listed))
	for _, f := range listed {
		ids = append(ids, f.SHA)
	}
	if err := repo.Fetch(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	sizes, err := repo.Sizes(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range listed {
		if want := int64(len(*files[f.Path])); sizes[f.SHA] != want {
			t.Fatalf("%s size = %d, want %d", f.Path, sizes[f.SHA], want)
		}
	}
	// commits (1) + trees (1) + blobs (1).
	if got := gh.uploadPacks.Load(); got != 3 {
		t.Fatalf("snapshot of 120 files made %d upload-pack requests, want 3", got)
	}
	if gh.restCalls.Load() != 0 {
		t.Fatalf("snapshot spent %d REST calls, want 0", gh.restCalls.Load())
	}

	// A second pass over the same version is served from the mirror.
	before := gh.uploadPacks.Load()
	if _, err := repo.List(context.Background(), commit, inDocsMarkdown); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fetch(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if got := gh.uploadPacks.Load() - before; got != 0 {
		t.Fatalf("a warm mirror made %d requests, want 0", got)
	}
}

func TestStreamServesBlobsInRequestOrderFromOneReader(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	commit := gh.repo("docs")(map[string]*string{"docs/a.md": str("alpha"), "docs/b.md": str(""), "docs/c.md": str("gamma!")})
	repo, err := gh.client("").OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	listed, err := repo.List(context.Background(), commit, inDocsMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Path > listed[j].Path })
	ids := []string{listed[0].SHA, listed[1].SHA, listed[2].SHA}
	if err := repo.Fetch(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	var got []string
	err = repo.Stream(context.Background(), ids, func(id string, size int64, content io.Reader) error {
		b, err := io.ReadAll(content)
		if err != nil {
			return err
		}
		if int64(len(b)) != size {
			return fmt.Errorf("size %d, read %d", size, len(b))
		}
		got = append(got, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "gamma!||alpha" {
		t.Fatalf("stream = %q, want request order", got)
	}
	if _, err := repo.Read(context.Background(), ids[0], 3); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("read past max err = %v, want ErrFileTooLarge", err)
	}
}

// A blob the mirror does not hold is an error, never a hidden per-object fetch.
func TestLocalReadsNeverFetchLazily(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	commit := gh.repo("docs")(map[string]*string{"docs/a.md": str("alpha")})
	repo, err := gh.client("").OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	listed, err := repo.List(context.Background(), commit, inDocsMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	before := gh.uploadPacks.Load()
	if _, err := repo.Read(context.Background(), listed[0].SHA, 1024); err == nil {
		t.Fatal("read of an unfetched blob succeeded")
	}
	if gh.uploadPacks.Load() != before {
		t.Fatal("a local read reached the network")
	}
}

func TestCompareOverGit(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	commit := gh.repo("docs")
	base := commit(map[string]*string{"docs/keep.md": str("same"), "docs/edit.md": str("v1"), "docs/gone.md": str("bye"), "docs/move.md": str("moving content")})
	head := commit(map[string]*string{"docs/edit.md": str("v2"), "docs/gone.md": nil, "docs/new.md": str("hello"), "docs/move.md": nil, "docs/moved.md": str("moving content")})
	repo, err := gh.client("").OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	cmp, err := repo.Compare(context.Background(), base, head)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Status != CompareStatusAhead || cmp.Truncated {
		t.Fatalf("status = %s truncated=%v, want ahead", cmp.Status, cmp.Truncated)
	}
	got := map[string]string{}
	for _, f := range cmp.Files {
		got[f.Filename] = f.Status + ":" + f.PreviousFilename
	}
	want := map[string]string{"docs/edit.md": "modified:", "docs/gone.md": "removed:", "docs/new.md": "added:", "docs/moved.md": "renamed:docs/move.md"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	if back, err := repo.Compare(context.Background(), head, base); err != nil || back.Status != CompareStatusBehind {
		t.Fatalf("reverse = %v, %v; want behind", back, err)
	}
	if same, err := repo.Compare(context.Background(), head, head); err != nil || same.Status != CompareStatusIdentical {
		t.Fatalf("identical = %v, %v", same, err)
	}
	other := gh.divergeFrom("docs", base, map[string]*string{"docs/fork.md": str("fork")}, commit)
	if div, err := repo.Compare(context.Background(), head, other); err != nil || div.Status != CompareStatusDiverged {
		t.Fatalf("diverged = %v, %v", div, err)
	}
	if gh.restCalls.Load() != 0 {
		t.Fatalf("compare spent %d REST calls", gh.restCalls.Load())
	}
}

// A base the remote no longer holds — a force push orphaned it and it was
// never fetched — is ErrNotFound, which the compiler answers with a snapshot.
func TestCompareMissingBaseIsNotFound(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	commit := gh.repo("docs")
	first := commit(map[string]*string{"docs/a.md": str("a")})
	orphan := commit(map[string]*string{"docs/b.md": str("b")})
	gh.forcePush("docs", first)
	gh.gitCmd(filepath.Join(gh.root, "acme", "docs.git"), "reflog", "expire", "--expire=now", "--all")
	gh.gitCmd(filepath.Join(gh.root, "acme", "docs.git"), "gc", "--quiet", "--prune=now")
	head := commit(map[string]*string{"docs/c.md": str("c")})

	repo, err := gh.client("").OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	if _, err := repo.Compare(context.Background(), orphan, head); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphaned base err = %v, want ErrNotFound", err)
	}
}

func TestListRefusesTooManyFiles(t *testing.T) {
	gh := newFakeGitHub(t)
	commit := gh.repo("docs")(map[string]*string{"docs/a.md": str("a"), "docs/b.md": str("b"), "docs/c.md": str("c")})
	old := maxListedFiles
	maxListedFiles = 2
	t.Cleanup(func() { maxListedFiles = old })
	repo, err := gh.client("").OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	if _, err := repo.List(context.Background(), commit, inDocsMarkdown); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("err = %v, want ErrTooManyFiles", err)
	}
}

// A mirror that would outgrow its bound is discarded and the fetch refused.
func TestMirrorSizeBoundFailsClosed(t *testing.T) {
	gh := newFakeGitHub(t)
	files := map[string]*string{}
	for i := range 4 {
		raw := make([]byte, 256<<10)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		content := hex.EncodeToString(raw) // incompressible past what zlib can take back
		files[fmt.Sprintf("docs/big-%d.md", i)] = &content
	}
	commit := gh.repo("docs")(files)
	c := gh.client("")
	repo, err := c.OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	listed, err := repo.List(context.Background(), commit, inDocsMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	old := maxMirrorBytes
	maxMirrorBytes = 64 << 10
	t.Cleanup(func() { maxMirrorBytes = old })
	ids := []string{}
	for _, f := range listed {
		ids = append(ids, f.SHA)
	}
	if err := repo.Fetch(context.Background(), ids); !errors.Is(err, ErrRepositoryTooLarge) {
		t.Fatalf("err = %v, want ErrRepositoryTooLarge", err)
	}
	if _, err := os.Stat(repo.dir); !os.IsNotExist(err) {
		t.Fatalf("oversized mirror kept: %v", err)
	}
}

// The credential reaches GitHub as an Authorization header scoped to the git
// origin, and is written nowhere on disk and never into the remote URL.
func TestCredentialTravelsAsHeaderOnly(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	gh.requireAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:s3cret-token"))
	commit := gh.repo("docs")(map[string]*string{"docs/a.md": str("a")})
	root := t.TempDir()
	c := New("s3cret-token", gh.server.URL, WithCacheRoot(root))
	repo, err := c.OpenRepository(context.Background(), ws, "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := repo.List(context.Background(), commit, inDocsMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Fetch(context.Background(), []string{listed[0].SHA}); err != nil {
		t.Fatal(err)
	}
	_ = repo.Close()
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "s3cret-token") || strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("x-access-token:s3cret-token"))) {
			t.Errorf("credential written to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Without the credential the same read is refused, as GitHub refuses a
	// private repository: not found, when nothing was presented.
	if _, err := gh.client("").ResolveCommit(context.Background(), "acme/docs", "main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("anonymous read of a credentialed repo err = %v, want ErrNotFound", err)
	}
	if _, err := gh.client("wrong").ResolveCommit(context.Background(), "acme/docs", "main"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong credential err = %v, want ErrUnauthorized", err)
	}
}

func TestGitRefusalsAreClassified(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	gh.repo("docs")(map[string]*string{"docs/a.md": str("a")})
	for _, tc := range []struct {
		status int
		token  string
		want   error
	}{
		{http.StatusTooManyRequests, "", ErrUnauthenticatedRateLimited},
		{http.StatusTooManyRequests, "tok", ErrRateLimited},
		{http.StatusForbidden, "tok", ErrForbidden},
		{http.StatusNotFound, "", ErrNotFound},
	} {
		gh.refuse.Store(int32(tc.status))
		_, err := gh.client(tc.token).ResolveCommit(context.Background(), "acme/docs", "main")
		if !errors.Is(err, tc.want) {
			t.Fatalf("status %d token=%q: err = %v, want %v", tc.status, tc.token, err, tc.want)
		}
		if tc.token != "" && strings.Contains(err.Error(), tc.token) {
			t.Fatal("credential leaked into the error")
		}
	}
	gh.refuse.Store(0)
}

func TestWorkspaceAndRepositoryAreValidatedBeforeAnyPath(t *testing.T) {
	c := New("", "http://127.0.0.1:1", WithCacheRoot(t.TempDir()))
	if _, err := c.OpenRepository(context.Background(), Workspace{Org: "../x", Source: "y"}, "acme/docs"); err == nil {
		t.Fatal("a traversing workspace was accepted")
	}
	if _, err := c.OpenRepository(context.Background(), ws, "acme/../docs"); err == nil {
		t.Fatal("a traversing repository was accepted")
	}
}

func TestGitBaseURLFor(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.github.com":            "https://github.com",
		"https://ghe.example.com/api/v3":    "https://ghe.example.com",
		"https://ghe.example.com/api/v3/":   "https://ghe.example.com",
		"http://127.0.0.1:8080":             "http://127.0.0.1:8080",
		"https://git.example.com/prefix/v1": "https://git.example.com/prefix/v1",
	} {
		if got, _ := gitBaseURLFor(in); got != want {
			t.Errorf("gitBaseURLFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// The mirrors under one root are bounded together: a sweep discards the least
// recently used mirrors until the rest fit, and never one an operation holds.
func TestSweepDiscardsLeastRecentlyUsedMirrors(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, age time.Duration, size int) string {
		dir := filepath.Join(root, "org", name)
		if err := os.MkdirAll(filepath.Join(dir, "repo.git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "repo.git", "pack"), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		lock := filepath.Join(dir, "lock")
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		used := time.Now().Add(-age)
		if err := os.Chtimes(lock, used, used); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	oldest := mk("oldest", 3*time.Hour, 400)
	held := mk("held", 2*time.Hour, 400)
	recent := mk("recent", time.Minute, 400)

	sweepMirrors(root, 900, held)
	if _, err := os.Stat(filepath.Join(oldest, "repo.git")); !os.IsNotExist(err) {
		t.Fatal("the least recently used mirror survived a sweep over the bound")
	}
	for _, dir := range []string{held, recent} {
		if _, err := os.Stat(filepath.Join(dir, "repo.git")); err != nil {
			t.Fatalf("the sweep discarded %s, more than it needed to", dir)
		}
	}

	// Tighter still: the next least recent is held, so it is skipped, and the
	// most recent goes instead.
	sweepMirrors(root, 500, held)
	if _, err := os.Stat(filepath.Join(held, "repo.git")); err != nil {
		t.Fatal("a held mirror was discarded")
	}
	if _, err := os.Stat(filepath.Join(recent, "repo.git")); !os.IsNotExist(err) {
		t.Fatal("the sweep stopped over the bound")
	}
}
