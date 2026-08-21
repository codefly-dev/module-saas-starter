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

	// Non-POST is rejected.
	getReq := httptest.NewRequest(http.MethodGet, "/solutions/_register", nil)
	getW := httptest.NewRecorder()
	gw.ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusMethodNotAllowed, getW.Code)

	// Missing id.
	noIDReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(`{"upstream":"http://x:80"}`))
	noIDW := httptest.NewRecorder()
	gw.ServeHTTP(noIDW, noIDReq)
	require.Equal(t, http.StatusBadRequest, noIDW.Code)

	// Non-http scheme is rejected (no file://, no scheme-less host).
	badReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(`{"id":"x","upstream":"file:///etc/passwd"}`))
	badW := httptest.NewRecorder()
	gw.ServeHTTP(badW, badReq)
	require.Equal(t, http.StatusBadRequest, badW.Code)
}
