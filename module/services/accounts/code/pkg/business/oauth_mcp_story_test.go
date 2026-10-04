//go:build !pure

package business_test

import (
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// The MCP client's whole path, end to end against the real database: a client
// that registered itself by publishing a metadata document, a loopback redirect
// on an ephemeral port, a resource indicator, consent, the code, the token, and
// the audience the token ends up carrying.
//
// The story the issue asks for. Everything in it is a thing that could be built
// separately and still not work together — the registry fallback, the loopback
// port rule, the code's resource binding, the audience on the minted token, and
// the rotation that must reissue it.

const mcpClientID = "https://claude.ai/oauth/claude-code-client-metadata"

const mcpClientDocument = `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","client_name":"Claude Code","client_uri":"https://claude.ai","redirect_uris":["http://localhost/callback","http://127.0.0.1/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`

const mcpHostOrigin = "https://host.example.com"

// admitMetadataClient wires the published document as a resolved client for the
// duration of one test, without a network fetch. The fetch itself — its bounds,
// its SSRF guard, its cache — is covered in pkg/auth; what this story needs is
// the document's effect on the flow.
func admitMetadataClient(t *testing.T) {
	t.Helper()
	policy, err := auth.NewClientMetadataPolicy("any")
	require.NoError(t, err)
	// The document goes through every validation rule; only the HTTP fetch is
	// stood in for. Its bounds, SSRF guard and cache are covered in pkg/auth.
	testService.SetClientMetadataResolver(auth.NewClientMetadataResolverWith(policy,
		auth.StaticMetadataDocuments{mcpClientID: mcpClientDocument}))
	t.Cleanup(func() { testService.SetClientMetadataResolver(nil) })
}

func mcpAuthorizationRequest() business.OAuthAuthorizationRequest {
	return business.OAuthAuthorizationRequest{
		ResponseType: "code",
		ClientID:     mcpClientID,
		// The ephemeral loopback port Claude Code actually listens on. The
		// document names no port; RFC 8252 §7.3 is what makes this match.
		RedirectURI:         "http://localhost:54321/callback",
		CodeChallenge:       testCodeChallenge(),
		CodeChallengeMethod: "S256",
		State:               "opaque-state",
		Resource:            mcpHostOrigin + "/solutions/example/mcp",
	}
}

func TestStory_HOST_MCP_001(t *testing.T) {
	clearData(t)
	admitMetadataClient(t)

	// The authorization endpoint validates everything before any sign-in UI.
	// The browser is still on the host and nothing has been typed.
	publicCtx, err := auth.WithVerifiedPublicOrigin(testCtx, mcpHostOrigin)
	require.NoError(t, err)

	resolved, err := testService.ResolveOAuthAuthorization(publicCtx, mcpAuthorizationRequest())
	require.NoError(t, err)
	require.Equal(t, "Claude Code", resolved.Client.Name)
	require.True(t, resolved.Client.Metadata, "nothing durable was written for this client")
	require.Equal(t, "example", resolved.Resource.SolutionID)
	require.True(t, resolved.RequiresConsent,
		"a client the operator never declared, narrowing to a named resource, is the person's decision")

	// The person signs in on the host's own login page and approves. The code is
	// issued from THEIR session, so it names an identity the client never saw
	// authenticate.
	signedIn, hostSession := signInAsClientUser(t, "google-mcp-flow", "mcp-flow@test.com")
	signedInWithOrigin, err := auth.WithVerifiedPublicOrigin(signedIn, mcpHostOrigin)
	require.NoError(t, err)

	code, expiresIn, err := testService.GrantOAuthAuthorization(
		signedInWithOrigin, mcpAuthorizationRequest())
	require.NoError(t, err)
	require.NotEmpty(t, code)
	require.Positive(t, expiresIn)

	// The token endpoint. Form encoding is the adapter's business; what matters
	// here is the response shape a standard OAuth library reads, and the
	// audience the access token carries.
	tokens, err := testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "authorization_code",
		ClientID:     mcpClientID,
		Code:         code,
		RedirectURI:  "http://localhost:54321/callback",
		CodeVerifier: testCodeVerifier,
		Resource:     mcpHostOrigin + "/solutions/example/mcp",
	})
	require.NoError(t, err)
	require.Equal(t, "Bearer", tokens.TokenType, "RFC 6749 §5.1 requires it")
	require.NotEmpty(t, tokens.AccessToken)
	require.NotEmpty(t, tokens.RefreshToken)
	require.Equal(t, "offline_access", tokens.Scope)
	require.Positive(t, tokens.ExpiresIn)

	// The token is a SESSION credential for the person, bound to the resource.
	identity, err := testService.JWTMinter().VerifyAccess(tokens.AccessToken)
	require.NoError(t, err)
	require.Equal(t, mcpClientID, identity.ClientID, "azp names the client")
	require.Equal(t, hostSession.User.Uuid, identity.UserID.String(), "the subject is the person")
	require.NotEmpty(t, identity.SessionID, "a session id is what lets the SDK mint a Work Context")
	require.Equal(t, mcpHostOrigin+"/solutions/example/mcp", identity.Resource)

	// Rotation keeps the audience. A rotation that re-read it from the request
	// would let the client move its own binding by asking.
	rotated, err := testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "refresh_token",
		ClientID:     mcpClientID,
		RefreshToken: tokens.RefreshToken,
	})
	require.NoError(t, err)
	require.NotEqual(t, tokens.RefreshToken, rotated.RefreshToken, "the refresh half rotates")
	rotatedIdentity, err := testService.JWTMinter().VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, mcpHostOrigin+"/solutions/example/mcp", rotatedIdentity.Resource)

	// Revocation follows the registered-client tokens: the person's own browser
	// session is untouched by any of it.
	_, err = testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{RefreshToken: hostSession.RefreshToken})
	require.NoError(t, err, "authorizing an MCP client must not consume the host session")
}

