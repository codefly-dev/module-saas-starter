//go:build integration
// +build integration

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	codefly "github.com/codefly-dev/sdk-go"

	"github.com/codefly-dev/core/sdk"
	"github.com/stretchr/testify/require"

	apigen "auth-gateway/external/saas-starter/accounts"
)

// Global test fixtures — initialized once in TestMain.
var (
	testSidecar    *Sidecar
	testAuthClient apigen.AuthServiceClient
	testCtx        context.Context
)

func TestMain(m *testing.M) {
	os.Exit(runSidecarIntegrationTests(m))
}

func runSidecarIntegrationTests(m *testing.M) int {
	// The accounts transport deliberately fails closed for internal RPCs.
	// WithDependencies inherits this process environment, so both services use
	// the same integration-only credential.
	_ = os.Setenv("CODEFLY_INTERNAL_TOKEN", "integration-test-internal-token")
	_ = os.Setenv("CODEFLY_GATEWAY_TOKEN", "integration-test-gateway-token")
	ctx := context.Background()

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("sidecar-test"),
		sdk.WithTimeout(90*time.Second),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies failed: %v\n", err)
		return 1
	}
	defer deps.Destroy(ctx)

	_, err = codefly.Init(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init failed: %v\n", err)
		return 1
	}

	apiNet := codefly.For(ctx).Service("accounts").API("grpc").NetworkInstance()
	if apiNet == nil {
		fmt.Fprintf(os.Stderr, "backend gRPC endpoint not available\n")
		return 1
	}
	apiAddr := fmt.Sprintf("%s:%d", apiNet.Hostname, apiNet.Port)

	apiConn, err := grpc.NewClient(apiAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to backend: %v\n", err)
		return 1
	}
	defer apiConn.Close()

	// Fetch public key from backend's JWKS endpoint
	publicKey := fetchTestPublicKey(ctx, apiConn)

	internalNet := codefly.For(ctx).Service("accounts").API("rest").NetworkInstance()
	if internalNet == nil {
		fmt.Fprintf(os.Stderr, "backend internal gRPC endpoint not available\n")
		return 1
	}
	internalConn, err := grpc.NewClient(fmt.Sprintf("%s:%d", internalNet.Hostname, internalNet.Port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to internal backend: %v\n", err)
		return 1
	}
	defer internalConn.Close()
	testSidecar = NewSidecar(internalConn, publicKey)

	// Same wiring as main: the revoker reads the Redis revocation set accounts
	// writes on logout. The cache service is a declared dependency, so a
	// missing connection here is a broken graph, not an optional feature.
	redisURL, redisErr := codefly.For(ctx).Service("cache").Secret("redis", "connection")
	if redisErr != nil || redisURL == "" {
		redisURL = os.Getenv("REDIS_URL")
	}
	if redisURL == "" {
		fmt.Fprintf(os.Stderr, "cache Redis connection not available\n")
		return 1
	}
	revoker, err := newRevoker(redisURL, defaultRevocationCacheTTL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot build revoker: %v\n", err)
		return 1
	}
	testSidecar.SetRevoker(revoker)

	testAuthClient = apigen.NewAuthServiceClient(apiConn)
	testCtx = ctx

	return m.Run()
}

func fetchTestPublicKey(ctx context.Context, conn *grpc.ClientConn) ed25519.PublicKey {
	client := apigen.NewAuthServiceClient(conn)

	// Retry — backend may still be starting
	var resp *apigen.JWKSResponse
	var err error
	for i := 0; i < 30; i++ {
		resp, err = client.GetJWKS(ctx, &emptypb.Empty{})
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: cannot fetch JWKS after retries: %v\n", err)
		return nil
	}

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(resp.KeysJson), &jwks); err != nil {
		return nil
	}
	for _, key := range jwks.Keys {
		if key.Kty == "OKP" && key.Crv == "Ed25519" {
			pub, _ := base64.RawURLEncoding.DecodeString(key.X)
			return ed25519.PublicKey(pub)
		}
	}
	return nil
}

