package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newModuleUpstream starts a fake upstream and returns it with its (loopback)
// URL, which is a valid composition-local host for registration.
func newModuleUpstream(t *testing.T) (*fakeUpstream, string) {
	t.Helper()
	fake := &fakeUpstream{body: "module-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv.URL
}

// registerModule POSTs /modules/_register with the cluster-internal token and
// returns the response recorder.
func registerModule(t *testing.T, gw *Gateway, prefix, upstream string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"prefix":%q,"upstream":%q}`, prefix, upstream)
	req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	return w
}

// ============================================================================
// End-to-end: a composed module federates its REST surface through the gateway
// with no gateway redeploy — and is still fully authenticated.
// ============================================================================

// After a module registers, /v1/<module>/* is proxied to it with the caller's
// identity projected exactly as a protected catalog route, and the module owns
// its full /v1/<module> path.
func TestGateway_Module_Federated_ValidJWT_Proxied(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	moduleFake, upstream := newModuleUpstream(t)

	regResp := registerModule(t, gw, "documents", upstream)
	require.Equal(t, http.StatusOK, regResp.Code)

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "module-response", w.Body.String())
	// The module owns and serves its own /v1/<module> surface — the path is
	// forwarded unchanged.
	require.Equal(t, "/v1/documents/collection", moduleFake.lastPath)
	// Sidecar-stamped canonical identity reaches the module.
	require.NotEmpty(t, moduleFake.lastHeaders.Get("x-user-id"))
	require.NotEmpty(t, moduleFake.lastHeaders.Get("x-org-id"))
	// A federated upstream is NOT accounts, so the gateway credential is never
	// stamped for it.
	require.Empty(t, moduleFake.lastHeaders.Get("x-codefly-gateway-token"))
	// Caller-supplied identity headers are stripped before projection.
	require.Empty(t, moduleFake.lastHeaders.Get("x-codefly-internal-token"))
}

// A federated route consumes the same per-org rate-limit budget as an
// equivalent catalog route: it must NOT be an unmetered proxy. With a tiny
// budget, a burst of authenticated requests to /v1/<module>/* eventually 429s,
// exactly as a catalog route would. (Regression guard: the pre-fix code proxied
// federated routes directly, bypassing the limiter, so this never 429'd.)
func TestGateway_Module_Federated_RateLimited(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	// effective budget = limit(1) + burst(max(1/5,1)=1) = 2 requests / org / min.
	gw.rateLimiter = NewRateLimiter(1)
	moduleFake, upstream := newModuleUpstream(t)
	require.Equal(t, http.StatusOK, registerModule(t, gw, "documents", upstream).Code)

	// Reuse ONE token so every request keys on the same injected x-org-id.
	token := signValidToken(t, priv)
	got429 := false
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.True(t, got429, "federated module route must be subject to the rate-limit budget")
	// The limiter rejects at the gateway; a throttled request never reaches the
	// module upstream on that pass, but the earlier allowed ones did.
	require.NotNil(t, moduleFake.lastHeaders)
}

// Security invariant: registration adds a proxy target, never an auth bypass. A
// bearer-less call to a registered module prefix is denied at the gateway (401)
// and the module upstream is never reached.
func TestGateway_Module_Federated_NoToken_Denied(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	moduleFake, upstream := newModuleUpstream(t)
	require.Equal(t, http.StatusOK, registerModule(t, gw, "documents", upstream).Code)

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, moduleFake.lastHeaders, "module upstream must NEVER be reached without auth")
}

// An unregistered /v1/<module>/* prefix is indistinguishable from any other
// unexposed path: 404, no federation.
func TestGateway_Module_Unregistered_NotFound(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/ghost/thing", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "not exposed")
}

// ============================================================================
// Guardrail 1 — authenticated: registration requires the cluster-internal token.
// ============================================================================

func TestGateway_ModuleRegister_RequiresInternalToken(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	moduleFake, upstream := newModuleUpstream(t)
	body := fmt.Sprintf(`{"prefix":"documents","upstream":%q}`, upstream)

	// No token.
	noTok := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, noTok)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// Wrong token.
	badTok := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
	badTok.Header.Set("X-Codefly-Internal-Token", "not-the-token")
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, badTok)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// Nothing was registered: the prefix stays unrouted (404 even with a valid
	// bearer), and the (would-be) upstream is never reached.
	_, ok := gw.modules.get("documents")
	require.False(t, ok, "a rejected registration must not store the prefix")

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Nil(t, moduleFake.lastHeaders, "unregistered upstream must never be reached")
}

// ============================================================================
// Guardrail 2 — identity-bound (best-effort under a shared token):
// well-formed single-segment prefix + first-claim-wins.
// ============================================================================

func TestGateway_ModuleRegister_RejectsMalformedPrefix(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	for _, prefix := range []string{
		"",                      // empty
		"docs/collection",       // a path, not a single segment
		"../secret",             // traversal
		"Documents",             // uppercase (not a valid identity)
		"docs_underscore",       // underscore not in identity charset
		strings.Repeat("a", 64), // longer than 63
	} {
		body := fmt.Sprintf(`{"prefix":%q,"upstream":%q}`, prefix, upstream)
		req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
		req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equalf(t, http.StatusBadRequest, w.Code, "prefix %q must be rejected", prefix)
	}
}

// A prefix already held by a different upstream cannot be taken over; the same
// upstream re-registering is idempotent.
func TestGateway_ModuleRegister_FirstClaimWins(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	_, upstreamA := newModuleUpstream(t)
	_, upstreamB := newModuleUpstream(t)

	require.Equal(t, http.StatusOK, registerModule(t, gw, "documents", upstreamA).Code)

	// A different upstream trying to claim the same prefix is rejected.
	conflict := registerModule(t, gw, "documents", upstreamB)
	require.Equal(t, http.StatusConflict, conflict.Code)

	// The original registration still stands.
	stored, ok := gw.modules.get("documents")
	require.True(t, ok)
	require.Equal(t, mustHost(t, upstreamA), stored.Host)

	// Idempotent re-registration of the same upstream succeeds.
	require.Equal(t, http.StatusOK, registerModule(t, gw, "documents", upstreamA).Code)
}

// ============================================================================
// Guardrail 3 — catalog-protected: a registration can never shadow the catalog.
// ============================================================================

// Registering a prefix the catalog already owns is rejected loudly.
func TestGateway_ModuleRegister_RejectsCatalogPrefix(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	// The test catalog owns /v1/users, /v1/auth, /v1/mfa, /v1/billing, ...
	for _, prefix := range []string{"users", "auth", "billing", "mfa"} {
		w := registerModule(t, gw, prefix, upstream)
		require.Equalf(t, http.StatusConflict, w.Code, "catalog prefix %q must be reserved", prefix)
	}
}

// Even if a module is somehow registered under a catalog-owned prefix (bypassing
// the registration guard), the catalog still wins: the request routes to the
// catalog upstream, never the module. This is the structural invariant —
// federation is attempted ONLY after the matcher misses.
func TestGateway_Module_CatalogAlwaysWins(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)
	moduleFake, upstream := newModuleUpstream(t)

	// Force a shadowing registration directly into the registry.
	u, err := url.Parse(upstream)
	require.NoError(t, err)
	gw.modules.set("users", u)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "api-response", w.Body.String(), "catalog upstream must serve /v1/users")
	require.NotNil(t, apiFake.lastHeaders, "catalog (accounts) upstream must be reached")
	require.Nil(t, moduleFake.lastHeaders, "shadowing module upstream must NEVER be reached")
}

// ============================================================================
// Guardrail 4 — upstream constrained: composition-local (mesh) only.
// ============================================================================

func TestGateway_ModuleRegister_RejectsNonMeshUpstream(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	for _, upstream := range []string{
		"http://8.8.8.8",                  // public IP
		"http://93.184.216.34:8080",       // public IP with port
		"https://api.example.com",         // external FQDN
		"http://169.254.169.254/",         // cloud metadata (link-local)
		"http://metadata.google.internal", // metadata hostname
		"ftp://internal",                  // wrong scheme
		"http://",                         // no host
	} {
		w := registerModule(t, gw, "documents", upstream)
		require.GreaterOrEqualf(t, w.Code, 400, "upstream %q must be rejected", upstream)
		require.Lessf(t, w.Code, 500, "upstream %q rejection is a client error", upstream)
	}
}

// isDisallowedModuleUpstreamHost draws the mesh-local line: loopback, private
// ranges, bare service names, and cluster suffixes are allowed; public IPs,
// external FQDNs, and SSRF sinks are not.
func TestIsDisallowedModuleUpstreamHost(t *testing.T) {
	allowed := []string{
		"127.0.0.1", "::1", "localhost",
		"10.1.2.3", "172.16.0.5", "192.168.1.1", // RFC1918
		"fd00::1",                       // ULA
		"accounts",                      // bare mesh service name
		"documents.svc", "api.internal", // cluster suffixes
		"pod.namespace.svc.cluster.local",
	}
	for _, h := range allowed {
		require.Falsef(t, isDisallowedModuleUpstreamHost(h), "%q should be allowed (mesh-local)", h)
	}

	disallowed := []string{
		"", "8.8.8.8", "93.184.216.34",
		"api.example.com", "evil.co.uk",
		"169.254.169.254",          // link-local / metadata IP
		"metadata.google.internal", // metadata hostname
		"0.0.0.0",                  // unspecified
		"evil.localhost",           // multi-label *.localhost is NOT loopback: real DNS lookup
	}
	for _, h := range disallowed {
		require.Truef(t, isDisallowedModuleUpstreamHost(h), "%q should be rejected", h)
	}
}

// ============================================================================
// Register endpoint hygiene.
// ============================================================================

func TestGateway_ModuleRegister_MethodAndBody(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	// GET is not allowed.
	getReq := httptest.NewRequest(http.MethodGet, moduleRegisterPath, nil)
	getReq.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, getReq)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)

	// Invalid JSON.
	badJSON := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(`{not json`))
	badJSON.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, badJSON)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Host
}
