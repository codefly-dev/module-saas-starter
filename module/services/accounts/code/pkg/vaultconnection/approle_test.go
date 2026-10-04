package vaultconnection

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeVault serves the AppRole login and token renewal, recording what it was
// sent, and hands out numbered tokens so each login is distinguishable.
type fakeVault struct {
	mu        sync.Mutex
	logins    []map[string]string
	loginPath string
	renewals  []string
	issued    int
	lease     int64
	renewable bool
	refuse    bool
}

func (f *fakeVault) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/auth/token/renew-self":
		f.renewals = append(f.renewals, r.Header.Get("X-Vault-Token"))
		f.reply(w, r.Header.Get("X-Vault-Token"))
	case r.Method == http.MethodPost && filepath.Base(r.URL.Path) == "login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.logins = append(f.logins, body)
		f.loginPath = r.URL.Path
		if f.refuse {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		f.issued++
		f.reply(w, fmt.Sprintf("login-token-%d", f.issued))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeVault) reply(w http.ResponseWriter, token string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{
		"client_token": token, "lease_duration": f.lease, "renewable": f.renewable,
	}})
}

type harness struct {
	vault  *fakeVault
	server *httptest.Server
}

// The server is plain http on loopback, which is exactly the local shape: there
// is no CA to pin anywhere in this binding any more, so a TLS fixture would be
// testing a transport the contract no longer has.
func newHarness(t *testing.T) *harness {
	t.Helper()
	vault := &fakeVault{lease: 3600, renewable: true}
	server := httptest.NewServer(http.HandlerFunc(vault.handler))
	t.Cleanup(server.Close)
	return &harness{vault: vault, server: server}
}

func (h *harness) connect(t *testing.T, auth AppRoleAuth) (*Connection, *time.Time) {
	t.Helper()
	if auth.RoleID == "" {
		auth.RoleID = "role-fixture"
	}
	if auth.SecretID == "" {
		auth.SecretID = "secret-fixture"
	}
	c, err := New(Config{Address: h.server.URL, AppRole: &auth, Token: "must-never-be-used", Runtime: RuntimeLocal})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c.appRole.now = func() time.Time { return clock }
	return c, &clock
}

func TestAppRoleLoginPresentsTheCredentialAndCachesTheResult(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(t, AppRoleAuth{})
	for range 3 {
		token, err := c.Token()
		if err != nil {
			t.Fatal(err)
		}
		if token != "login-token-1" {
			t.Fatalf("token = %q, want the one login's token", token)
		}
	}
	if len(h.vault.logins) != 1 {
		t.Fatalf("logins = %d, want 1 (the token is cached)", len(h.vault.logins))
	}
	if got := h.vault.logins[0]; got["role_id"] != "role-fixture" || got["secret_id"] != "secret-fixture" {
		t.Fatalf("login body = %v", got)
	}
	if h.vault.loginPath != "/v1/auth/approle/login" {
		t.Fatalf("login path = %q, want the default mount", h.vault.loginPath)
	}
}

func TestAppRoleLoginUsesTheConfiguredMount(t *testing.T) {
	h := newHarness(t)
	h.connect(t, AppRoleAuth{Mount: "/cells/example/"})
	if h.vault.loginPath != "/v1/auth/cells/example/login" {
		t.Fatalf("login path = %q", h.vault.loginPath)
	}
}

func TestAppRoleTokenIsRenewedBeforeItsLeaseRunsOut(t *testing.T) {
	h := newHarness(t)
	c, clock := h.connect(t, AppRoleAuth{})
	*clock = clock.Add(50 * time.Minute) // under a third of the hour left
	token, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if len(h.vault.renewals) != 1 || h.vault.renewals[0] != "login-token-1" || token != "login-token-1" {
		t.Fatalf("renewals = %v, token = %q", h.vault.renewals, token)
	}
	if len(h.vault.logins) != 1 {
		t.Fatalf("a renewable token was replaced by a new login")
	}
}

func TestAppRoleLogsInAgainWhenTheLeaseHasEnded(t *testing.T) {
	h := newHarness(t)
	h.vault.renewable = false
	c, clock := h.connect(t, AppRoleAuth{})
	*clock = clock.Add(61 * time.Minute)
	token, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token != "login-token-2" || len(h.vault.renewals) != 0 {
		t.Fatalf("token = %q, renewals = %v", token, h.vault.renewals)
	}
}

func TestAppRoleLogsInAgainAfterARefusal(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(t, AppRoleAuth{})
	c.Invalidate()
	token, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token != "login-token-2" {
		t.Fatalf("token = %q, want a fresh login after Invalidate", token)
	}
}

// The login request body carries the credential, so a refusal is diagnosed by
// its status and Vault's own body is never echoed into the error.
func TestAppRoleLoginRefusalFailsClosedWithoutTheBody(t *testing.T) {
	h := newHarness(t)
	h.vault.refuse = true
	_, err := New(Config{
		Address: h.server.URL, Runtime: RuntimeLocal,
		AppRole: &AppRoleAuth{RoleID: "role-fixture", SecretID: "secret-fixture"},
	})
	if err == nil {
		t.Fatal("a refused login produced a connection")
	}
	// Exact, not a substring: Vault's own body carries "permission denied" and
	// must not reach the error, because a login body carries the credential.
	if want := "Vault approle login: Vault answered 403"; err.Error() != want {
		t.Fatalf("err = %q, want exactly %q", err, want)
	}
}

func TestAppRoleAuthRequiresBothHalvesOfTheCredential(t *testing.T) {
	h := newHarness(t)
	// Every case states RuntimeLocal so each is refused for the reason it names
	// rather than for an unstated runtime, which would make the whole table pass
	// while proving nothing.
	full := func() *AppRoleAuth { return &AppRoleAuth{RoleID: "role-fixture", SecretID: "secret-fixture"} }
	for name, expect := range map[string]struct {
		config Config
		names  string
	}{
		"no role id": {
			Config{Address: h.server.URL, AppRole: &AppRoleAuth{SecretID: "secret-fixture"}, Runtime: RuntimeLocal},
			"VAULT_APPROLE_ROLE_ID",
		},
		"no secret id": {
			Config{Address: h.server.URL, AppRole: &AppRoleAuth{RoleID: "role-fixture"}, Runtime: RuntimeLocal},
			"VAULT_APPROLE_SECRET_ID",
		},
		"escaping mount": {
			Config{Address: h.server.URL, AppRole: &AppRoleAuth{RoleID: "r", SecretID: "s", Mount: "../sys"}, Runtime: RuntimeLocal},
			"invalid VAULT_APPROLE_MOUNT",
		},
		"unstated runtime": {
			Config{Address: h.server.URL, AppRole: full()},
			"stated runtime",
		},
	} {
		_, err := New(expect.config)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), expect.names) {
			t.Errorf("%s: refusal does not name %q: %v", name, expect.names, err)
		}
	}
}

func TestAppRoleAuthNeverPresentsAStaticToken(t *testing.T) {
	h := newHarness(t)
	h.vault.refuse = true
	auth := AppRoleAuth{RoleID: "role-fixture", SecretID: "secret-fixture"}
	if _, err := New(Config{
		Address: h.server.URL, AppRole: &auth,
		Token: "root-token-must-not-be-used", Runtime: RuntimeLocal,
	}); err == nil {
		t.Fatal("fell back to the static token when the login was refused")
	}
}
