package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigen "auth-gateway/external/saas-starter/accounts"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// scopedAPIKeyClient admits every key as one its owner scoped to users:read,
// standing in for accounts' ValidateAPIKey.
type scopedAPIKeyClient struct {
	apigen.APIKeyServiceClient
	userID, orgID string
}

func (c scopedAPIKeyClient) ValidateAPIKey(context.Context, *apigen.ValidateAPIKeyRequest, ...grpc.CallOption) (*apigen.ValidateAPIKeyResponse, error) {
	return &apigen.ValidateAPIKeyResponse{Valid: true, UserId: c.userID, OrganizationId: c.orgID, Scopes: []string{"users:read"}}, nil
}

// connectionNaming sets Connection the way a caller would to make a forwarding
// proxy delete the named headers: across two header lines, in mixed case, with
// stray whitespace and empty options.
func connectionNaming(req *http.Request, names ...string) {
	half := len(names) / 2
	req.Header.Add("Connection", " keep-alive , "+strings.ToLower(strings.Join(names[:half], " ,")))
	req.Header.Add("Connection", strings.ToUpper(strings.Join(names[half:], ", ,")))
}

// httputil.ReverseProxy deletes every header the caller names in Connection
// (RFC 9110 §7.6.1) after proxyTo hands the request over — after the identity
// headers and the gateway credential are stamped. Naming X-Scopes on an API-key
// request made accounts receive the gateway credential and the key owner's
// identity with no scope ceiling, which requireScope reads as an interactive
// session: the key got its owner's whole authority. Every header the gateway
// stamps reaches accounts whatever the caller names.
func TestGateway_ConnectionHeaderCannotRemoveStampedHeaders_APIKey(t *testing.T) {
	gw, apiFake, _, _ := newGatewayHarness(t)
	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()
	gw.authz.apiKey = scopedAPIKeyClient{userID: user, orgID: org}

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer cfly_sk_live_example")
	connectionNaming(req, "X-Scopes", "X-Credential-Kind", "X-Client-Id", "X-User-Id", "X-Org-Id", "X-Codefly-Gateway-Token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, apiFake.lastHeaders, "the request must still be forwarded")
	require.Equal(t, []string{"users:read"}, apiFake.lastHeaders.Values("X-Scopes"), "the key's scope ceiling reaches accounts")
	require.Equal(t, "api_key", apiFake.lastHeaders.Get("X-Credential-Kind"))
	require.Equal(t, user, apiFake.lastHeaders.Get("X-User-Id"))
	require.Equal(t, org, apiFake.lastHeaders.Get("X-Org-Id"))
	require.Equal(t, "test-gateway-token", apiFake.lastHeaders.Get("X-Codefly-Gateway-Token"))
	require.Contains(t, apiFake.lastHeaders, "X-Client-Id", "stamped empty, and still present")
}

// The same for a session: none of the canonical identity headers, the gateway
// credential or the verified public origin can be named away.
func TestGateway_ConnectionHeaderCannotRemoveStampedHeaders_Session(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	authenticateFrontendOrigin(req, "https://app.example.com")
	names := append([]string{"X-Codefly-Gateway-Token", "X-Codefly-Public-Origin"}, canonicalUpstreamAuthHeaders...)
	connectionNaming(req, names...)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, apiFake.lastHeaders)
	for _, name := range canonicalUpstreamAuthHeaders {
		require.Containsf(t, apiFake.lastHeaders, http.CanonicalHeaderKey(name), "stamped %s must reach accounts", name)
	}
	require.Equal(t, "admin", apiFake.lastHeaders.Get("X-Org-Role"))
	require.Equal(t, "super_admin", apiFake.lastHeaders.Get("X-Platform-Role"))
	require.Equal(t, "session", apiFake.lastHeaders.Get("X-Credential-Kind"))
	require.Equal(t, "test-gateway-token", apiFake.lastHeaders.Get("X-Codefly-Gateway-Token"))
	require.Equal(t, "https://app.example.com", apiFake.lastHeaders.Get("X-Codefly-Public-Origin"))
}

// Solution and module upstreams receive the identity the gateway stamps under
// the same guarantee: every proxy path goes through proxyTo.
func TestGateway_ConnectionHeaderCannotRemoveStampedHeaders_SolutionAndModule(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	solution := registerSolutionUpstream(t, gw, "audit")
	module, moduleURL := newModuleUpstream(t)
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", moduleURL).Code)

	for name, tc := range map[string]struct {
		path     string
		upstream *fakeUpstream
	}{
		"solution": {path: "/solutions/audit/v1/audit/logs", upstream: solution},
		"module":   {path: "/v1/documents/collection", upstream: module},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
			connectionNaming(req, "X-User-Id", "X-Org-Id", "X-Org-Role", "X-Credential-Kind")
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			require.NotEmpty(t, tc.upstream.lastHeaders.Get("X-User-Id"))
			require.NotEmpty(t, tc.upstream.lastHeaders.Get("X-Org-Id"))
			require.Equal(t, "admin", tc.upstream.lastHeaders.Get("X-Org-Role"))
			require.Equal(t, "session", tc.upstream.lastHeaders.Get("X-Credential-Kind"))
		})
	}
}

// Connection keeps its meaning for the caller's own headers — they are still
// not forwarded — and a protocol upgrade is still carried to the upstream.
func TestGateway_ConnectionHeaderStillRemovesCallerHeadersAndCarriesUpgrade(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("X-Caller-Hop", "for the gateway only")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "x-caller-hop, Upgrade, X-User-Id")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, apiFake.lastHeaders)
	require.NotContains(t, apiFake.lastHeaders, "X-Caller-Hop", "a header the caller named in Connection is not forwarded")
	require.Equal(t, "websocket", apiFake.lastHeaders.Get("Upgrade"))
	require.Equal(t, "Upgrade", apiFake.lastHeaders.Get("Connection"))
	require.NotEmpty(t, apiFake.lastHeaders.Get("X-User-Id"))
}
