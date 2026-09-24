package vaultconnection

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeVault serves the Kubernetes login and token renewal, recording what it
// was sent, and hands out numbered tokens so each login is distinguishable.
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
	ca     string
	jwt    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	vault := &fakeVault{lease: 3600, renewable: true}
	server := httptest.NewTLSServer(http.HandlerFunc(vault.handler))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o400); err != nil {
		t.Fatal(err)
	}
	jwt := filepath.Join(dir, "token")
	if err := os.WriteFile(jwt, []byte("projected-sa-jwt\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	return &harness{vault: vault, server: server, ca: ca, jwt: jwt}
}

func (h *harness) connect(t *testing.T, auth KubernetesAuth) (*Connection, *time.Time) {
	t.Helper()
	if auth.JWTPath == "" {
		auth.JWTPath = h.jwt
	}
	c, err := New(Config{Address: h.server.URL, CAFile: h.ca, Kubernetes: &auth, Token: "must-never-be-used", TokenFile: ""})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c.kubernetes.now = func() time.Time { return clock }
	return c, &clock
}

func TestKubernetesLoginPresentsTheProjectedTokenAndCachesTheResult(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(t, KubernetesAuth{Role: "accounts"})
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
	if got := h.vault.logins[0]; got["role"] != "accounts" || got["jwt"] != "projected-sa-jwt" {
		t.Fatalf("login body = %v", got)
	}
	if h.vault.loginPath != "/v1/auth/kubernetes/login" {
		t.Fatalf("login path = %q, want the default mount", h.vault.loginPath)
	}
}

func TestKubernetesLoginUsesTheConfiguredMount(t *testing.T) {
	h := newHarness(t)
	h.connect(t, KubernetesAuth{Role: "accounts", Mount: "/cells/example/"})
	if h.vault.loginPath != "/v1/auth/cells/example/login" {
		t.Fatalf("login path = %q", h.vault.loginPath)
	}
}

func TestKubernetesTokenIsRenewedBeforeItsLeaseRunsOut(t *testing.T) {
	h := newHarness(t)
	c, clock := h.connect(t, KubernetesAuth{Role: "accounts"})
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

func TestKubernetesLogsInAgainWhenTheLeaseHasEnded(t *testing.T) {
	h := newHarness(t)
	h.vault.renewable = false
	c, clock := h.connect(t, KubernetesAuth{Role: "accounts"})
	*clock = clock.Add(61 * time.Minute)
	token, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token != "login-token-2" || len(h.vault.renewals) != 0 {
		t.Fatalf("token = %q, renewals = %v", token, h.vault.renewals)
	}
}

func TestKubernetesLogsInAgainAfterARefusal(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(t, KubernetesAuth{Role: "accounts"})
	c.Invalidate()
	token, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token != "login-token-2" {
		t.Fatalf("token = %q, want a fresh login after Invalidate", token)
	}
}

func TestKubernetesLoginRefusalFailsClosedWithoutTheBody(t *testing.T) {
	h := newHarness(t)
	h.vault.refuse = true
	_, err := New(Config{Address: h.server.URL, CAFile: h.ca, Kubernetes: &KubernetesAuth{Role: "accounts", JWTPath: h.jwt}})
	if err == nil {
		t.Fatal("a refused login produced a connection")
	}
	if want := "Vault kubernetes login: Vault answered 403"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

func TestKubernetesAuthRequiresARoleTLSAndAPinnedCA(t *testing.T) {
	h := newHarness(t)
	for name, config := range map[string]Config{
		"no role":        {Address: h.server.URL, CAFile: h.ca, Kubernetes: &KubernetesAuth{JWTPath: h.jwt}},
		"no CA":          {Address: h.server.URL, Kubernetes: &KubernetesAuth{Role: "accounts", JWTPath: h.jwt}},
		"cleartext":      {Address: "http://vault.example.internal:8200", Kubernetes: &KubernetesAuth{Role: "accounts", JWTPath: h.jwt}, AllowInsecureHTTP: true},
		"escaping mount": {Address: h.server.URL, CAFile: h.ca, Kubernetes: &KubernetesAuth{Role: "accounts", Mount: "../sys", JWTPath: h.jwt}},
		"relative jwt":   {Address: h.server.URL, CAFile: h.ca, Kubernetes: &KubernetesAuth{Role: "accounts", JWTPath: "token"}},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKubernetesAuthNeverPresentsAStaticToken(t *testing.T) {
	h := newHarness(t)
	h.vault.refuse = true
	auth := KubernetesAuth{Role: "accounts", JWTPath: h.jwt}
	if _, err := New(Config{Address: h.server.URL, CAFile: h.ca, Kubernetes: &auth, Token: "root-token-must-not-be-used"}); err == nil {
		t.Fatal("fell back to the static token when the login was refused")
	}
}
