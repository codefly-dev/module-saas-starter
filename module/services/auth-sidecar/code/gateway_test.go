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
		{Service: "accounts", Method: "POST", Path: "/v1/auth/authenticate", Protected: false},
		{Service: "accounts", Method: "POST", Path: "/v1/auth/refresh", Protected: false},
		{Service: "accounts", Method: "POST", Path: "/v1/auth/logout", Protected: false},
		{Service: "accounts", Method: "GET", Path: "/v1/auth/.well-known/jwks.json", Protected: false},
		// MFA pending (treated as protected for gateway purposes)
		{Service: "accounts", Method: "POST", Path: "/v1/mfa/totp/verify", Protected: true},
		// Protected routes
		{Service: "accounts", Method: "GET", Path: "/v1/users", Protected: true},
		{Service: "accounts", Method: "GET", Path: "/v1/users/{uuid}", Protected: true},
		{Service: "accounts", Method: "GET", Path: "/v1/users/self", Protected: true},
		{Service: "accounts", Method: "POST", Path: "/v1/users", Protected: true},
		{Service: "accounts", Method: "DELETE", Path: "/v1/users/{uuid}", Protected: true},
		{Service: "accounts", Method: "POST", Path: "/v1/users/{user_uuid}/identities", Protected: true},
		{Service: "accounts", Method: "GET", Path: "/v1/users/{user_uuid}/identities", Protected: true},
		{Service: "accounts", Method: "DELETE", Path: "/v1/mfa/devices/{id}", Protected: true},
		{Service: "accounts", Method: "GET", Path: "/v1/version", Protected: false},
		// Billing webhook (public)
		{Service: "accounts", Method: "POST", Path: "/v1/billing/webhook", Protected: false},
		// Resend delivery webhook (public, signed by Svix)
		{Service: "accounts", Method: "POST", Path: "/v1/email/webhook/resend", Protected: false},
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
		publicKey:    pub,
		issuer:       "saas-starter",
		audience:     "saas-starter",
		gatewayToken: "test-gateway-token",
	}

	apiFake := &fakeUpstream{body: "api-response"}
	apiSrv := httptest.NewServer(apiFake)
	t.Cleanup(apiSrv.Close)

	frontendFake := &fakeUpstream{body: "frontend-response"}
	frontendSrv := httptest.NewServer(frontendFake)
	t.Cleanup(frontendSrv.Close)

	apiURL, _ := url.Parse(apiSrv.URL)
	frontendURL, _ := url.Parse(frontendSrv.URL)

	matcher := NewRouteMatcher(testRouteEntries(), nil)
	upstreams := map[string]*url.URL{
		"accounts": apiURL,
		"frontend": frontendURL,
	}

	gateway := NewGateway(
		sidecar,
		matcher,
		upstreams,
		nil,
		WithPublicOrigin("http://localhost:54321"),
	)

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
	require.Equal(t, "test-gateway-token", apiFake.lastHeaders.Get("x-codefly-gateway-token"))
	require.Equal(t, "http://localhost:54321", apiFake.lastHeaders.Get("x-codefly-public-origin"))
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
	require.Equal(t, "test-gateway-token", apiFake.lastHeaders.Get("x-codefly-gateway-token"))
}

func TestGateway_StripsCallerIdentityAndTrustCredentials(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	for _, key := range untrustedAuthHeaders {
		req.Header.Set(key, "attacker-value")
	}
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	for _, key := range untrustedAuthHeaders {
		if key == "x-codefly-gateway-token" {
			require.Equal(t, "test-gateway-token", apiFake.lastHeaders.Get(key))
			continue
		}
		if key == "x-codefly-public-origin" {
			require.Equal(t, "http://localhost:54321", apiFake.lastHeaders.Get(key))
			continue
		}
		require.Empty(t, apiFake.lastHeaders.Get(key), "caller-controlled %s must be stripped", key)
	}
}

func TestCanonicalPublicOriginAcceptsCodeflyEndpointAddress(t *testing.T) {
	origin, err := canonicalPublicOrigin("http://localhost:54321")
	require.NoError(t, err)
	require.Equal(t, "http://localhost:54321", origin)

	for _, candidate := range []string{
		"localhost:54321",
		"http://localhost:54321/auth/callback",
		"http://localhost:54321?spoof=true",
		"ftp://localhost:54321",
	} {
		_, err := canonicalPublicOrigin(candidate)
		require.Error(t, err, candidate)
	}
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
		{"GET", "/v1/email/webhook/resend"}, // POST only
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

	req := httptest.NewRequest(http.MethodPost, "/saas.accounts.v1.UserService/SecretMethod", nil)
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

func TestGateway_RoutesGeneratedFrontendPagesAssetsAndHandlers(t *testing.T) {
	gw, _, frontendFake, _ := newGatewayHarness(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/auth/login"},
		{http.MethodGet, "/admin/installed-plugin/page"},
		{http.MethodGet, "/_next/static/chunks/app.js"},
		{http.MethodGet, "/api/fixtures"},
		{http.MethodPost, "/api/plugins/example/accounts/v1/action"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.path, frontendFake.lastPath)
			require.Equal(t, tc.method, frontendFake.lastMethod)
		})
	}
}

