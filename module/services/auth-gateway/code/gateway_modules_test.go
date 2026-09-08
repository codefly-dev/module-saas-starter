package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

// signModuleRegistrationToken mints a per-module registration token binding the
// module identity to a single prefix, signed with the same Ed25519 key the
// harness configures as the sidecar's public key.
func signModuleRegistrationToken(t *testing.T, priv ed25519.PrivateKey, prefix string) string {
	t.Helper()
	c := moduleRegistrationClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   "module:" + prefix,
			Audience:  jwt.ClaimStrings{moduleRegistrationAudience},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
		Prefix: prefix,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c).SignedString(priv)
	require.NoError(t, err)
	return signed
}

// registerModule POSTs /modules/_register with a per-module registration token
// bound to prefix, and returns the response recorder.
func registerModule(t *testing.T, gw *Gateway, priv ed25519.PrivateKey, prefix, upstream string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"prefix":%q,"upstream":%q}`, prefix, upstream)
	req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
	req.Header.Set(moduleRegistrationHeader, signModuleRegistrationToken(t, priv, prefix))
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

	regResp := registerModule(t, gw, priv, "documents", upstream)
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
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", upstream).Code)

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
	gw, _, _, priv := newGatewayHarness(t)
	moduleFake, upstream := newModuleUpstream(t)
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", upstream).Code)

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
// Guardrail 1 — per-module cryptographic identity: registration requires a
// valid signed registration token, and that token binds the caller to exactly
// one prefix.
// ============================================================================

// Registration is gated on the signed per-module token, NOT the shared
// cluster-internal secret: a missing token, a malformed token, and a token
// signed for the wrong purpose (a user access token) are all rejected, and
// nothing is registered.
func TestGateway_ModuleRegister_RequiresRegistrationToken(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	moduleFake, upstream := newModuleUpstream(t)
	body := fmt.Sprintf(`{"prefix":"documents","upstream":%q}`, upstream)

	post := func(setHeader func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
		if setHeader != nil {
			setHeader(req)
		}
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w.Code
	}

	// No token.
	require.Equal(t, http.StatusUnauthorized, post(nil))
	// Garbage token.
	require.Equal(t, http.StatusUnauthorized, post(func(r *http.Request) {
		r.Header.Set(moduleRegistrationHeader, "not-a-jwt")
	}))
	// The old shared cluster-internal token is no longer accepted here.
	require.Equal(t, http.StatusUnauthorized, post(func(r *http.Request) {
		r.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	}))
	// A valid USER access token (wrong audience) cannot double as a registration
	// credential — audience-locking keeps the two token types non-interchangeable.
	require.Equal(t, http.StatusUnauthorized, post(func(r *http.Request) {
		r.Header.Set(moduleRegistrationHeader, signValidToken(t, priv))
	}))

	// Nothing was registered: the prefix stays unrouted (404 even with a valid
	// bearer), and the (would-be) upstream is never reached.
	_, ok := gw.modules.get("documents")
	require.False(t, ok, "a rejected registration must not store the prefix")

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Nil(t, moduleFake.lastHeaders, "unregistered upstream must never be reached")
}

// A token signed by the wrong key is rejected: verification is a real signature
// check, not merely a well-formedness check.
func TestGateway_ModuleRegister_RejectsForeignKey(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	_, foreignPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	w := registerModule(t, gw, foreignPriv, "documents", upstream)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	_, ok := gw.modules.get("documents")
	require.False(t, ok)
}

// An expired registration token is rejected (fail closed on expiry).
func TestGateway_ModuleRegister_RejectsExpiredToken(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	c := moduleRegistrationClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   "module:documents",
			Audience:  jwt.ClaimStrings{moduleRegistrationAudience},
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
		Prefix: "documents",
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c).SignedString(priv)
	require.NoError(t, err)

	body := `{"prefix":"documents","upstream":"` + upstream + `"}`
	req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
	req.Header.Set(moduleRegistrationHeader, expired)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// The token binds ONE prefix to the module identity: a caller holding a valid
// token for "documents" cannot register any other prefix, even a well-formed,
// non-catalog one. This is the per-caller binding the shared secret lacked.
func TestGateway_ModuleRegister_EnforcesPrefixBinding(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	// Token authorizes "documents"; payload tries to claim "reports".
	body := fmt.Sprintf(`{"prefix":"reports","upstream":%q}`, upstream)
	req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
	req.Header.Set(moduleRegistrationHeader, signModuleRegistrationToken(t, priv, "documents"))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	_, ok := gw.modules.get("reports")
	require.False(t, ok, "a prefix the token does not authorize must not be registered")

	// The identity's own prefix still registers fine.
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", upstream).Code)
}

// ============================================================================
// Guardrail 2 — well-formed single-segment prefix + first-claim-wins.
// ============================================================================

func TestGateway_ModuleRegister_RejectsMalformedPrefix(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	for _, prefix := range []string{
		"",                      // empty
		"docs/collection",       // a path, not a single segment
		"../secret",             // traversal
		"Documents",             // uppercase (not a valid identity)
		"docs_underscore",       // underscore not in identity charset
		strings.Repeat("a", 64), // longer than 63
	} {
		// A valid token (bound to some prefix) gets past Guardrail 1; the malformed
		// PAYLOAD prefix is rejected by the well-formedness check that runs before
		// the binding compare.
		body := fmt.Sprintf(`{"prefix":%q,"upstream":%q}`, prefix, upstream)
		req := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(body))
		req.Header.Set(moduleRegistrationHeader, signModuleRegistrationToken(t, priv, "placeholder"))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equalf(t, http.StatusBadRequest, w.Code, "prefix %q must be rejected", prefix)
	}
}

// A prefix already held by a different upstream cannot be taken over; the same
// upstream re-registering is idempotent.
func TestGateway_ModuleRegister_FirstClaimWins(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	_, upstreamA := newModuleUpstream(t)
	_, upstreamB := newModuleUpstream(t)

	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", upstreamA).Code)

	// A different upstream trying to claim the same prefix is rejected.
	conflict := registerModule(t, gw, priv, "documents", upstreamB)
	require.Equal(t, http.StatusConflict, conflict.Code)

	// The original registration still stands.
	stored, ok := gw.modules.get("documents")
	require.True(t, ok)
	require.Equal(t, mustHost(t, upstreamA), stored.Host)

	// Idempotent re-registration of the same upstream succeeds.
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", upstreamA).Code)
}

// ============================================================================
// Guardrail 3 — catalog-protected: a registration can never shadow the catalog.
// ============================================================================

// Registering a prefix the catalog already owns is rejected loudly. (The token
// is minted per-prefix directly in the test, bypassing the real minting
// authority, so the catalog guard is exercised in isolation.)
func TestGateway_ModuleRegister_RejectsCatalogPrefix(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	_, upstream := newModuleUpstream(t)

	// The test catalog owns /v1/users, /v1/auth, /v1/mfa, /v1/billing, ...
	for _, prefix := range []string{"users", "auth", "billing", "mfa"} {
		w := registerModule(t, gw, priv, prefix, upstream)
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
	gw, _, _, priv := newGatewayHarness(t)

	for _, upstream := range []string{
		"http://8.8.8.8",                  // public IP
		"http://93.184.216.34:8080",       // public IP with port
		"https://api.example.com",         // external FQDN
		"http://169.254.169.254/",         // cloud metadata (link-local)
		"http://metadata.google.internal", // metadata hostname
		"ftp://internal",                  // wrong scheme
		"http://",                         // no host
	} {
		w := registerModule(t, gw, priv, "documents", upstream)
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
// Resolve-time SSRF / DNS-rebinding defense.
// ============================================================================

// isAllowedResolvedModuleIP is the resolve-time counterpart to the host-string
// guard: only loopback and private (RFC1918/ULA) addresses are mesh-local.
func TestIsAllowedResolvedModuleIP(t *testing.T) {
	allowed := []string{
		"127.0.0.1", "127.0.0.53", "::1", // loopback
		"10.1.2.3", "172.16.0.5", "192.168.1.1", "fd00::1", // private / ULA
	}
	for _, s := range allowed {
		require.Truef(t, isAllowedResolvedModuleIP(net.ParseIP(s)), "%q should be mesh-local", s)
	}
	disallowed := []string{
		"8.8.8.8", "93.184.216.34", "1.1.1.1", // public
		"169.254.169.254", "fe80::1", // link-local (metadata)
		"0.0.0.0", "::", // unspecified
	}
	for _, s := range disallowed {
		require.Falsef(t, isAllowedResolvedModuleIP(net.ParseIP(s)), "%q should be rejected", s)
	}
	require.False(t, isAllowedResolvedModuleIP(nil))
}

// validateResolvedModuleAddrs fails closed on an empty result and rejects the
// WHOLE set if any single address is off-mesh (no retry to a rebinding-mixed
// forbidden IP).
func TestValidateResolvedModuleAddrs(t *testing.T) {
	require.Error(t, validateResolvedModuleAddrs(nil))
	require.NoError(t, validateResolvedModuleAddrs([]net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("10.0.0.1")},
	}))
	// A benign address mixed with a forbidden one rejects the entire dial.
	require.Error(t, validateResolvedModuleAddrs([]net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("8.8.8.8")},
	}))
}

// The module transport MUST NOT honor an environment proxy. It is built by
// cloning http.DefaultTransport, which carries Proxy: ProxyFromEnvironment; left
// intact, a configured HTTP(S)_PROXY would make the transport dial the proxy, so
// the resolve-and-pin DialContext would validate the proxy's address instead of
// the upstream's and tunnel in-mesh traffic off-mesh. Proxy must be disabled so
// the validated direct dial is the only path to the upstream.
func TestModuleUpstreamTransport_DisablesProxy(t *testing.T) {
	// Baseline: the clone source really does carry a proxy, so a nil Proxy on the
	// module transport is a deliberate override, not a coincidental default.
	require.NotNil(t, http.DefaultTransport.(*http.Transport).Clone().Proxy,
		"precondition: DefaultTransport clone carries ProxyFromEnvironment")

	tr := newModuleUpstreamTransport(stubResolver{})
	require.Nil(t, tr.Proxy, "module transport must not route module upstreams through an env proxy")
}

// stubResolver maps a host to a fixed set of addresses, standing in for DNS so a
// rebinding scenario is deterministic in a unit test.
type stubResolver map[string][]net.IP

func (s stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := s[host]
	if !ok {
		return nil, fmt.Errorf("no stub for %s", host)
	}
	addrs := make([]net.IPAddr, len(ips))
	for i, ip := range ips {
		addrs[i] = net.IPAddr{IP: ip}
	}
	return addrs, nil
}

func mustPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Port()
}

// A mesh hostname that passes the register-time string guard but, at PROXY time,
// resolves to a public IP (DNS rebinding) must have its dial refused: the caller
// gets a 502 and the address is never connected to. The same name resolving to
// loopback still proxies, so a legitimate mesh upstream is unaffected.
func TestGateway_Module_ResolveTimeRebindingBlocked(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	moduleFake, realURL := newModuleUpstream(t) // http://127.0.0.1:PORT
	port := mustPort(t, realURL)

	// Register a mesh-looking host (documents.svc passes isDisallowedModuleUpstreamHost).
	meshUpstream := "http://documents.svc:" + port
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", meshUpstream).Code)

	proxy := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/documents/x", nil)
		req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w
	}

	// Rebinding: the mesh name resolves to a public IP at dial time -> refused.
	gw.moduleTransport = newModuleUpstreamTransport(stubResolver{
		"documents.svc": {net.ParseIP("8.8.8.8")},
	})
	w := proxy()
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "forbidden module upstream address")
	require.Nil(t, moduleFake.lastHeaders, "a rebound public address must never be dialed")

	// The same name resolving to loopback still proxies (mesh dev / tests).
	gw.moduleTransport = newModuleUpstreamTransport(stubResolver{
		"documents.svc": {net.ParseIP("127.0.0.1")},
	})
	w = proxy()
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "module-response", w.Body.String())
	require.NotNil(t, moduleFake.lastHeaders, "a loopback-resolved mesh upstream must be reached")
}

// ============================================================================
// Register endpoint hygiene.
// ============================================================================

func TestGateway_ModuleRegister_MethodAndBody(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	// GET is not allowed (method is checked before auth).
	getReq := httptest.NewRequest(http.MethodGet, moduleRegisterPath, nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, getReq)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)

	// Invalid JSON, with a valid token so it reaches the decode step.
	badJSON := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(`{not json`))
	badJSON.Header.Set(moduleRegistrationHeader, signModuleRegistrationToken(t, priv, "documents"))
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

// ============================================================================
// Registration-credential exchange (/modules/_registration-token)
// ============================================================================

// fakeAccountsMint stands in for accounts' credential exchange: it records what
// the gateway forwarded and signs a registration token with the same key the
// harness publishes as the sidecar's public key, exactly as accounts does.
type fakeAccountsMint struct {
	priv         ed25519.PrivateKey
	status       int
	lastPath     string
	lastInternal string
	lastPrefix   string
	lastSecret   string
	requestCount int
}

func (f *fakeAccountsMint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requestCount++
	f.lastPath = r.URL.Path
	f.lastInternal = r.Header.Get("X-Codefly-Internal-Token")
	var payload struct {
		Prefix string `json:"prefix"`
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	f.lastPrefix = payload.Prefix
	f.lastSecret = payload.Secret

	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		return
	}
	claims := moduleRegistrationClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   "module:" + payload.Prefix,
			Audience:  jwt.ClaimStrings{moduleRegistrationAudience},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
		Prefix: payload.Prefix,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(f.priv)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"token":%q,"expiresAt":"2030-01-01T00:00:00Z"}`, signed)))
}

