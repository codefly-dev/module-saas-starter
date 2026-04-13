//go:build integration
// +build integration

package main

// Full-stack gateway integration test.
//
// Brings up real store (postgres), vault, and api via codefly
// WithDependencies (see TestMain in sidecar_integration_test.go),
// constructs the sidecar + HTTP gateway in the test process, then
// sends real HTTP requests through the gateway chain:
//
//	curl -> Gateway.ServeHTTP -> Sidecar.Check -> httputil.ReverseProxy -> api REST
//
// Covers the exact hot path a production Envoy deployment would
// exercise on every request, minus the Envoy container itself.
//
// Run with:  go test -tags integration ./...

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// newTestGateway wires the test sidecar into a Gateway pointed at the
// real api REST endpoint resolved through the codefly SDK, then binds
// the gateway to an ephemeral localhost port.
func newTestGateway(t *testing.T) (baseURL string, teardown func()) {
	t.Helper()

	apiRestNet := codefly.For(testCtx).Service("api").API("rest").NetworkInstance()
	require.NotNil(t, apiRestNet, "api REST endpoint not available")
	apiURL, err := url.Parse(fmt.Sprintf("http://%s:%d", apiRestNet.Hostname, apiRestNet.Port))
	require.NoError(t, err)

	// Load the real route config.
	entries, err := LoadRoutesFromDir(context.Background(), DefaultRoutingDir())
	require.NoError(t, err)
	matcher := NewRouteMatcher(entries)

	upstreams := map[string]*url.URL{
		"api": apiURL,
	}

	gateway := NewGateway(testSidecar, matcher, upstreams, nil)

	// Bind to :0 so we get an ephemeral port that can't clash with
	// anything else the daemon allocated.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{Handler: gateway}
	go func() { _ = srv.Serve(lis) }()

	// Give the listener a moment to settle before returning.
	time.Sleep(20 * time.Millisecond)

	teardown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return "http://" + lis.Addr().String(), teardown
}

// doRequest issues an HTTP request through the gateway and returns
// (status, body, headers).
func doRequest(t *testing.T, method, urlStr string, body any, bearer string) (int, []byte, http.Header) {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, urlStr, reqBody)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, resp.Header
}

// ============================================================================
// Protected route — unauthenticated denied
// ============================================================================

func TestIntegration_Gateway_UnauthOnProtectedRoute_Denied(t *testing.T) {
	base, teardown := newTestGateway(t)
	defer teardown()

	status, body, _ := doRequest(t, http.MethodGet, base+"/v1/users", nil, "")
	require.Equal(t, 401, status, "protected route must deny missing auth")
	require.Contains(t, string(body), "auth")
}

// ============================================================================
// Public route — login allowed through without token
// ============================================================================

func TestIntegration_Gateway_LoginPath_NoToken_Allowed(t *testing.T) {
	base, teardown := newTestGateway(t)
	defer teardown()

	// Login through the gateway with no prior token. Authenticate proto
	// path: POST /v1/auth/authenticate. The route config marks this as
	// public so it must be allowed through.
	status, body, _ := doRequest(t, http.MethodPost, base+"/v1/auth/authenticate",
		map[string]any{
			"provider":       "google",
			"provider_id":    "integration-user-1",
			"provider_email": "integration-1@test.local",
		}, "")
	require.Equal(t, 200, status, "login must succeed: %s", body)

	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			Uuid string `json:"uuid"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	require.NotEmpty(t, resp.AccessToken, "access token returned")
	require.NotEmpty(t, resp.RefreshToken, "refresh token returned")
	require.NotEmpty(t, resp.User.Uuid)
}

// ============================================================================
// Full round trip — login, then use token on protected route
// ============================================================================

func TestIntegration_Gateway_AuthenticatedRequest_Allowed(t *testing.T) {
	base, teardown := newTestGateway(t)
	defer teardown()

	// Step 1: login via the public /v1/auth/authenticate path.
	loginStatus, loginBody, _ := doRequest(t, http.MethodPost, base+"/v1/auth/authenticate",
		map[string]any{
			"provider":       "google",
			"provider_id":    "integration-user-2",
			"provider_email": "integration-2@test.local",
		}, "")
	require.Equal(t, 200, loginStatus, "login: %s", loginBody)

	var login struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))
	require.NotEmpty(t, login.AccessToken)

	// Step 2: use the token on a protected endpoint through the gateway.
	status, body, _ := doRequest(t, http.MethodGet,
		base+"/v1/users", nil, login.AccessToken)
	require.NotEqual(t, 401, status, "valid token must not be rejected: %s", body)
	require.NotEqual(t, 403, status, "valid token must not be forbidden: %s", body)
}

// ============================================================================
// Sidecar strips forged identity headers when caller has no token
// ============================================================================

func TestIntegration_Gateway_StripsForgedIdentityHeaders(t *testing.T) {
	base, teardown := newTestGateway(t)
	defer teardown()

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/auth/authenticate",
		bytes.NewReader([]byte(`{"provider":"google","provider_id":"strip-test","provider_email":"strip@test.local"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", "attacker-spoofed-uuid")
	req.Header.Set("X-Platform-Role", "super_admin")
	req.Header.Set("X-Org-Role", "owner")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode, "login should succeed despite forged headers")

	body, _ := io.ReadAll(resp.Body)
	var out struct {
		User struct {
			Uuid string `json:"uuid"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotEmpty(t, out.User.Uuid)
	require.NotEqual(t, "attacker-spoofed-uuid", out.User.Uuid,
		"gateway must not let caller influence identity via forged headers")
}

// ============================================================================
// Malformed token denied with 401 (not forwarded)
// ============================================================================

func TestIntegration_Gateway_MalformedToken_Denied(t *testing.T) {
	base, teardown := newTestGateway(t)
	defer teardown()

	status, _, _ := doRequest(t, http.MethodGet,
		base+"/v1/users", nil, "total-garbage-not-a-jwt")
	require.Equal(t, 401, status)
}

// ============================================================================
// Unlisted path — 404 through real gateway
// ============================================================================

func TestIntegration_Gateway_UnlistedPath_404(t *testing.T) {
	base, teardown := newTestGateway(t)
	defer teardown()

	status, body, _ := doRequest(t, http.MethodGet, base+"/v1/secret-admin-panel", nil, "")
	require.Equal(t, 404, status, "unlisted path must return 404")
	require.Contains(t, string(body), "endpoint not exposed")
}