func makeCheckRequest(headers map[string]string) *authv3.CheckRequest {
	return &authv3.CheckRequest{
		Attributes: &authv3.AttributeContext{
			Request: &authv3.AttributeContext_Request{
				Http: &authv3.AttributeContext_HttpRequest{
					Headers: headers,
				},
			},
		},
	}
}

// ============================================================================
// Public routes — no auth required
// ============================================================================

func makeCheckRequestWithPath(path string, headers map[string]string) *authv3.CheckRequest {
	return &authv3.CheckRequest{
		Attributes: &authv3.AttributeContext{
			Request: &authv3.AttributeContext_Request{
				Http: &authv3.AttributeContext_HttpRequest{
					Headers: headers,
					Path:    path,
				},
			},
		},
	}
}

func TestCheck_PublicPath_AuthEndpoint(t *testing.T) {
	resp, err := testSidecar.Check(testCtx,
		makeCheckRequestWithPath("/v1/auth/authenticate", map[string]string{}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "login must be publicly reachable")
}

func TestCheck_PublicPath_Health(t *testing.T) {
	resp, err := testSidecar.Check(testCtx,
		makeCheckRequestWithPath("/health", map[string]string{}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())
}

func TestCheck_NoAuth_ProtectedRoute_Denied(t *testing.T) {
	resp, err := testSidecar.Check(testCtx,
		makeCheckRequestWithPath("/v1/users", map[string]string{}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "protected routes must reject missing auth")
}

// ============================================================================
// JWT path tests
// ============================================================================

func TestCheck_JWTAuth(t *testing.T) {
	// Authenticate to get a JWT
	authResp, err := testAuthClient.Authenticate(testCtx, &apigen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google_jwt_test",
		ProviderEmail: "jwt-test@example.com",
		EmailVerified: true,
		Profile:       map[string]string{"name": "JWT Tester"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, authResp.AccessToken)
	require.NotEmpty(t, authResp.RefreshToken)
	require.Equal(t, int64(180), authResp.ExpiresIn)
	require.NotEmpty(t, authResp.User.Uuid)

	// Use the JWT against the sidecar on a protected path.
	resp, err := testSidecar.Check(testCtx, makeCheckRequestWithPath("/v1/users", map[string]string{
		"authorization": "Bearer " + authResp.AccessToken,
	}))
	require.NoError(t, err)

	okResp := resp.GetOkResponse()
	require.NotNil(t, okResp, "should allow valid JWT")

	headerMap := make(map[string]string)
	for _, h := range okResp.Headers {
		headerMap[h.Header.Key] = h.Header.Value
	}

	require.Equal(t, authResp.User.Uuid, headerMap["x-user-id"])
	require.NotEmpty(t, headerMap["x-session-id"], "session id must be forwarded for audit correlation")
	// org id may be empty on a brand-new signup; org role and platform role depend on provisioning path
}

func TestCheck_ExpiredJWT(t *testing.T) {
	// Send a clearly invalid/expired JWT
	resp, err := testSidecar.Check(testCtx, makeCheckRequestWithPath("/v1/users", map[string]string{
		"authorization": "Bearer eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJ0ZXN0IiwiZXhwIjoxfQ.invalid",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "should deny expired/invalid JWT")
	require.Equal(t, "invalid or expired token", resp.GetDeniedResponse().Body)
}

func TestCheck_InvalidJWT(t *testing.T) {
	resp, err := testSidecar.Check(testCtx, makeCheckRequest(map[string]string{
		"authorization": "Bearer not.a.jwt",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "should deny invalid JWT")
}

// ============================================================================
// Auth flow tests (authenticate → refresh → logout)
// ============================================================================

func TestAuth_RefreshToken(t *testing.T) {
	authResp, err := testAuthClient.Authenticate(testCtx, &apigen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google_refresh_test",
		ProviderEmail: "refresh@example.com",
	})
	require.NoError(t, err)

	// Refresh
	refreshResp, err := testAuthClient.RefreshToken(testCtx, &apigen.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.NoError(t, err)
	require.NotEmpty(t, refreshResp.AccessToken)
	require.NotEmpty(t, refreshResp.RefreshToken)
	require.NotEqual(t, authResp.RefreshToken, refreshResp.RefreshToken, "should rotate refresh token")
	require.NotEqual(t, authResp.AccessToken, refreshResp.AccessToken, "should issue new access token")

	// Old refresh token should no longer work (consumed)
	_, err = testAuthClient.RefreshToken(testCtx, &apigen.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.Error(t, err, "old refresh token should be rejected (reuse detection)")
}

func TestAuth_Logout(t *testing.T) {
	authResp, err := testAuthClient.Authenticate(testCtx, &apigen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google_logout_test",
		ProviderEmail: "logout@example.com",
	})
	require.NoError(t, err)

	// Logout
	_, err = testAuthClient.Logout(testCtx, &apigen.LogoutRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.NoError(t, err)

	// Refresh should fail after logout
	_, err = testAuthClient.RefreshToken(testCtx, &apigen.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.Error(t, err, "refresh should fail after logout")
}

// End-to-end revocation across the real accounts + Redis stack, in the exact
// gateway sequence: protected RPC authorized → logout request authorized (the
// sidecar drops its cached answer for the jti here) → accounts revokes the
// jti in the shared Redis set → immediate reuse of the old access token is
// rejected. Pins the cross-service "revoked-jti:" key contract between
// accounts' cache.TokenRevoker and the sidecar's revoker.
func TestCheck_RevokedAccessToken_RejectedOnGatewayPath(t *testing.T) {
	// The integration graph runs IDENTITY_PROVIDER=fixture with the dev-admin
	// fixture seeded; "dev-bob" is one of its allowlisted login tokens.
	authResp, err := testAuthClient.Authenticate(testCtx, &apigen.AuthenticateRequest{
		Provider: "email",
		Authentication: &apigen.AuthenticateRequest_Fixture{
			Fixture: &apigen.FixtureAuthentication{Token: "dev-bob"},
		},
	})
	require.NoError(t, err)

	bearer := map[string]string{"authorization": "Bearer " + authResp.AccessToken}

	resp, err := testSidecar.Check(testCtx, makeCheckRequestWithPath("/v1/users", bearer))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "protected RPC succeeds before logout")

	// The gateway authorizes the logout request before proxying it upstream.
	resp, err = testSidecar.Check(testCtx, makeCheckRequestWithPath("/v1/auth/logout", bearer))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "logout request is authorized")

	// Accounts revokes the refresh family AND the access token's jti (read
	// from the Authorization metadata, as the gateway forwards it).
	logoutCtx := metadata.AppendToOutgoingContext(testCtx,
		"authorization", "Bearer "+authResp.AccessToken)
	_, err = testAuthClient.Logout(logoutCtx, &apigen.LogoutRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.NoError(t, err)

	resp, err = testSidecar.Check(testCtx, makeCheckRequestWithPath("/v1/users", bearer))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "revoked access token must be rejected immediately")
	require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
	require.Equal(t, "token revoked", resp.GetDeniedResponse().Body)
}

func TestAuth_GetJWKS(t *testing.T) {
	resp, err := testAuthClient.GetJWKS(testCtx, &emptypb.Empty{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.KeysJson)

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	err = json.Unmarshal([]byte(resp.KeysJson), &jwks)
	require.NoError(t, err)
	require.Len(t, jwks.Keys, 1)
	require.Equal(t, "OKP", jwks.Keys[0].Kty)
	require.Equal(t, "Ed25519", jwks.Keys[0].Crv)
	require.Equal(t, "EdDSA", jwks.Keys[0].Alg)
	require.Equal(t, "sig", jwks.Keys[0].Use)
	require.NotEmpty(t, jwks.Keys[0].Kid)
	require.NotEmpty(t, jwks.Keys[0].X)
}
