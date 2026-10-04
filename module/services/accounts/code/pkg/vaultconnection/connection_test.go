package vaultconnection

import (
	"context"
	"net"
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

// serviceFixture serves a Vault login at a fixture while the connection keeps
// the canonical Service address the admission rule is about. Rewriting the
// address to reach the fixture would test a different address from the one the
// contract describes, and under the rule below a loopback address is no longer
// admitted on a deployed runtime at all.
func serviceFixture(t *testing.T) (string, func(context.Context, string, string) (net.Conn, error), func() int) {
	t.Helper()
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
	t.Cleanup(server.Close)
	target := strings.TrimPrefix(server.URL, "http://")
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	return "http://vault.vault.svc.cluster.local:8200", dial, func() int { return logins }
}

// Plaintext is admitted by exactly two things, and which one applies depends on
// the runtime: loopback in a LOCAL run, and the mesh assertion over an
// in-cluster Service address anywhere.
//
// A deployed runtime gets no loopback exemption. A cell that named a loopback
// Vault would be reading its secrets from something inside its own pod — the
// in-memory store this binding exists to stop — and would reach it without the
// composition asserting anything, so that case is refused by name.
func TestPlaintextAdmissionDependsOnTheRuntime(t *testing.T) {
	approle := func() *AppRoleAuth { return &AppRoleAuth{RoleID: "r", SecretID: "s"} }

	// Local: loopback needs no assertion.
	for _, address := range []string{"http://localhost:8200", "http://127.0.0.1:8200", "http://[::1]:8200"} {
		if _, err := New(Config{Address: address, Token: "fixture", Runtime: RuntimeLocal}); err != nil {
			t.Fatalf("loopback %s refused on a local runtime: %v", address, err)
		}
	}

	// Deployed: loopback is not an exception to the admission rule.
	for _, address := range []string{"http://localhost:8200", "http://127.0.0.1:8200", "http://[::1]:8200"} {
		_, err := New(Config{Address: address, AppRole: approle(), Runtime: RuntimeDeployed})
		requireRefusal(t, err, "refusing cleartext http to a Vault")
		requireRefusal(t, err, "internal-transport/mesh-protected=true")
	}
	// Not even with the assertion: a mesh does not carry a hop that never
	// leaves the pod, so the assertion cannot cover it.
	for _, address := range []string{"http://localhost:8200", "http://127.0.0.1:8200"} {
		_, err := New(Config{Address: address, AppRole: approle(), Runtime: RuntimeDeployed, MeshProtected: true})
		requireRefusal(t, err, "refusing cleartext http to a Vault")
	}

	// Deployed, asserted, and an in-cluster Service address: admitted, and the
	// login actually completes over it.
	address, dial, logins := serviceFixture(t)
	c, err := New(Config{Address: address, AppRole: approle(), Runtime: RuntimeDeployed, MeshProtected: true, dial: dial})
	if err != nil {
		t.Fatalf("the in-mesh cell Vault was refused: %v", err)
	}
	if token, err := c.Token(); err != nil || token != "minted" || logins() != 1 {
		t.Fatalf("token = %q, err = %v after %d logins", token, err, logins())
	}
	if c.Address != "http://vault.vault.svc.cluster.local:8200" {
		t.Fatalf("the connection rewrote its address to %q", c.Address)
	}

	// The same address with no assertion stays refused.
	_, err = New(Config{Address: address, AppRole: approle(), Runtime: RuntimeDeployed, dial: dial})
	requireRefusal(t, err, "refusing cleartext http to a Vault")
}

// The mesh assertion admits only what a mesh can cover. An `svc` label inside
// somebody else's domain is not an in-cluster Service: `vault.vault.svc.example.com`
// resolves on the public internet, so admitting it would let the assertion
// authorize a destination the mesh demonstrably does not carry.
func TestTheMeshAssertionDoesNotAdmitAnExternalSvcName(t *testing.T) {
	_, dial, _ := serviceFixture(t)
	for _, address := range []string{
		"http://vault.vault.svc.example.com:8200",
		"http://a.b.c.svc.example.com:8200",
		"http://vault.vault.svc.cluster.example.com:8200",
		"http://vault.svc.example.com:8200",
		"http://a.b.c.d.svc:8200",
		"http://vault..svc:8200",
		"http://vault.example.com:8200",
		"http://10.0.0.5:8200",
		"http://vault:8200",
	} {
		_, err := New(Config{
			Address: address, AppRole: &AppRoleAuth{RoleID: "r", SecretID: "s"},
			Runtime: RuntimeDeployed, MeshProtected: true, dial: dial,
		})
		requireRefusal(t, err, "refusing cleartext http to a Vault")
	}
}

// A bare origin, or nothing. A path, query, fragment or userinfo would be
// dropped or carried into every request path the connection builds, so each is
// refused rather than normalized — and the refusal names the key that carries
// the value.
func TestTheAddressMustBeABareOrigin(t *testing.T) {
	for _, address := range []string{
		"not-a-url",
		"",
		"://missing-scheme",
		"http://",
		"ftp://vault.vault.svc:8200",
		"vault.vault.svc:8200",
		"http://user:pass@vault.vault.svc:8200",
		"http://vault.vault.svc:8200/v1",
		"http://vault.vault.svc:8200?token=x",
		"http://vault.vault.svc:8200#fragment",
		"http://vault.vault.svc:8200/v1/auth",
	} {
		_, err := New(Config{Address: address, Token: "fixture", Runtime: RuntimeLocal})
		requireRefusal(t, err, "invalid VAULT_ADDR")
	}
	// A trailing slash is the one tolerated spelling, since it names the same
	// origin and operators write it.
	if _, err := New(Config{Address: "http://127.0.0.1:8200/", Token: "fixture", Runtime: RuntimeLocal}); err != nil {
		t.Fatalf("a trailing slash was refused: %v", err)
	}
}

// No service-owned TLS configuration: transport security here is the mesh's, so
// there is no certificate to anchor, no pool to build and no client certificate
// to present. A config carried anyway would assert a posture this service does
// not implement.
func TestTheClientCarriesNoServiceOwnedTLSConfiguration(t *testing.T) {
	c, err := New(Config{Address: "http://127.0.0.1:8200", Token: "fixture", Runtime: RuntimeLocal})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := c.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.Client.Transport)
	}
	// What the cloned platform default carries is the platform's business, and
	// the review's remediation says so: system defaults serve any separately
	// supported https case. What must be absent is TLS policy this SERVICE
	// chose — a pinned root, a client certificate, a verification hook, a
	// skipped verification, or a protocol floor it invented.
	if tls := transport.TLSClientConfig; tls != nil {
		switch {
		case tls.RootCAs != nil:
			t.Error("the connection pins its own roots; there is no certificate for a service to anchor here")
		case len(tls.Certificates) != 0, tls.GetClientCertificate != nil:
			t.Error("the connection presents a client certificate; the credential is an AppRole secret")
		case tls.InsecureSkipVerify:
			t.Error("the connection skips verification")
		case tls.VerifyPeerCertificate != nil, tls.VerifyConnection != nil:
			t.Error("the connection carries its own verification hook")
		case tls.MinVersion != 0, tls.MaxVersion != 0:
			t.Error("the connection sets its own protocol floor rather than the platform's")
		}
	}
	if transport.Proxy != nil {
		t.Error("the connection honours a proxy, so the credential could be routed off the declared dependency")
	}
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
	// deployed runtime never connects". Every refusal above fires before the
	// address is admitted, which is why they can use the loopback fixture while
	// this one needs the canonical Service address.
	address, dial, _ := serviceFixture(t)
	if _, err := New(Config{
		Address: address, AppRole: approle(), Runtime: RuntimeDeployed,
		MeshProtected: true, dial: dial,
	}); err != nil {
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
	// Both carriers of every key this binding reads are blanked, not just one.
	//
	// Load falls back to a plain process variable when a group carries nothing,
	// so a stray value would silently satisfy the very branch under test — and
	// under `codefly ci run` the group carriers are not stray at all: the
	// runtime injects this module's own local defaults, including the
	// placeholder AppRole credential from vault.secret.env. That is how the
	// first version of this test passed locally and failed in CI: it blanked the
	// plain variables and inherited a real credential from the injected group.
	for _, key := range []string{
		"VAULT_ADDR", "VAULT_AUTH_METHOD", "VAULT_APPROLE_MOUNT",
		"VAULT_APPROLE_ROLE_ID", "VAULT_APPROLE_SECRET_ID", "VAULT_KEY_CUSTODY",
	} {
		t.Setenv(key, "")
		t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__VAULT__"+key, "")
		t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__VAULT__"+key, "")
	}
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "")
	t.Setenv("MESH_PROTECTED", "")
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
// end: values from one group, one secret from another, the mesh assertion from
// a third, nothing mounted.
//
// Load builds its own Config, so it cannot be handed the test dialer — and the
// canonical Service address does not resolve here. That is the point: the
// binding has to get past admission and fail at the login instead, which is
// what proves Load read all three groups and reached the AppRole exchange. The
// login completing over that address is held by
// TestPlaintextAdmissionDependsOnTheRuntime, which can inject the dialer.
func TestLoadBuildsTheHostedBindingFromValuesAndOneSecret(t *testing.T) {
	hosted(t)
	group(t, "VAULT_ADDR", "http://vault.vault.svc.cluster.local:8200")
	group(t, "VAULT_AUTH_METHOD", "approle")
	secretGroup(t, "VAULT_APPROLE_ROLE_ID", "role-from-the-secret-group")
	secretGroup(t, "VAULT_APPROLE_SECRET_ID", "secret-from-the-secret-group")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "true")

	_, err := Load(context.Background())
	if err == nil {
		t.Fatal("the canonical Service address resolved here, so this proves nothing")
	}
	// Admitted, then attempted: not refused on its address, and not refused for
	// a missing credential.
	if strings.Contains(err.Error(), "refusing cleartext") {
		t.Fatalf("the asserted in-cluster address was refused on its address: %v", err)
	}
	requireRefusal(t, err, "Vault approle login")

	// Without the assertion the same three groups are refused on the address,
	// so the assertion is load-bearing rather than incidental.
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "")
	_, err = Load(context.Background())
	requireRefusal(t, err, "refusing cleartext http to a Vault")
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

