package vaultconnection

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	c, err := New(Config{Address: server.URL, CAFile: ca, TokenFile: current, Token: "must-never-fallback", Runtime: RuntimeLocal})
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
	if _, err = New(Config{Address: "http://vault.invalid", TokenFile: current, Runtime: RuntimeLocal}); err == nil {
		t.Fatal("projected credentials accepted without TLS")
	}
	untrusted, err := New(Config{Address: server.URL, Token: "fixture", Runtime: RuntimeLocal})
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

func TestCleartextOnlyToLoopbackUnlessAsserted(t *testing.T) {
	if _, err := New(Config{Address: "http://vault.example.internal:8200", Token: "fixture", Runtime: RuntimeLocal}); err == nil {
		t.Fatal("cleartext to a non-loopback Vault accepted without the operator's assertion")
	}
	if _, err := New(Config{Address: "http://vault.example.internal:8200", Token: "fixture", AllowInsecureHTTP: true, Runtime: RuntimeLocal}); err != nil {
		t.Fatalf("asserted out-of-band protection refused: %v", err)
	}
	for _, address := range []string{"http://localhost:8200", "http://127.0.0.1:8200", "http://[::1]:8200"} {
		if _, err := New(Config{Address: address, Token: "fixture", Runtime: RuntimeLocal}); err != nil {
			t.Fatalf("loopback %s refused: %v", address, err)
		}
	}
}

// A deployed runtime accepts exactly one Vault binding: the Vault the
// composition names, over https, reached as accounts' own ServiceAccount. Every
// other shape is the local-development one, and a product reading its secrets
// store that way is the incident this refuses — so each case asserts both the
// refusal and the key it names, because a refusal that does not say which knob
// is wrong sends the reader to the wrong file.
func TestDeployedRuntimeAcceptsOnlyAPinnedKubernetesBinding(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"minted","lease_duration":3600,"renewable":true}}`))
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o400); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ca := write("ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	jwt := write("jwt", []byte("projected-sa-jwt"))
	role := func() *KubernetesAuth { return &KubernetesAuth{Role: "accounts", JWTPath: jwt} }

	for name, expect := range map[string]struct {
		config Config
		names  string
	}{
		"token auth": {
			Config{Address: server.URL, CAFile: ca, Token: "provisioned", Runtime: RuntimeDeployed},
			"VAULT_AUTH_METHOD=kubernetes",
		},
		"cleartext address": {
			Config{Address: "http://vault.vault.svc.cluster.local:8200", Kubernetes: role(), Runtime: RuntimeDeployed},
			"https",
		},
		"asserted insecure http": {
			Config{Address: server.URL, CAFile: ca, Kubernetes: role(), AllowInsecureHTTP: true, Runtime: RuntimeDeployed},
			"VAULT_ALLOW_INSECURE_HTTP",
		},
		"static token beside kubernetes auth": {
			Config{Address: server.URL, CAFile: ca, Kubernetes: role(), Token: "provisioned", Runtime: RuntimeDeployed},
			"Kubernetes auth",
		},
		"unstated runtime": {
			Config{Address: server.URL, CAFile: ca, Kubernetes: role()},
			"stated runtime",
		},
	} {
		_, err := New(expect.config)
		if err == nil {
			t.Errorf("%s: accepted on a deployed runtime", name)
			continue
		}
		if !strings.Contains(err.Error(), expect.names) {
			t.Errorf("%s: refusal does not name %q: %v", name, expect.names, err)
		}
	}

	// The one shape that is accepted, so the refusals above are not simply "a
	// deployed runtime never connects".
	if _, err := New(Config{Address: server.URL, CAFile: ca, Kubernetes: role(), Runtime: RuntimeDeployed}); err != nil {
		t.Fatalf("the pinned Kubernetes binding was refused on a deployed runtime: %v", err)
	}
}