// A repeated `resource` at the token endpoint must be the one the
// authorization granted. Naming a different one cannot widen what the person
// approved — and because the code is consumed before the check, the attempt
// burns it rather than leaving it to be tried again with another resource.
func TestAnMCPCodeCannotBeRedeemedForADifferentResource(t *testing.T) {
	clearData(t)
	admitMetadataClient(t)

	signedIn, _ := signInAsClientUser(t, "google-mcp-swap", "mcp-swap@test.com")
	signedInWithOrigin, err := auth.WithVerifiedPublicOrigin(signedIn, mcpHostOrigin)
	require.NoError(t, err)

	code, _, err := testService.GrantOAuthAuthorization(
		signedInWithOrigin, mcpAuthorizationRequest())
	require.NoError(t, err)

	_, err = testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "authorization_code",
		ClientID:     mcpClientID,
		Code:         code,
		RedirectURI:  "http://localhost:54321/callback",
		CodeVerifier: testCodeVerifier,
		Resource:     mcpHostOrigin + "/solutions/audit/mcp",
	})
	require.Error(t, err)

	// And the code is spent, not left live for a second attempt.
	_, err = testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "authorization_code",
		ClientID:     mcpClientID,
		Code:         code,
		RedirectURI:  "http://localhost:54321/callback",
		CodeVerifier: testCodeVerifier,
		Resource:     mcpHostOrigin + "/solutions/example/mcp",
	})
	require.Error(t, err)
}

