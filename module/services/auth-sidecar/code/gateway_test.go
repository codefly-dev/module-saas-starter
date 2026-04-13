package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeUpstream captures the headers an upstream would see after the
// gateway forwards the request.
type fakeUpstream struct {
	lastHeaders http.Header
	lastPath    string
	lastMethod  string
	statusCode  int
	body        string
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.lastHeaders = r.Header.Clone()
	f.lastPath = r.URL.Path
	f.lastMethod = r.Method
	code := f.statusCode
	if code == 0 {
		code = 200
	}
	w.WriteHeader(code)
	_, _ = io.WriteString(w, f.body)
}

// testRouteEntries returns a minimal set of RouteEntry for testing.
func testRouteEntries() []*RouteEntry {
	return []*RouteEntry{
		// Public auth routes
		{Service: "api", Method: "POST", Path: "/v1/auth/authenticate", Protected: false},
		{Service: "api", Method: "POST", Path: "/v1/auth/refresh", Protected: false},
		{Service: "api", Method: "POST", Path: "/v1/auth/logout", Protected: false},
		{Service: "api", Method: "GET", Path: "/v1/auth/.well-known/jwks.json", Protected: false},
		// MFA pending (treated as protected for gateway purposes)
		{Service: "api", Method: "POST", Path: "/v1/mfa/totp/verify", Protected: true},
		// Protected routes
		{Service: "api", Method: "GET", Path: "/v1/users", Protected: true},
		{Service: "api", Method: "GET", Path: "/v1/users/{uuid}", Protected: true},
		{Service: "api", Method: "GET", Path: "/v1/users/self", Protected: true},
		{Service: "api", Method: "POST", Path: "/v1/users", Protected: true},
		{Service: "api", Method: "DELETE", Path: "/v1/users/{uuid}", Protected: true},
		{Service: "api", Method: "POST", Path: "/v1/users/{user_uuid}/identities", Protected: true},
		{Service: "api", Method: "GET", Path: "/v1/users/{user_uuid}/identities", Protected: true},
		{Service: "api", Method: "DELETE", Path: "/v1/mfa/devices/{id}", Protected: true},
		{Service: "api", Method: "GET", Path: "/v1/version", Protected: false},
		// Billing webhook (public)
		{Service: "api", Method: "POST", Path: "/v1/billing/webhook", Protected: false},
		// Frontend
		{Service: "frontend", Method: "GET", Path: "/", Protected: false},
		// Health checks
		{Service: "self", Method: "GET", Path: "/health", Protected: false},
		{Service: "self", Method: "GET", Path: "/healthz", Protected: false},
		{Service: "self", Method: "GET", Path: "/ready", Protected: false},
	}
}

func newGatewayHarness(t *testing.T) (*Gateway, *fakeUpstream, *fakeUpstream, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	sidecar := &Sidecar{
		publicKey: pub,
		issuer:    "saas-starter",
		audience:  "saas-starter",
	}

	apiFake := &fakeUpstream{body: "api-response"}
	apiSrv := httptest.NewServer(apiFake)
	t.Cleanup(apiSrv.Close)

	frontendFake := &fakeUpstream{body: "frontend-response"}
	frontendSrv := httptest.NewServer(frontendFake)
	t.Cleanup(frontendSrv.Close)

	apiURL, _ := url.Parse(apiSrv.URL)
	frontendURL, _ := url.Parse(frontendSrv.URL)

	matcher := NewRouteMatcher(testRouteEntries())
	upstreams := map[string]*url.URL{
		"api":      apiURL,
		"frontend": frontendURL,
	}

	gateway := NewGateway(sidecar, matcher, upstreams, nil)

	return gateway, apiFake, frontendFake, priv
}

func signValidToken(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	c := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   uuid.Must(uuid.NewV7()).String(),
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti",
		},
		OrgID:        uuid.Must(uuid.NewV7()).String(),
		OrgRole:      "admin",
		PlatformRole: "super_admin",
		SessionID:    uuid.Must(uuid.NewV7()).String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

// ============================================================================
// Public routes — no auth required
// ============================================================================

func TestGateway_PublicAuthPath_NoToken_Forwarded(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "api-response", w.Body.String())
	require.Equal(t, "/v1/auth/authenticate", apiFake.lastPath)
	// Public path: no x-user-id header should be present
	require.Equal(t, "", apiFake.lastHeaders.Get("x-user-id"))
}

// ============================================================================
// Protected routes — JWT required
// ============================================================================

func TestGateway_ProtectedRoute_NoToken_Denied(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, apiFake.lastHeaders, "upstream must NEVER be reached without auth")
}

func TestGateway_ProtectedRoute_ValidJWT_ForwardsWithHeaders(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	token := signValidToken(t, priv)
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "api-response", w.Body.String())
	require.NotEmpty(t, apiFake.lastHeaders.Get("x-user-id"))
	require.NotEmpty(t, apiFake.lastHeaders.Get("x-org-id"))
	require.Equal(t, "admin", apiFake.lastHeaders.Get("x-org-role"))
	require.Equal(t, "super_admin", apiFake.lastHeaders.Get("x-platform-role"))
	require.NotEmpty(t, apiFake.lastHeaders.Get("x-session-id"))
}