// newExchangeHarness points the gateway's accounts upstream at a fake mint.
func newExchangeHarness(t *testing.T) (*Gateway, *fakeAccountsMint, ed25519.PrivateKey) {
	t.Helper()
	gw, _, _, priv := newGatewayHarness(t)
	mint := &fakeAccountsMint{priv: priv}
	srv := httptest.NewServer(mint)
	t.Cleanup(srv.Close)
	accountsURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	gw.upstreams["accounts"] = accountsURL
	return gw, mint, priv
}

func exchangeTokenRequest(prefix, secret, internal string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, moduleRegistrationTokenPath,
		strings.NewReader(fmt.Sprintf(`{"prefix":%q}`, prefix)))
	if secret != "" {
		req.Header.Set(moduleSecretHeader, secret)
	}
	if internal != "" {
		req.Header.Set("X-Codefly-Internal-Token", internal)
	}
	return req
}

func TestGateway_ModuleRegistrationToken_BrokersToAccounts(t *testing.T) {
	gw, mint, _ := newExchangeHarness(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, exchangeTokenRequest("documents", "documents-secret", "test-internal-token"))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("cache-control"))
	// The gateway presents its OWN cluster credential on the internal leg and
	// forwards the module's secret for accounts to judge.
	require.Equal(t, accountsModuleRegistrationPath, mint.lastPath)
	require.Equal(t, "test-internal-token", mint.lastInternal)
	require.Equal(t, "documents", mint.lastPrefix)
	require.Equal(t, "documents-secret", mint.lastSecret)

	var payload struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Token)
}

