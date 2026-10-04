package vaultconnection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The local shape: the composed `vault` service over loopback, with a token the
// platform provisions as a file that may be rotated under the process.
func TestProjectedTokenRotatesUnderTheProcess(t *testing.T) {
	tokens := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens <- r.Header.Get("X-Vault-Token")
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "http://untrusted.invalid")
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
		if err := os.WriteFile(path, data, 0o400); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := write("first", []byte("first-fixture-token"))
	second := write("second", []byte("second-fixture-token"))
	current := filepath.Join(dir, "current")
	if err := os.Symlink(first, current); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Address: server.URL, TokenFile: current, Token: "must-never-fallback", Runtime: RuntimeLocal})
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
	// A token file is still a file, so off loopback it is only ever read over
	// TLS — there is no CA to pin, but the hop must not be cleartext.
	if _, err = New(Config{Address: "http://vault.invalid", TokenFile: current, Runtime: RuntimeLocal}); err == nil {
		t.Fatal("a token file was accepted over cleartext off loopback")
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

// Plaintext off loopback is admitted by exactly one thing: the composition's
// mesh assertion, and only for an in-cluster Service address. The host rules
// themselves are exhausted in package meshtransport's own test; what this holds
// is that New consults them — that an admitted address gets *past* the address
// check and an unasserted one does not.
func TestCleartextNeedsBothTheMeshAssertionAndAnInClusterAddress(t *testing.T) {
	approle := func() *AppRoleAuth { return &AppRoleAuth{RoleID: "r", SecretID: "s"} }

	for _, address := range []string{"http://localhost:8200", "http://127.0.0.1:8200", "http://[::1]:8200"} {
		if _, err := New(Config{Address: address, Token: "fixture", Runtime: RuntimeLocal}); err != nil {
			t.Fatalf("loopback %s refused: %v", address, err)
		}
	}

	const inCluster = "http://vault.vault.svc.cluster.local:8200"

	// Asserted and in-cluster: the address check must let this through. Nothing
	// answers at that name here, so it fails at the login instead — which is
	// exactly the evidence that the address was admitted.
	_, err := New(Config{Address: inCluster, AppRole: approle(), Runtime: RuntimeDeployed, MeshProtected: true})
	if err != nil && strings.Contains(err.Error(), "refusing cleartext") {
		t.Errorf("the in-mesh cell Vault was refused on its address: %v", err)
	}

	// The same address with no assertion stays refused, and the refusal names
	// the one way through.
	_, err = New(Config{Address: inCluster, AppRole: approle(), Runtime: RuntimeDeployed})
	requireRefusal(t, err, "refusing cleartext http to a non-loopback Vault")
	requireRefusal(t, err, "internal-transport/mesh-protected=true")

	// Asserted, but a host no mesh can cover.
	_, err = New(Config{Address: "http://vault.svc.example.com:8200", AppRole: approle(), Runtime: RuntimeDeployed, MeshProtected: true})
	requireRefusal(t, err, "refusing cleartext http to a non-loopback Vault")
}

// A deployed runtime accepts exactly one binding: the Vault the composition
// names, reached with the AppRole credential it delivered as a secret. Each
// case asserts the text only its own guard produces, so removing that guard
// goes red rather than being covered by an overlapping refusal.
func TestDeployedRuntimeAcceptsOnlyAnAppRoleBinding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/login") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"minted","lease_duration":3600,"renewable":true}}`))
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("provisioned"), 0o400); err != nil {
		t.Fatal(err)
	}
	// The fixture server is loopback, so the mesh rule never masks these.
	approle := func() *AppRoleAuth { return &AppRoleAuth{RoleID: "r", SecretID: "s"} }

	for name, expect := range map[string]struct {
		config Config
		names  string
	}{
		"token auth": {
			Config{Address: server.URL, Token: "provisioned", Runtime: RuntimeDeployed},
			"VAULT_AUTH_METHOD=approle",
		},
		"static token beside the credential": {
			Config{Address: server.URL, AppRole: approle(), Token: "provisioned", Runtime: RuntimeDeployed},
			"refusing a provisioned Vault token outside the local environment",
		},
		"token file beside the credential": {
			Config{Address: server.URL, AppRole: approle(), TokenFile: tokenFile, Runtime: RuntimeDeployed},
			"refusing a provisioned Vault token outside the local environment",
		},
		"half a credential": {
			Config{Address: server.URL, AppRole: &AppRoleAuth{RoleID: "r"}, Runtime: RuntimeDeployed},
			"VAULT_APPROLE_SECRET_ID",
		},
		"unstated runtime": {
			Config{Address: server.URL, AppRole: approle()},
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
	if _, err := New(Config{Address: server.URL, AppRole: approle(), Runtime: RuntimeDeployed}); err != nil {
		t.Fatalf("the AppRole binding was refused on a deployed runtime: %v", err)
	}
}

// group and secretGroup set a key on the real hosted carriers the deployment
// uses, so these exercise Load's own branches rather than a convenience
// shortcut: the incident was a deployed process reading the wrong source, and
// only the real carrier proves which source Load actually consulted.
func group(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__VAULT__"+key, value)
}

func secretGroup(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__VAULT__"+key, value)
}

func hosted(t *testing.T) {
	t.Helper()
	t.Setenv("CODEFLY__ENVIRONMENT", "hosted")
	// Load falls back to a plain process variable when a group carries nothing,
	// so a stray value from the environment running the test would silently
	// satisfy the very branch under test.
	for _, key := range []string{
		"VAULT_ADDR", "VAULT_AUTH_METHOD", "VAULT_APPROLE_MOUNT",
		"VAULT_APPROLE_ROLE_ID", "VAULT_APPROLE_SECRET_ID",
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
		group(t, "VAULT_ADDR", "http://vault.vault.svc.cluster.local:8200")
		_, err := Load(context.Background())
		requireRefusal(t, err, "refusing a Vault token binding outside the local environment")
	})

	t.Run("token auth named outright", func(t *testing.T) {
		hosted(t)
		group(t, "VAULT_ADDR", "http://vault.vault.svc.cluster.local:8200")
		group(t, "VAULT_AUTH_METHOD", "token")
		_, err := Load(context.Background())
		requireRefusal(t, err, "refusing a Vault token binding outside the local environment")
	})

	t.Run("approle with no credential", func(t *testing.T) {
		hosted(t)
		group(t, "VAULT_ADDR", "http://vault.vault.svc.cluster.local:8200")
		group(t, "VAULT_AUTH_METHOD", "approle")
		_, err := Load(context.Background())
		requireRefusal(t, err, "VAULT_APPROLE_ROLE_ID")
	})

	t.Run("an unsupported auth method", func(t *testing.T) {
		hosted(t)
		group(t, "VAULT_ADDR", "http://vault.vault.svc.cluster.local:8200")
		group(t, "VAULT_AUTH_METHOD", "kubernetes")
		_, err := Load(context.Background())
		requireRefusal(t, err, `unsupported VAULT_AUTH_METHOD "kubernetes"`)
	})
}

