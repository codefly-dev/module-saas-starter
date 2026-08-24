package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// registerSolutionUpstream wires a fake upstream into the runtime solution
// registry and returns its capture struct.
func registerSolutionUpstream(t *testing.T, gw *Gateway, id string) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{body: "solution-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	gw.solutions.set(id, u)
	return fake
}

// A registered solution is still ALWAYS auth-required: no bearer means the
// ext_authz Check denies and the upstream is never reached.
func TestGateway_Solution_NoToken_Denied(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "solution upstream must NEVER be reached without auth")
}

// An unregistered solution is rejected before any auth work, with 502.
func TestGateway_Solution_Unregistered_BadGateway(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/solutions/ghost/v1/thing", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "not registered")
}

// A solution's static Module-Federation surface (/assets/*) and public
// discovery documents (/.well-known/*) are served unauthenticated for reads: a
// browser's module loader fetches the MF manifest, remote entry, and chunks
// with no bearer, so gating them would break same-origin loading.
func TestGateway_Solution_PublicSurface_NoToken_OK(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	for _, path := range []string{
		"/assets/mf-manifest.json",
		"/assets/remoteEntry.js",
		"/assets/chunks/app.1a2b3c.js",
		"/assets",
		"/.well-known/capabilities",
	} {
		req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go"+path, nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "path %q must be served without a bearer", path)
		require.Equal(t, "solution-response", w.Body.String())
		require.Equal(t, path, fake.lastPath, "path is rewritten to the solution suffix")
	}
}

// The public read exemption never forwards caller-supplied identity: a request
// spoofing identity headers reaches the upstream with them stripped.
func TestGateway_Solution_PublicSurface_StripsSpoofedIdentity(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go/assets/mf-manifest.json", nil)
	req.Header.Set("x-user-id", "attacker")
	req.Header.Set("x-org-role", "super_admin")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, fake.lastHeaders.Get("x-user-id"))
	require.Empty(t, fake.lastHeaders.Get("x-org-role"))
}

// The exemption is read-only: a non-GET/HEAD request to the public surface is
// still auth-required, so it cannot be used as an unauthenticated write path.
func TestGateway_Solution_PublicSurface_NonReadStillAuthRequired(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodPost, "/solutions/lastlogin-go/assets/mf-manifest.json", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")
}

// A traversal suffix cannot borrow the /assets exemption to reach an
// authenticated endpoint: the decision is made on the cleaned path.
func TestGateway_Solution_PublicSurface_TraversalStillAuthRequired(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go/assets/../lastlogin", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")
}

// A solution's data endpoints stay auth-required: /lastlogin needs a valid
// bearer for both GET and POST, and reaches the upstream once presented.
func TestGateway_Solution_DataEndpoint_RequiresBearer(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		fake.lastHeaders = nil
		noTok := httptest.NewRequest(method, "/solutions/lastlogin-go/lastlogin", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, noTok)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s /lastlogin without a bearer must be denied", method)
		require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")

		withTok := httptest.NewRequest(method, "/solutions/lastlogin-go/lastlogin", nil)
		withTok.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
		w = httptest.NewRecorder()
		gw.ServeHTTP(w, withTok)
		require.Equal(t, http.StatusOK, w.Code, "%s /lastlogin with a valid bearer is forwarded", method)
		require.Equal(t, "/lastlogin", fake.lastPath)
	}
}

// A valid JWT is projected into identity headers exactly as for a catalog
// route, the path is rewritten to the solution suffix, and the caller's bearer
// is preserved so the solution can call downstream services on the user's
// behalf.
func TestGateway_Solution_ValidJWT_ForwardsWithIdentity(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	token := signValidToken(t, priv)
	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs?limit=5", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "solution-response", w.Body.String())
	// Path is rewritten to the suffix after /solutions/{id}.
	require.Equal(t, "/v1/audit/logs", fake.lastPath)
	// Identity is projected from the validated JWT.
	require.NotEmpty(t, fake.lastHeaders.Get("x-user-id"))
	require.NotEmpty(t, fake.lastHeaders.Get("x-org-id"))
	require.Equal(t, "admin", fake.lastHeaders.Get("x-org-role"))
	// The caller's bearer is preserved for the solution's own downstream calls.
	require.Equal(t, "Bearer "+token, fake.lastHeaders.Get("authorization"))
	// The gateway token is an accounts-only capability and must not leak to a
	// solution upstream.
	require.Empty(t, fake.lastHeaders.Get("x-codefly-gateway-token"))
}

