package github

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
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