// The exchange feeds the endpoint it exists for: a module that runs the full
// handshake ends up with a routed, still-authenticated federated prefix.
func TestGateway_ModuleRegistrationToken_CompletesHandshake(t *testing.T) {
	gw, _, priv := newExchangeHarness(t)
	moduleFake, upstream := newModuleUpstream(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, exchangeTokenRequest("documents", "documents-secret", "test-internal-token"))
	require.Equal(t, http.StatusOK, w.Code)
	var issued struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &issued))

	regBody := fmt.Sprintf(`{"prefix":"documents","upstream":%q}`, upstream)
	regReq := httptest.NewRequest(http.MethodPost, moduleRegisterPath, strings.NewReader(regBody))
	regReq.Header.Set(moduleRegistrationHeader, issued.Token)
	regResp := httptest.NewRecorder()
	gw.ServeHTTP(regResp, regReq)
	require.Equal(t, http.StatusOK, regResp.Code)

	proxied := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	proxied.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	proxiedResp := httptest.NewRecorder()
	gw.ServeHTTP(proxiedResp, proxied)
	require.Equal(t, http.StatusOK, proxiedResp.Code)
	require.Equal(t, "/v1/documents/collection", moduleFake.lastPath)

	// Registration added a target, not an auth bypass.
	unauth := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	unauthResp := httptest.NewRecorder()
	gw.ServeHTTP(unauthResp, unauth)
	require.Equal(t, http.StatusUnauthorized, unauthResp.Code)
}

