package github

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestDefaultBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/docs" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q, want Bearer tok", got)
		}
		_, _ = w.Write([]byte(`{"default_branch":"trunk"}`))
	}))
	defer srv.Close()

	branch, err := New("tok", srv.URL).DefaultBranch(context.Background(), "acme/docs")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "trunk" {
		t.Fatalf("branch = %q, want trunk", branch)
	}
}

func TestListFilesFiltersByPrefixAtSegmentBoundary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/docs/git/trees/main" || r.URL.Query().Get("recursive") != "1" {
			t.Errorf("unexpected tree request %q?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"truncated":false,"tree":[
			{"path":"docs","type":"tree","sha":"t1"},
			{"path":"docs/a.md","type":"blob","sha":"a"},
			{"path":"docs/sub/b.md","type":"blob","sha":"b"},
			{"path":"documents/c.md","type":"blob","sha":"c"},
			{"path":"README.md","type":"blob","sha":"r"}
		]}`))
	}))
	defer srv.Close()

	files, err := New("tok", srv.URL).ListFiles(context.Background(), "acme/docs", "main", []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.SHA
	}
	if len(got) != 2 || got["docs/a.md"] != "a" || got["docs/sub/b.md"] != "b" {
		t.Fatalf("filtered files = %v, want only docs/ blobs", got)
	}
}

func TestListFilesRejectsTruncatedTree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"truncated":true,"tree":[]}`))
	}))
	defer srv.Close()

	if _, err := New("tok", srv.URL).ListFiles(context.Background(), "acme/docs", "main", nil); err == nil {
		t.Fatal("want error on truncated tree, got nil")
	}
}

func TestGetFileContentDecodesBase64(t *testing.T) {
	payload := []byte("# Title\nbody\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/docs/contents/docs/a.md" || r.URL.Query().Get("ref") != "main" {
			t.Errorf("unexpected contents request %q?%s", r.URL.Path, r.URL.RawQuery)
		}
		// GitHub wraps the base64 body at column 60.
		encoded := base64.StdEncoding.EncodeToString(payload)
		_, _ = w.Write([]byte(`{"type":"file","encoding":"base64","size":13,"content":"` + encoded[:8] + "\\n" + encoded[8:] + `"}`))
	}))
	defer srv.Close()

	got, err := New("tok", srv.URL).GetFileContent(context.Background(), "acme/docs", "main", "docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("content = %q, want %q", got, payload)
	}
}

func TestGetFileContentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	if _, err := New("tok", srv.URL).GetFileContent(context.Background(), "acme/docs", "main", "missing.md"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetFileContentTooLarge(t *testing.T) {
	// GitHub returns a 200 with encoding "none" and empty content for files above
	// the contents-API 1 MiB cap. This must be ErrFileTooLarge (skippable), not a
	// generic error that would abort a whole sync.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"file","encoding":"none","size":2000000,"content":""}`))
	}))
	defer srv.Close()

	if _, err := New("tok", srv.URL).GetFileContent(context.Background(), "acme/docs", "main", "big.png"); err != ErrFileTooLarge {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestCompareMapsStatusAndFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/docs/compare/base...head" {
			t.Errorf("unexpected compare path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q, want Bearer tok", got)
		}
		_, _ = w.Write([]byte(`{"status":"ahead","files":[
			{"filename":"docs/a.md","status":"modified","sha":"a1"},
			{"filename":"src/new.go","previous_filename":"src/old.go","status":"renamed","sha":"r1"}
		]}`))
	}))
	defer srv.Close()

	cmp, err := New("tok", srv.URL).Compare(context.Background(), "acme/docs", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Status != CompareStatusAhead || cmp.Truncated || len(cmp.Files) != 2 {
		t.Fatalf("comparison = %+v", cmp)
	}
	if cmp.Files[1].PreviousFilename != "src/old.go" || cmp.Files[1].Status != "renamed" {
		t.Fatalf("rename file = %+v", cmp.Files[1])
	}
}

func TestCompareNotFoundOnMissingBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	if _, err := New("tok", srv.URL).Compare(context.Background(), "acme/docs", "gone", "head"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// compareFilesServer serves a compare whose files array has n entries, and
// records how many requests it received so the single-call contract can be
// asserted (GitHub never paginates the files array).
func compareFilesServer(t *testing.T, n int, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		var b strings.Builder
		b.WriteString(`{"status":"ahead","files":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"filename":"docs/` + strconv.Itoa(i) + `.md","status":"added","sha":"s"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
}

func TestCompareTruncatesAtFileCap(t *testing.T) {
	// GitHub caps a comparison's files at 300; at the cap the result must read
	// Truncated so the caller snapshots instead of trusting a partial op list.
	// The files array is not paginated, so Compare makes exactly one request.
	calls := 0
	srv := compareFilesServer(t, 300, &calls)
	defer srv.Close()

	cmp, err := New("tok", srv.URL).Compare(context.Background(), "acme/docs", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("compare made %d requests, want exactly 1", calls)
	}
	if !cmp.Truncated || len(cmp.Files) != 300 {
		t.Fatalf("comparison truncated=%v files=%d, want truncated at 300", cmp.Truncated, len(cmp.Files))
	}
}

func TestCompareDoesNotTruncateBelowFileCap(t *testing.T) {
	// One file short of the cap is a complete diff, not a truncated one.
	calls := 0
	srv := compareFilesServer(t, 299, &calls)
	defer srv.Close()

	cmp, err := New("tok", srv.URL).Compare(context.Background(), "acme/docs", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("compare made %d requests, want exactly 1", calls)
	}
	if cmp.Truncated || len(cmp.Files) != 299 {
		t.Fatalf("comparison truncated=%v files=%d, want 299 and not truncated", cmp.Truncated, len(cmp.Files))
	}
}

func TestGetBlobDecodesAndEnforcesLimit(t *testing.T) {
	payload := []byte("blob-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/docs/git/blobs/sha1" {
			t.Errorf("unexpected blob path %q", r.URL.Path)
		}
		encoded := base64.StdEncoding.EncodeToString(payload)
		_, _ = w.Write([]byte(`{"encoding":"base64","size":10,"content":"` + encoded + `"}`))
	}))
	defer srv.Close()

	got, err := New("tok", srv.URL).GetBlob(context.Background(), "acme/docs", "sha1", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("blob = %q, want %q", got, payload)
	}
	if _, err := New("tok", srv.URL).GetBlob(context.Background(), "acme/docs", "sha1", 5); err != ErrFileTooLarge {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestResolveCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/docs/commits/main" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"sha":"deadbeef","commit":{"message":"x"}}`))
	}))
	defer srv.Close()

	sha, err := New("tok", srv.URL).ResolveCommit(context.Background(), "acme/docs", "main")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "deadbeef" {
		t.Fatalf("sha = %q, want deadbeef", sha)
	}
}