func TestGateway_UnknownFrontendLikePathsRemainClosed(t *testing.T) {
	gw, _, frontendFake, _ := newGatewayHarness(t)
	for _, path := range []string{"/unknown", "/api/unknown", "/v1/unknown"} {
		frontendFake.lastPath = ""
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.Empty(t, frontendFake.lastPath)
	}
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

func TestGateway_LivenessDoesNotDependOnSidecarOrUpstreams(t *testing.T) {
	matcher := NewRouteMatcher([]*RouteEntry{{Service: "accounts", Method: "GET", Path: "/v1/users", Protected: true}, {Service: "self", Method: "GET", Path: "/health"}}, nil)
	gateway := NewGateway(nil, matcher, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestGateway_ReadinessRequiresEveryRoutedUpstream(t *testing.T) {
	available := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(available.Close)
	availableURL, err := url.Parse(available.URL)
	require.NoError(t, err)

	matcher := NewRouteMatcher([]*RouteEntry{
		{Service: "accounts", Method: "GET", Path: "/v1/users", Protected: true},
		{Service: "frontend", Method: "GET", Path: "/", Protected: false},
		{Service: "self", Method: "GET", Path: "/ready", Protected: false},
	}, nil)

	gateway := NewGateway(&Sidecar{}, matcher, map[string]*url.URL{"accounts": availableURL}, nil)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "frontend")

	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	unavailableURL, err := url.Parse(unavailable.URL)
	require.NoError(t, err)
	unavailable.Close()
	gateway = NewGateway(&Sidecar{}, matcher, map[string]*url.URL{"accounts": availableURL, "frontend": unavailableURL}, nil)
	w = httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "frontend")
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

func TestGateway_PublicAccountsRouteReceivesBackendCapabilities(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	req.Header.Set("X-Codefly-Gateway-Token", "caller-controlled")
	req.Header.Set("X-Codefly-Public-Origin", "https://evil.example")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "test-gateway-token", apiFake.lastHeaders.Get("X-Codefly-Gateway-Token"))
	require.Equal(t, "http://localhost:54321", apiFake.lastHeaders.Get("X-Codefly-Public-Origin"))
}

func TestGateway_DoesNotForwardBackendCapabilitiesToFrontend(t *testing.T) {
	gw, _, frontendFake, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Codefly-Gateway-Token", "caller-controlled")
	req.Header.Set("X-Codefly-Public-Origin", "https://evil.example")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, frontendFake.lastHeaders.Get("X-Codefly-Gateway-Token"))
	require.Empty(t, frontendFake.lastHeaders.Get("X-Codefly-Public-Origin"))
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
	matcher := NewRouteMatcher([]*RouteEntry{}, nil)
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

func TestGateway_ConnectProtocol_AuthenticatedEndToEnd(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sidecar := &Sidecar{
		publicKey:    pub,
		issuer:       "saas-starter",
		audience:     "saas-starter",
		gatewayToken: "test-gateway-token",
	}

	upstream := &fakeUpstream{body: `{"user":{"id":"user-1"}}`}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)
	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)

	procedure := "/saas.accounts.v1.UserService/GetSelf"
	matcher := NewRouteMatcher(nil, []*RouteEntry{{
		Service: "accounts_connect", Method: http.MethodPost, Path: procedure, Protected: true,
	}})
	gateway := NewGateway(sidecar, matcher, map[string]*url.URL{"accounts_connect": upstreamURL}, nil)

	req := httptest.NewRequest(http.MethodPost, procedure, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, procedure, upstream.lastPath)
	require.Equal(t, http.MethodPost, upstream.lastMethod)
	require.NotEmpty(t, upstream.lastHeaders.Get("X-User-Id"))
	require.Equal(t, "test-gateway-token", upstream.lastHeaders.Get("X-Codefly-Gateway-Token"))
	require.Equal(t, "1", upstream.lastHeaders.Get("Connect-Protocol-Version"))
}

func TestGateway_LegacyConnectProcedureRewritesToV1(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sidecar := &Sidecar{
		publicKey:    pub,
		issuer:       "saas-starter",
		audience:     "saas-starter",
		gatewayToken: "test-gateway-token",
	}

	upstream := &fakeUpstream{body: `{}`}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)
	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)

	legacy := "/customers.UserService/GetSelf"
	canonical := "/saas.accounts.v1.UserService/GetSelf"
	matcher := NewRouteMatcher(nil, []*RouteEntry{{
		Service:      "accounts_connect",
		Method:       http.MethodPost,
		Path:         legacy,
		UpstreamPath: canonical,
		Protected:    true,
	}})
	gateway := NewGateway(sidecar, matcher, map[string]*url.URL{"accounts_connect": upstreamURL}, nil)

	req := httptest.NewRequest(http.MethodPost, legacy, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, canonical, upstream.lastPath)
}