// Caller-supplied identity headers are stripped and re-stamped from the token,
// never trusted as presented — same discipline as every protected route.
func TestGateway_Solution_StripsSpoofedIdentity(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("x-user-id", "attacker")
	req.Header.Set("x-org-role", "super_admin")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotEqual(t, "attacker", fake.lastHeaders.Get("x-user-id"))
	require.Equal(t, "admin", fake.lastHeaders.Get("x-org-role"))
}

// A request with no solution id after the prefix is a 404, not a proxy attempt.
func TestGateway_Solution_MissingID_NotFound(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/solutions/", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// The internal registration endpoint accepts a valid upstream and makes the
// solution routable immediately afterward.
func TestGateway_Solution_Register(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	fake := &fakeUpstream{body: "solution-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	regBody := `{"id":"audit","upstream":"` + srv.URL + `"}`
	regReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(regBody))
	regReq.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	regW := httptest.NewRecorder()
	gw.ServeHTTP(regW, regReq)
	require.Equal(t, http.StatusOK, regW.Code)

	// Now routable.
	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "/v1/ping", fake.lastPath)
}

func TestGateway_Solution_Register_Rejects(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	// Non-POST is rejected before any auth work.
	getReq := httptest.NewRequest(http.MethodGet, "/solutions/_register", nil)
	getW := httptest.NewRecorder()
	gw.ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusMethodNotAllowed, getW.Code)

	// Missing id (with a valid internal token, to isolate the id check).
	noIDReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(`{"upstream":"http://x:80"}`))
	noIDReq.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	noIDW := httptest.NewRecorder()
	gw.ServeHTTP(noIDW, noIDReq)
	require.Equal(t, http.StatusBadRequest, noIDW.Code)

	// Non-http scheme is rejected (no file://, no scheme-less host).
	badReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(`{"id":"x","upstream":"file:///etc/passwd"}`))
	badReq.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	badW := httptest.NewRecorder()
	gw.ServeHTTP(badW, badReq)
	require.Equal(t, http.StatusBadRequest, badW.Code)
}

// Registration is privileged: without the cluster-internal token it is
// rejected before the upstream is ever stored, so an edge caller cannot point
// authenticated traffic at an attacker-controlled host.
func TestGateway_Solution_Register_RequiresInternalToken(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	body := `{"id":"evil","upstream":"http://attacker.example"}`

	// No token.
	noTok := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(body))
	noTokW := httptest.NewRecorder()
	gw.ServeHTTP(noTokW, noTok)
	require.Equal(t, http.StatusUnauthorized, noTokW.Code)

	// Wrong token.
	badTok := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(body))
	badTok.Header.Set("X-Codefly-Internal-Token", "not-the-token")
	badTokW := httptest.NewRecorder()
	gw.ServeHTTP(badTokW, badTok)
	require.Equal(t, http.StatusUnauthorized, badTokW.Code)

	// The upstream was never registered, so even a fully authenticated user
	// gets 502, not a proxy to attacker.example.
	req := httptest.NewRequest(http.MethodGet, "/solutions/evil/x", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
}

// A confused/compromised internal caller still cannot register a
// credential-theft SSRF sink (cloud metadata / link-local / unspecified).
// Loopback is intentionally allowed, so it is not asserted here.
func TestGateway_Solution_Register_RejectsSSRFHosts(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	for _, upstream := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/",
		"http://[fe80::1]:9000",
		"http://0.0.0.0:9000",
	} {
		req := httptest.NewRequest(http.MethodPost, "/solutions/_register",
			strings.NewReader(`{"id":"x","upstream":"`+upstream+`"}`))
		req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "upstream %q must be rejected", upstream)
	}
}