// No refusal, and no error on any path, may carry the credential's value.
//
// This is not hypothetical bookkeeping: a CI runner masks any log text equal to
// one of its secrets, so an error that echoed a role id or secret id would come
// back from CI with the useful part replaced by asterisks — the failure would
// become undiagnosable precisely when it mattered. And a Vault token or an
// AppRole secret in a test log is a leak whether or not anything masks it.
//
// Every refusal therefore names the KEY that is missing or wrong, never the
// value it found, and the login refusal is asserted by exact equality so
// Vault's own response body cannot widen it either.
func TestNoRefusalEverCarriesTheCredentialValue(t *testing.T) {
	const (
		roleID   = "role-id-that-must-never-be-logged"
		secretID = "secret-id-that-must-never-be-logged"
		token    = "vault-token-that-must-never-be-logged"
	)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		// Vault echoing the credential back would be unusual, but an error that
		// passed a response body through would leak whatever it contained.
		_, _ = w.Write([]byte(`{"errors":["permission denied for ` + secretID + `"]}`))
	}))
	defer refusing.Close()

	full := func() *AppRoleAuth { return &AppRoleAuth{RoleID: roleID, SecretID: secretID} }
	for name, config := range map[string]Config{
		"refused login":                       {Address: refusing.URL, AppRole: full(), Runtime: RuntimeLocal},
		"half a credential":                   {Address: refusing.URL, AppRole: &AppRoleAuth{RoleID: roleID}, Runtime: RuntimeLocal},
		"static token beside the credential":  {Address: refusing.URL, AppRole: full(), Token: token, Runtime: RuntimeDeployed},
		"token binding on a deployed runtime": {Address: refusing.URL, Token: token, Runtime: RuntimeDeployed},
		"cleartext off loopback":              {Address: "http://vault.example.com:8200", AppRole: full(), Runtime: RuntimeDeployed},
		"invalid mount":                       {Address: refusing.URL, AppRole: &AppRoleAuth{RoleID: roleID, SecretID: secretID, Mount: "../sys"}, Runtime: RuntimeLocal},
		"unstated runtime":                    {Address: refusing.URL, AppRole: full()},
	} {
		_, err := New(config)
		if err == nil {
			t.Errorf("%s: accepted, so this case proves nothing", name)
			continue
		}
		for value, what := range map[string]string{
			roleID:   "role id",
			secretID: "secret id",
			token:    "Vault token",
		} {
			if strings.Contains(err.Error(), value) {
				t.Errorf("%s: the refusal carries the %s", name, what)
			}
		}
	}
}
