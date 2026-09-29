//go:build integration
// +build integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	testExtAuthz   *ExtAuthz
	testAuthClient apigen.AuthServiceClient
	testCtx        context.Context
)

func TestMain(m *testing.M) {
	os.Exit(runExtAuthzIntegrationTests(m))
}

func runExtAuthzIntegrationTests(m *testing.M) int {
	// The accounts transport deliberately fails closed for internal RPCs.
	// WithDependencies inherits this process environment, so both services use
	// the same integration-only credential.
	_ = os.Setenv("CODEFLY_INTERNAL_TOKEN", "integration-test-internal-token")
	_ = os.Setenv("CODEFLY_GATEWAY_TOKEN", "integration-test-gateway-token")
	ctx := context.Background()

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("ext-authz-test"),
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

	// Same wiring as main: verification keys are read by kid from the JWKS
	// accounts publishes, so this exercises the real fetch/cache path rather
	// than a single key pinned at boot. accounts may still be starting, so warm
	// with a bounded retry.
	accessKeySet := newAccessJWKS(fmt.Sprintf("http://%s:%d", internalNet.Hostname, internalNet.Port))
	// Same lifetime as main: the warm loop runs for the whole process, so the
	// suite exercises a gateway whose key set is kept current rather than one
	// warmed once and then left to age.
	go keepAccessKeysWarm(ctx, accessKeySet)
	for i := 0; i < 60 && !accessKeySet.loaded(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !accessKeySet.loaded() {
		fmt.Fprintf(os.Stderr, "access-token JWKS never became available\n")
		return 1
	}
	testExtAuthz = NewExtAuthz(internalConn, accessKeySet)

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
	testExtAuthz.SetRevoker(revoker)

	testAuthClient = apigen.NewAuthServiceClient(apiConn)
	testCtx = ctx

	return m.Run()
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

// Check answers on the credential, never on the path: a request with no
// credential is denied whatever it asks for, and the Gateway's public branch
// owns the decision to forward such a request anyway, without identity. These
// two pin that division, because a Check that started admitting paths would hand
// every public route an unauthenticated OK with a stamped identity header set.
//
// Public REACHABILITY is therefore not asserted here — it is a Gateway-layer
// property, proven end to end by TestIntegration_Gateway_LoginPath_NoToken_Allowed,
// which logs in through the real gateway carrying no token at all.
func TestCheck_PublicPath_AuthEndpoint(t *testing.T) {
	resp, err := testExtAuthz.Check(testCtx,
		makeCheckRequestWithPath("/v1/auth/authenticate", map[string]string{}))
	require.NoError(t, err)
	requireCredentiallessDenial(t, resp, "the login path is not an exception to Check's credential rule")
}

func TestCheck_PublicPath_Health(t *testing.T) {
	resp, err := testExtAuthz.Check(testCtx,
		makeCheckRequestWithPath("/health", map[string]string{}))
	require.NoError(t, err)
	requireCredentiallessDenial(t, resp, "health is not an exception to Check's credential rule")
}

// requireCredentiallessDenial asserts Check refused, with 401, and stamped no
// identity at all — a denial that still carried identity headers would be worse
// than an allow, because the Gateway forwards a credential-less public request
// after the denial.
func requireCredentiallessDenial(t *testing.T, resp *authv3.CheckResponse, why string) {
	t.Helper()
	require.Nil(t, resp.GetOkResponse(), why)
	denied := resp.GetDeniedResponse()
	require.NotNil(t, denied, why)
	require.Equal(t, 401, int(denied.GetStatus().GetCode()), "credential-less requests are refused as unauthenticated")
	require.Empty(t, denied.GetHeaders(), "a denial must not stamp identity headers")
}

func TestCheck_NoAuth_ProtectedRoute_Denied(t *testing.T) {
	resp, err := testExtAuthz.Check(testCtx,
		makeCheckRequestWithPath("/v1/users", map[string]string{}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "protected routes must reject missing auth")
}

// ============================================================================
// JWT path tests
// ============================================================================

func TestCheck_JWTAuth(t *testing.T) {
	// Authenticate to get a JWT. provider_id / provider_email are deprecated
	// and Authenticate ignores them (authentication.proto), so the identity
	// comes from the seeded fixture token the required `authentication` oneof
	// carries. Each test here logs in as a different fixture identity so a
	// logout or refresh-reuse assertion cannot revoke another test's session.
	authResp, err := testAuthClient.Authenticate(testCtx, &apigen.AuthenticateRequest{
		Provider: "email",
		Authentication: &apigen.AuthenticateRequest_Fixture{
			Fixture: &apigen.FixtureAuthentication{Token: "dev-alice"},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, authResp.AccessToken)
	// This is the direct gRPC Authenticate, so the refresh token is in the
	// response. The REST surface deliberately moves it into an httpOnly cookie
	// instead (adapters/rest_extras.go); that contract is asserted by the
	// gateway suite, not here.
	require.NotEmpty(t, authResp.RefreshToken)
	requireExpiresInMatchesSignedTTL(t, authResp.AccessToken, authResp.ExpiresIn)
	require.NotEmpty(t, authResp.User.Uuid)

	// Use the JWT against the ext_authz check on a protected path.
	resp, err := testExtAuthz.Check(testCtx, makeCheckRequestWithPath("/v1/users", map[string]string{
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
	resp, err := testExtAuthz.Check(testCtx, makeCheckRequestWithPath("/v1/users", map[string]string{
		"authorization": "Bearer eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJ0ZXN0IiwiZXhwIjoxfQ.invalid",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "should deny expired/invalid JWT")
	require.Equal(t, "invalid or expired token", resp.GetDeniedResponse().Body)
}

func TestCheck_InvalidJWT(t *testing.T) {
	resp, err := testExtAuthz.Check(testCtx, makeCheckRequest(map[string]string{
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
		Provider: "email",
		Authentication: &apigen.AuthenticateRequest_Fixture{
			Fixture: &apigen.FixtureAuthentication{Token: "dev-carol"},
		},
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
		Provider: "email",
		Authentication: &apigen.AuthenticateRequest_Fixture{
			Fixture: &apigen.FixtureAuthentication{Token: "dev-dana"},
		},
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
// ext_authz check drops its cached answer for the jti here) → accounts revokes the
// jti in the shared Redis set → immediate reuse of the old access token is
// rejected. Pins the cross-service "revoked-jti:" key contract between
// accounts' cache.TokenRevoker and the ext_authz check's revoker.
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

	resp, err := testExtAuthz.Check(testCtx, makeCheckRequestWithPath("/v1/users", bearer))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "protected RPC succeeds before logout")

	// The gateway authorizes the logout request before proxying it upstream.
	resp, err = testExtAuthz.Check(testCtx, makeCheckRequestWithPath("/v1/auth/logout", bearer))
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

	resp, err = testExtAuthz.Check(testCtx, makeCheckRequestWithPath("/v1/users", bearer))
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

// requireExpiresInMatchesSignedTTL holds expires_in to the token actually
// signed, rather than to a constant. expires_in is derived from the signed TTL
// and is whole seconds, so a literal `180` fails the moment a second elapses
// between minting and the assertion — which is a clock artefact, not a defect.
//
// The signed lifetime (exp - iat) is exact and carries no clock dependence at
// all, so it is asserted exactly. expires_in is then the remaining lifetime,
// which may only have lost the time the call itself took: it must not exceed the
// signed lifetime, and must not have lost more than allowance. Nothing sleeps or
// retries to make this pass.
func requireExpiresInMatchesSignedTTL(t *testing.T, accessToken string, expiresIn int64) {
	t.Helper()
	const allowance = 30 * time.Second

	parts := strings.Split(accessToken, ".")
	require.Len(t, parts, 3, "access token is a three-part JWS")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "access token payload is base64url")
	var claims struct {
		Exp int64 `json:"exp"`
		Iat int64 `json:"iat"`
	}
	require.NoError(t, json.Unmarshal(raw, &claims))
	require.NotZero(t, claims.Exp, "access token carries exp")
	require.NotZero(t, claims.Iat, "access token carries iat")

	signedTTL := claims.Exp - claims.Iat
	require.Equal(t, int64(accessTokenSignedTTLSeconds), signedTTL,
		"the signed lifetime is exact and independent of any clock here")

	require.LessOrEqual(t, expiresIn, signedTTL,
		"expires_in cannot exceed the lifetime actually signed")
	require.GreaterOrEqual(t, expiresIn, signedTTL-int64(allowance.Seconds()),
		"expires_in should differ from the signed lifetime only by the time this call took")
}

// accessTokenSignedTTLSeconds is the access-token lifetime accounts signs. It is
// the contract this suite pins; expires_in is measured against it above rather
// than compared to it directly.
const accessTokenSignedTTLSeconds = 180