func TestGateway_ModuleRegistrationToken_FailsClosed(t *testing.T) {
	tests := map[string]struct {
		prefix   string
		secret   string
		internal string
		want     int
	}{
		"no internal token":  {"documents", "documents-secret", "", http.StatusUnauthorized},
		"bad internal token": {"documents", "documents-secret", "wrong", http.StatusUnauthorized},
		"no module secret":   {"documents", "", "test-internal-token", http.StatusUnauthorized},
		"path prefix":        {"documents/nested", "documents-secret", "test-internal-token", http.StatusBadRequest},
		"wildcard prefix":    {"*", "documents-secret", "test-internal-token", http.StatusBadRequest},
		"empty prefix":       {"", "documents-secret", "test-internal-token", http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gw, mint, _ := newExchangeHarness(t)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, exchangeTokenRequest(test.prefix, test.secret, test.internal))

			require.Equal(t, test.want, w.Code)
			// Nothing reached accounts: the perimeter rejected it first.
			require.Zero(t, mint.requestCount)
		})
	}
}

// accounts owns the authorization decision, so its refusal is the gateway's.
func TestGateway_ModuleRegistrationToken_RelaysAccountsRefusal(t *testing.T) {
	gw, mint, _ := newExchangeHarness(t)
	mint.status = http.StatusUnauthorized

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, exchangeTokenRequest("documents", "wrong-secret", "test-internal-token"))

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, 1, mint.requestCount)
}

func TestGateway_ModuleRegistrationToken_UnavailableAuthority(t *testing.T) {
	gw, mint, _ := newExchangeHarness(t)
	mint.status = http.StatusServiceUnavailable

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, exchangeTokenRequest("documents", "documents-secret", "test-internal-token"))

	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestGateway_ModuleRegistrationToken_MethodNotAllowed(t *testing.T) {
	gw, _, _ := newExchangeHarness(t)

	req := httptest.NewRequest(http.MethodGet, moduleRegistrationTokenPath, nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// accounts' own exchange path is off the route catalog, so the gateway must not
// route to it: the exchange is reachable only through the brokered endpoint.
func TestGateway_AccountsMintPathNotRoutable(t *testing.T) {
	gw, mint, _ := newExchangeHarness(t)

	req := httptest.NewRequest(http.MethodPost, accountsModuleRegistrationPath, strings.NewReader(`{}`))
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Zero(t, mint.requestCount)
}