// A misspelled assertion must not pass for an absent one nor for a present one,
// so it fails startup instead — and it does so before anything depends on it.
func TestLoadRefusesAMalformedMeshAssertion(t *testing.T) {
	hosted(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "yes")
	group(t, "VAULT_ADDR", "http://vault.vault.svc.cluster.local:8200")
	group(t, "VAULT_AUTH_METHOD", "approle")
	secretGroup(t, "VAULT_APPROLE_ROLE_ID", "r")
	secretGroup(t, "VAULT_APPROLE_SECRET_ID", "s")
	_, err := Load(context.Background())
	requireRefusal(t, err, "internal-transport/mesh-protected is \"yes\"")
}

// The credential arrives through the SECRET group, the way every other accounts
// secret does, and never as a file. This is the whole hosted binding end to
// end: values from one group, one secret from another, nothing mounted.
func TestLoadBuildsTheHostedBindingFromValuesAndOneSecret(t *testing.T) {
	var logins int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/login") {
			logins++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"minted","lease_duration":3600,"renewable":true}}`))
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()

	hosted(t)
	group(t, "VAULT_ADDR", server.URL) // loopback, so no assertion is needed
	group(t, "VAULT_AUTH_METHOD", "approle")
	secretGroup(t, "VAULT_APPROLE_ROLE_ID", "role-from-the-secret-group")
	secretGroup(t, "VAULT_APPROLE_SECRET_ID", "secret-from-the-secret-group")

	c, err := Load(context.Background())
	if err != nil {
		t.Fatalf("the hosted binding was refused: %v", err)
	}
	token, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token != "minted" || logins != 1 {
		t.Fatalf("token = %q after %d logins, want one AppRole login", token, logins)
	}
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
