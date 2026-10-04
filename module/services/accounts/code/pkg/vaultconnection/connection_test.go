package vaultconnection

import (
	"context"
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
		// Off loopback, the Kubernetes-auth rule already refuses http, so this
		// case would pass with the deployed check gone. The loopback case below
		// is the one only the deployed check catches, and both assert their own
		// wording rather than a substring another refusal also contains.
		// No CAFile here on purpose: a CA beside an http address trips the older
		// "projected identity requires HTTPS" rule first, which would make this
		// case pass with the deployed check gone.
		"cleartext address": {
			Config{Address: "http://vault.vault.svc.cluster.local:8200", Kubernetes: role(), Runtime: RuntimeDeployed},
			"refusing a cleartext Vault address outside the local environment",
		},
		"cleartext loopback, asserted as protected": {
			Config{Address: "http://127.0.0.1:8200", Kubernetes: role(), AllowInsecureHTTP: true, Runtime: RuntimeDeployed},
			"refusing a cleartext Vault address outside the local environment",
		},
		"asserted insecure http": {
			Config{Address: server.URL, CAFile: ca, Kubernetes: role(), AllowInsecureHTTP: true, Runtime: RuntimeDeployed},
			"refusing VAULT_ALLOW_INSECURE_HTTP=true outside the local environment",
		},
		"static token beside kubernetes auth": {
			Config{Address: server.URL, CAFile: ca, Kubernetes: role(), Token: "provisioned", Runtime: RuntimeDeployed},
			"refusing a provisioned Vault token outside the local environment",
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

// group sets a key on the real hosted carrier the deployment uses, so these
// exercise Load's own branches rather than a convenience shortcut: the
// incident was a deployed process reading the wrong source, and only the real
// carrier proves which source Load actually consulted.
func group(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__VAULT__"+key, value)
}

func hosted(t *testing.T) {
	t.Helper()
	t.Setenv("CODEFLY__ENVIRONMENT", "hosted")
	// Load falls back to a plain process variable when the group carries
	// nothing, so a stray value from the environment running the test would
	// silently satisfy the very branch under test.
	for _, key := range []string{
		"VAULT_ADDR", "VAULT_CA_FILE", "VAULT_AUTH_METHOD", "VAULT_K8S_ROLE",
		"VAULT_K8S_MOUNT", "VAULT_K8S_TOKEN_PATH", "VAULT_ALLOW_INSECURE_HTTP",
	} {
		t.Setenv(key, "")
	}
}

// Load's own refusals, each on the real carrier and each asserted on its own
// wording. New refuses most of these a second time, which is deliberate
// defence in depth — but it also means a loose assertion here would stay green
// with Load's branch deleted, so every case names the text only Load produces.
func TestLoadRefusesAHostedBindingTheCompositionDidNotName(t *testing.T) {
	t.Run("no group at all", func(t *testing.T) {
		hosted(t)
		_, err := Load(context.Background())
		requireRefusal(t, err, "VAULT_ADDR is required outside the local environment")
	})

	t.Run("the composed service's own projection is not inherited", func(t *testing.T) {
		hosted(t)
		// The in-module `vault` service's address and token projections, exactly
		// as Codefly delivers them. On a cell this is what accounts used to fall
		// back to, and falling back to it is the incident.
		t.Setenv("CODEFLY__SERVICE_CONFIGURATION__SAAS_STARTER__VAULT___VAULT__ADDRESS", "http://vault:8200")
		t.Setenv("CODEFLY__SERVICE_SECRET_CONFIGURATION__SAAS_STARTER__VAULT___VAULT__TOKEN", "root-token")
		_, err := Load(context.Background())
		requireRefusal(t, err, "VAULT_ADDR is required outside the local environment")
		requireRefusal(t, err, "never inherited by a deployed product")
	})

	t.Run("an address but no auth method", func(t *testing.T) {
		hosted(t)
		group(t, "VAULT_ADDR", "https://vault.vault.svc.cluster.local:8200")
		_, err := Load(context.Background())
		requireRefusal(t, err, "refusing a Vault token binding outside the local environment")
	})

	t.Run("token auth named outright", func(t *testing.T) {
		hosted(t)
		group(t, "VAULT_ADDR", "https://vault.vault.svc.cluster.local:8200")
		group(t, "VAULT_AUTH_METHOD", "token")
		_, err := Load(context.Background())
		requireRefusal(t, err, "refusing a Vault token binding outside the local environment")
	})

	t.Run("kubernetes auth with no role", func(t *testing.T) {
		hosted(t)
		group(t, "VAULT_ADDR", "https://vault.vault.svc.cluster.local:8200")
		group(t, "VAULT_AUTH_METHOD", "kubernetes")
		_, err := Load(context.Background())
		requireRefusal(t, err, "VAULT_K8S_ROLE")
	})
}

// The CA path is this render's own, so Load must supply it on a hosted profile
// rather than make a cell restate a constant out of this repository. The
// refusal naming the missing projection is how that default is observable
// without a mounted file.
func TestLoadDefaultsTheCAPathTheRenderOwns(t *testing.T) {
	hosted(t)
	group(t, "VAULT_ADDR", "https://vault.vault.svc.cluster.local:8200")
	group(t, "VAULT_AUTH_METHOD", "kubernetes")
	group(t, "VAULT_K8S_ROLE", "accounts")
	_, err := Load(context.Background())
	if err == nil {
		t.Fatal("a Vault binding whose CA is not mounted was accepted")
	}
	// It got past the "requires https and VAULT_CA_FILE" rule, which only an
	// already-defaulted CA path can do, and failed reading that path instead.
	if strings.Contains(err.Error(), "VAULT_CA_FILE") {
		t.Fatalf("the CA path was not defaulted to %s: %v", DefaultCAFile, err)
	}
	requireRefusal(t, err, "Vault projection")
}

func requireRefusal(t *testing.T, err error, names string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want a refusal naming %q", names)
	}
	if !strings.Contains(err.Error(), names) {
		t.Fatalf("refusal does not name %q: %v", names, err)
	}
}
