package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

// allowLoopback disables the SSRF dial guard for the duration of a test so it
// can reach an httptest server on 127.0.0.1, restoring the guarding
// implementation afterwards. Production behavior is unchanged.
func allowLoopback(t *testing.T) {
	t.Helper()
	prev := dialGuard
	dialGuard = func(_, _ string, _ syscall.RawConn) error { return nil }
	t.Cleanup(func() { dialGuard = prev })
}

// sitemapXML renders a sitemaps.org urlset listing the given loc URLs.
func sitemapXML(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, loc := range locs {
		fmt.Fprintf(&b, "<url><loc>%s</loc></url>", loc)
	}
	b.WriteString(`</urlset>`)
	return b.String()
}

func TestListAndFetchHappyPath(t *testing.T) {
	allowLoopback(t)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, sitemapXML(server.URL+"/a", server.URL+"/b"))
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "page a body")
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "page b body")
	})

	c := New(Config{SitemapURL: server.URL + "/sitemap.xml"})
	urls, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("got %d urls, want 2", len(urls))
	}

	byURL := map[string]Page{}
	for _, u := range urls {
		p, err := c.Fetch(context.Background(), u)
		if err != nil {
			t.Fatalf("Fetch %s: %v", u, err)
		}
		byURL[p.URL] = p
	}
	a, ok := byURL[server.URL+"/a"]
	if !ok {
		t.Fatalf("missing page /a: got %v", byURL)
	}
	if string(a.Body) != "page a body" {
		t.Errorf("page a body = %q", a.Body)
	}
	if a.ContentType != "text/html" {
		t.Errorf("page a content type = %q", a.ContentType)
	}
	b, ok := byURL[server.URL+"/b"]
	if !ok {
		t.Fatalf("missing page /b: got %v", byURL)
	}
	if string(b.Body) != "page b body" {
		t.Errorf("page b body = %q", b.Body)
	}
	if b.ContentType != "text/plain" {
		t.Errorf("page b content type = %q", b.ContentType)
	}
}

func TestListRespectsMaxPages(t *testing.T) {
	allowLoopback(t)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	locs := make([]string, 5)
	for i := range locs {
		locs[i] = fmt.Sprintf("%s/p%d", server.URL, i)
	}
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, sitemapXML(locs...))
	})

	c := New(Config{SitemapURL: server.URL + "/sitemap.xml", MaxPages: 2})
	urls, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("got %d urls, want 2", len(urls))
	}
}

func TestFetchReportsFailingPage(t *testing.T) {
	allowLoopback(t)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/boom", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	// A failing page now surfaces as a returned error (the caller decides whether
	// to skip it best-effort), not a silently dropped result.
	c := New(Config{SitemapURL: server.URL + "/sitemap.xml"})
	if _, err := c.Fetch(context.Background(), server.URL+"/boom"); err == nil {
		t.Fatal("a 500 page must return an error, not be silently dropped")
	}
}

func TestFetchReportsTooLargePage(t *testing.T) {
	allowLoopback(t)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, maxResponseBytes+1))
	})

	c := New(Config{SitemapURL: server.URL + "/sitemap.xml"})
	_, err := c.Fetch(context.Background(), server.URL+"/big")
	if !errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("oversized page: err = %v, want ErrPageTooLarge", err)
	}
}

func TestListBlocksLoopbackByDefault(t *testing.T) {
	// No allowLoopback here: the default guard must reject a loopback target.
	c := New(Config{SitemapURL: "http://127.0.0.1:1/sitemap.xml"})
	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("List against a loopback address must return an error")
	}
}