// A client naming the wrong resource on refresh is refused as invalid_target
// and keeps the session it legitimately holds. The refusal happens on the
// locked session row, before the token is consumed.
func TestAnMCPRefreshNamingAnotherResourceKeepsTheSession(t *testing.T) {
	clearData(t)
	admitMetadataClient(t)

	signedIn, _ := signInAsClientUser(t, "google-mcp-refresh", "mcp-refresh@test.com")
	signedInWithOrigin, err := auth.WithVerifiedPublicOrigin(signedIn, mcpHostOrigin)
	require.NoError(t, err)
	code, _, err := testService.GrantOAuthAuthorization(
		signedInWithOrigin, mcpAuthorizationRequest())
	require.NoError(t, err)

	tokens, err := testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "authorization_code",
		ClientID:     mcpClientID,
		Code:         code,
		RedirectURI:  "http://localhost:54321/callback",
		CodeVerifier: testCodeVerifier,
	})
	require.NoError(t, err)

	_, err = testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "refresh_token",
		ClientID:     mcpClientID,
		RefreshToken: tokens.RefreshToken,
		Resource:     mcpHostOrigin + "/solutions/other/mcp",
	})
	require.Error(t, err)

	// Still usable: a client told `invalid_target` has lost nothing.
	rotated, err := testService.ExchangeOAuthToken(testCtx, business.OAuthTokenRequest{
		GrantType:    "refresh_token",
		ClientID:     mcpClientID,
		RefreshToken: tokens.RefreshToken,
		Resource:     mcpHostOrigin + "/solutions/example/mcp",
	})
	require.NoError(t, err)
	require.NotEmpty(t, rotated.AccessToken)
}

// A loopback redirect on a port the document did not name is admitted; one with
// a different path is not. The rule applies to the presented port only, never to
// the path, which is what keeps a local attacker from registering a listener on
// a path the client never asked to be returned to.
func TestAnMCPRedirectMustStillMatchItsPath(t *testing.T) {
	clearData(t)
	admitMetadataClient(t)

	publicCtx, err := auth.WithVerifiedPublicOrigin(testCtx, mcpHostOrigin)
	require.NoError(t, err)

	request := mcpAuthorizationRequest()
	request.RedirectURI = "http://localhost:54321/stolen"
	_, err = testService.ResolveOAuthAuthorization(publicCtx, request)
	require.Error(t, err)

	request.RedirectURI = "http://127.0.0.1:1/callback"
	_, err = testService.ResolveOAuthAuthorization(publicCtx, request)
	require.NoError(t, err)
}

// An impersonated session may not hand a client a credential: the client would
// hold a rotatable token for a person who never authorized it, and it would
// outlive the window, which is capped precisely so nothing rotatable does. The
// standard authorize endpoint inherits that rule from the shared code issuer
// rather than restating it, so this is the test that the inheritance is real.
func TestAnImpersonatedSessionCannotAuthorizeAnMCPClient(t *testing.T) {
	clearData(t)
	admitMetadataClient(t)
	fixture := seedImpersonationFixture(t, "mcp")

	issued, err := testService.ImpersonateUser(testCtx, fixture.supportID,
		&gen.ImpersonateUserRequest{UserId: fixture.memberID, Reason: impersonationReason})
	require.NoError(t, err)
	identity, err := testService.JWTMinter().VerifyAccess(issued.AccessToken)
	require.NoError(t, err)
	impersonating := auth.WithVerifiedRequestIdentity(testCtx, auth.RequestIdentityOf(identity))
	impersonating, err = auth.WithVerifiedPublicOrigin(impersonating, mcpHostOrigin)
	require.NoError(t, err)

	_, _, err = testService.GrantOAuthAuthorization(impersonating, mcpAuthorizationRequest())
	require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)
}

// And a caller with no verified session at all. The request itself is valid —
// the client resolves, the resource resolves — so the refusal can only come
// from the code issuer having nobody to bind the code to.
func TestAnUnauthenticatedCallerCannotAuthorizeAnMCPClient(t *testing.T) {
	clearData(t)
	admitMetadataClient(t)

	publicCtx, err := auth.WithVerifiedPublicOrigin(testCtx, mcpHostOrigin)
	require.NoError(t, err)
	_, err = testService.ResolveOAuthAuthorization(publicCtx, mcpAuthorizationRequest())
	require.NoError(t, err, "the request is well formed; only the caller is missing")

	_, _, err = testService.GrantOAuthAuthorization(publicCtx, mcpAuthorizationRequest())
	require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)
}