// ============================================================================
// Unlisted paths — MUST return 404
// ============================================================================

func TestGateway_UnlistedPath_Returns404(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/v1/secret-admin-panel"},
		{"POST", "/v1/internal/debug"},
		{"GET", "/api/v1/something"},
		{"DELETE", "/v1/auth/authenticate"}, // wrong method
		{"GET", "/v1/billing/webhook"},      // POST only
		{"GET", "/random"},
		{"GET", "/v2/users"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			apiFake.lastHeaders = nil
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code,
				"unlisted path %s %s must return 404", tc.method, tc.path)
			require.Contains(t, w.Body.String(), "endpoint not exposed")
			require.Nil(t, apiFake.lastHeaders,
				"unlisted path must never reach upstream")
		})
	}
}

// ============================================================================
// Path parameter matching
// ============================================================================

func TestGateway_PathParameterMatching(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	token := signValidToken(t, priv)

	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/v1/users/abc-123-def"},
		{"GET", "/v1/users/" + uuid.Must(uuid.NewV7()).String()},
		{"DELETE", "/v1/users/some-uuid"},
		{"POST", "/v1/users/user-123/identities"},
		{"GET", "/v1/users/user-456/identities"},
		{"DELETE", "/v1/mfa/devices/device-789"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			apiFake.lastHeaders = nil
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, 200, w.Code,
				"path with parameter %s %s must match", tc.method, tc.path)
			require.Equal(t, tc.path, apiFake.lastPath)
		})
	}
}

// ============================================================================
// Connect RPC path matching
// ============================================================================
// NOTE: Connect RPC routing is now handled by Envoy only (via EnvoyRouteEntry).
// The Go gateway's RouteMatcher uses RouteEntry which has no Connect field.
// Connect paths that don't match a REST route correctly return 404.

func TestGateway_ConnectRPC_UnlistedMethod_404(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/customers.UserService/SecretMethod", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "endpoint not exposed")
}

// ============================================================================
// Route selection — frontend
// ============================================================================

func TestGateway_RoutesFrontendByDefault(t *testing.T) {
	gw, _, frontendFake, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "frontend-response", w.Body.String())
	require.Equal(t, "/", frontendFake.lastPath)
}

// ============================================================================
// Health checks — self service
// ============================================================================

func TestGateway_HealthCheck_Self(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	for _, path := range []string{"/health", "/healthz", "/ready"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, 200, w.Code)
			require.Contains(t, w.Body.String(), `"status":"ok"`)
		})
	}
}

// ============================================================================
// Identity header stripping — caller cannot spoof auth headers
// ============================================================================

func TestGateway_StripsCallerInjectedIdentityHeaders(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	// Public path — no sidecar will set identity headers. If the caller
	// injected them, the gateway MUST strip them so the upstream never
	// sees forged identity.
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	req.Header.Set("x-user-id", "attacker-spoofed-uuid")
	req.Header.Set("x-org-role", "owner")
	req.Header.Set("x-platform-role", "super_admin")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "", apiFake.lastHeaders.Get("x-user-id"),
		"caller-injected x-user-id must be stripped on public routes")
	require.Equal(t, "", apiFake.lastHeaders.Get("x-org-role"),
		"caller-injected x-org-role must be stripped")
	require.Equal(t, "", apiFake.lastHeaders.Get("x-platform-role"),
		"caller-injected x-platform-role must be stripped")
}

func TestGateway_AuthenticatedRequest_CallerHeadersOverridden(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	token := signValidToken(t, priv)
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+token)
	// Attacker attempts to set platform_role via request header
	req.Header.Set("x-platform-role", "attacker-claim")
	req.Header.Set("x-user-id", "attacker-sub")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	// Must be the sidecar-derived values from the JWT, not the forged ones
	require.Equal(t, "super_admin", apiFake.lastHeaders.Get("x-platform-role"))
	require.NotEqual(t, "attacker-sub", apiFake.lastHeaders.Get("x-user-id"))
}

// ============================================================================
// Deny behavior
// ============================================================================

func TestGateway_InvalidToken_Denied_NoUpstreamCall(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer total-garbage")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, apiFake.lastHeaders, "invalid token must not reach upstream")
}

func TestGateway_NoRoute_404(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sidecar := &Sidecar{
		publicKey: pub,
		issuer:    "saas-starter",
		audience:  "saas-starter",
	}

	// Empty route config — nothing is whitelisted.
	matcher := NewRouteMatcher([]*RouteEntry{})
	gateway := NewGateway(sidecar, matcher, map[string]*url.URL{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// ============================================================================
// Security headers on error responses
// ============================================================================

func TestGateway_ErrorResponse_HasNosniff(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

// ============================================================================
// Exact method matching — same path, different methods
// ============================================================================

func TestGateway_ExactMethodMatching(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	token := signValidToken(t, priv)

	// GET /v1/users is listed (required auth)
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	// PUT /v1/users is NOT listed — should 404
	req = httptest.NewRequest(http.MethodPut, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}
