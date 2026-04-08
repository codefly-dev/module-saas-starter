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
	"google.golang.org/protobuf/types/known/emptypb"

	codefly "github.com/codefly-dev/sdk-go"

	"github.com/codefly-dev/core/sdk"
	"github.com/stretchr/testify/require"

	backend "backend/pkg/gen"
)

// Global test fixtures — initialized once in TestMain.
var (
	testSidecar    *Sidecar
	testUserClient backend.UserServiceClient
	testAuthClient backend.AuthServiceClient
	testCtx        context.Context
	testCleanup    func()
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("sidecar-test"),
		sdk.WithTimeout(90*time.Second),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies failed: %v\n", err)
		os.Exit(1)
	}

	_, err = codefly.Init(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init failed: %v\n", err)
		os.Exit(1)
	}

	backendNet := codefly.For(ctx).Service("backend").API("grpc").NetworkInstance()
	if backendNet == nil {
		fmt.Fprintf(os.Stderr, "backend gRPC endpoint not available\n")
		os.Exit(1)
	}
	backendAddr := fmt.Sprintf("%s:%d", backendNet.Hostname, backendNet.Port)

	backendConn, err := grpc.NewClient(backendAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to backend: %v\n", err)
		os.Exit(1)
	}

	// Fetch public key from backend's JWKS endpoint
	publicKey := fetchTestPublicKey(ctx, backendConn)

	testSidecar = NewSidecar(backendConn, publicKey)
	testUserClient = backend.NewUserServiceClient(backendConn)
	testAuthClient = backend.NewAuthServiceClient(backendConn)
	testCtx = ctx

	testCleanup = func() {
		backendConn.Close()
		deps.Destroy(ctx)
	}

	code := m.Run()
	testCleanup()
	os.Exit(code)
}

func fetchTestPublicKey(ctx context.Context, conn *grpc.ClientConn) ed25519.PublicKey {
	client := backend.NewAuthServiceClient(conn)

	// Retry — backend may still be starting
	var resp *backend.JWKSResponse
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
// Public endpoint (no auth headers → pass through)
// ============================================================================

func TestCheck_NoAuthHeaders(t *testing.T) {
	resp, err := testSidecar.Check(testCtx, makeCheckRequest(map[string]string{
		"content-type": "application/json",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "should pass through unauthenticated requests")
}

// ============================================================================
// JWT path tests
// ============================================================================

func TestCheck_JWTAuth(t *testing.T) {
	// Authenticate to get a JWT
	authResp, err := testAuthClient.Authenticate(testCtx, &backend.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google_jwt_test",
		ProviderEmail: "jwt-test@example.com",
		EmailVerified: true,
		Profile:       map[string]string{"name": "JWT Tester"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, authResp.AccessToken)
	require.NotEmpty(t, authResp.RefreshToken)
	require.Equal(t, int64(900), authResp.ExpiresIn)
	require.NotEmpty(t, authResp.User.Uuid)

	// Use the JWT against the sidecar
	resp, err := testSidecar.Check(testCtx, makeCheckRequest(map[string]string{
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
	require.NotEmpty(t, headerMap["x-org-id"])
	require.Contains(t, headerMap["x-roles"], "admin")
}

func TestCheck_ExpiredJWT(t *testing.T) {
	// Send a clearly invalid/expired JWT
	resp, err := testSidecar.Check(testCtx, makeCheckRequest(map[string]string{
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
	authResp, err := testAuthClient.Authenticate(testCtx, &backend.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google_refresh_test",
		ProviderEmail: "refresh@example.com",
	})
	require.NoError(t, err)

	// Refresh
	refreshResp, err := testAuthClient.RefreshToken(testCtx, &backend.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.NoError(t, err)
	require.NotEmpty(t, refreshResp.AccessToken)
	require.NotEmpty(t, refreshResp.RefreshToken)
	require.NotEqual(t, authResp.RefreshToken, refreshResp.RefreshToken, "should rotate refresh token")
	require.NotEqual(t, authResp.AccessToken, refreshResp.AccessToken, "should issue new access token")

	// Old refresh token should no longer work (consumed)
	_, err = testAuthClient.RefreshToken(testCtx, &backend.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.Error(t, err, "old refresh token should be rejected (reuse detection)")
}

func TestAuth_Logout(t *testing.T) {
	authResp, err := testAuthClient.Authenticate(testCtx, &backend.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google_logout_test",
		ProviderEmail: "logout@example.com",
	})
	require.NoError(t, err)

	// Logout
	_, err = testAuthClient.Logout(testCtx, &backend.LogoutRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.NoError(t, err)

	// Refresh should fail after logout
	_, err = testAuthClient.RefreshToken(testCtx, &backend.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.Error(t, err, "refresh should fail after logout")
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
