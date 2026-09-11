package vaultconnection

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectedTLSAndAtomicTokenRotation(t *testing.T) {
	tokens := make(chan string, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens <- r.Header.Get("X-Vault-Token")
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://untrusted.invalid")
			w.WriteHeader(307)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0400); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ca := write("ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	first := write("first", []byte("first-fixture-token"))
	second := write("second", []byte("second-fixture-token"))
	current := filepath.Join(dir, "current")
	if err := os.Symlink(first, current); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Address: server.URL, CAFile: ca, TokenFile: current, Token: "must-never-fallback"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, want string) {
		t.Helper()
		token, err := c.Token()
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("X-Vault-Token", token)
		resp, err := c.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := <-tokens; got != want {
			t.Fatal("wrong projected token")
		}
	}
	request("/", "first-fixture-token")
	next := filepath.Join(dir, "next")
	if err := os.Symlink(second, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, current); err != nil {
		t.Fatal(err)
	}
	request("/", "second-fixture-token")
	token, _ := c.Token()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/redirect", nil)
	req.Header.Set("X-Vault-Token", token)
	if _, err = c.Client.Do(req); err == nil {
		t.Fatal("Vault redirect accepted")
	}
	<-tokens
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Token(); err == nil {
		t.Fatal("missing projected token fell back to static token")
	}
	if _, err = New(Config{Address: "http://vault.invalid", TokenFile: current}); err == nil {
		t.Fatal("projected credentials accepted without TLS")
	}
	untrusted, err := New(Config{Address: server.URL, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = untrusted.Client.Get(server.URL); err == nil {
		t.Fatal("untrusted Vault certificate accepted")
	}
}

func TestProjectionPermissionsAndContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProjection(path); err == nil {
		t.Fatal("world-readable token accepted")
	}
	if err := os.Chmod(path, 0440); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProjection(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProjection("relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	c := &Connection{token: "bad\nheader"}
	if _, err := c.Token(); err == nil {
		t.Fatal("multiline token accepted")
	}
}
