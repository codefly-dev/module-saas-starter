//go:build !pure

package business_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

const (
	testClientID      = "example-addin"
	testRedirectURI   = "https://localhost:3000/auth/callback"
	testCodeVerifier  = "ZmFrZS12ZXJpZmllci10aGF0LWlzLWxvbmctZW5vdWdoLTAxMjM0NTY3ODk"
	testOtherRedirect = "https://addin.example.com/auth/callback"
)

func testCodeChallenge() string {
	digest := sha256.Sum256([]byte(testCodeVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// registerTestClients declares two clients for the duration of one test and
// restores the empty registry afterwards, so no other test inherits them.
func registerTestClients(t *testing.T) {
	t.Helper()
	registry, err := auth.NewClientRegistry(`[
		{"client_id": "example-addin", "name": "Example Add-in", "kind": "public",
		 "redirect_uris": ["https://localhost:3000/auth/callback", "https://addin.example.com/auth/callback"],
		 "origins": ["https://addin.example.com"]},
		{"client_id": "example-cli", "name": "Example CLI",
		 "redirect_uris": ["http://localhost:7890/callback"]}
	]`)
	require.NoError(t, err)
	testService.SetClientRegistry(registry)
	t.Cleanup(func() {
		empty, err := auth.NewClientRegistry("")
		require.NoError(t, err)
		testService.SetClientRegistry(empty)
	})
}

func authorizationRequest() *gen.ClientAuthorizationRequest {
	return &gen.ClientAuthorizationRequest{
		ClientId:            testClientID,
		RedirectUri:         testRedirectURI,
		CodeChallenge:       testCodeChallenge(),
		CodeChallengeMethod: "S256",
	}
}

// signInAsClientUser runs the host's own sign-in and returns a context carrying
// the verified identity the login page would be calling with.
func signInAsClientUser(t *testing.T, providerID, email string) (context.Context, *gen.AuthenticateResponse) {
	t.Helper()
	resp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: providerID, ProviderEmail: email,
	})
	require.NoError(t, err)
	identity, err := testService.JWTMinter().VerifyAccess(resp.AccessToken)
	require.NoError(t, err)
	return auth.WithVerifiedRequestIdentity(testCtx, auth.RequestIdentityOf(identity)), resp
}

// A registered client signs in through the host: the whole story, end to end
// against the real database.
func TestStory_HOST_CLIENT_001(t *testing.T) {
	clearData(t)
	registerTestClients(t)

	// The login page validates before it renders anything.
	validated, err := testService.ValidateClientAuthorization(testCtx,
		&gen.ValidateClientAuthorizationRequest{Authorization: authorizationRequest()})
	require.NoError(t, err)
	require.Equal(t, "Example Add-in", validated.ClientName)

	signedIn, hostSession := signInAsClientUser(t, "google-client-flow", "client-flow@test.com")

	issued, err := testService.IssueClientAuthorizationCode(signedIn,
		&gen.IssueClientAuthorizationCodeRequest{Authorization: authorizationRequest()})
	require.NoError(t, err)
	require.NotEmpty(t, issued.Code)
	require.Positive(t, issued.ExpiresIn)

	// The client exchanges the code for its own tokens. They come back in the
	// body, and the client never saw the person authenticate.
	exchanged, err := testService.ExchangeClientToken(testCtx, &gen.ExchangeClientTokenRequest{
		ClientId: testClientID,
		Grant: &gen.ExchangeClientTokenRequest_AuthorizationCode{
			AuthorizationCode: &gen.AuthorizationCodeGrant{
				Code:         issued.Code,
				RedirectUri:  testRedirectURI,
				CodeVerifier: testCodeVerifier,
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, exchanged.AccessToken)
	require.NotEmpty(t, exchanged.RefreshToken)
	require.Positive(t, exchanged.ExpiresIn)

	clientIdentity, err := testService.JWTMinter().VerifyAccess(exchanged.AccessToken)
	require.NoError(t, err)
	require.Equal(t, testClientID, clientIdentity.ClientID, "the token names the client in azp")
	require.Equal(t, hostSession.User.Uuid, clientIdentity.UserID.String(), "the subject is the person")

	// The refresh half rotates and stays bound to this client.
	rotated, err := testService.ExchangeClientToken(testCtx, &gen.ExchangeClientTokenRequest{
		ClientId: testClientID,
		Grant: &gen.ExchangeClientTokenRequest_RefreshToken{
			RefreshToken: &gen.ClientRefreshTokenGrant{RefreshToken: exchanged.RefreshToken},
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, exchanged.RefreshToken, rotated.RefreshToken)
	rotatedIdentity, err := testService.JWTMinter().VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, testClientID, rotatedIdentity.ClientID)

	// And the person's own browser session is untouched by all of it.
	_, err = testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{RefreshToken: hostSession.RefreshToken})
	require.NoError(t, err, "authorizing a client must not consume the host session")
}

// "And a redirect URI not registered for that client is refused" — before any
// sign-in UI, which is what makes ValidateClientAuthorization public.
func TestUnregisteredClientAuthorizationIsRefusedBeforeSignIn(t *testing.T) {
	clearData(t)
	registerTestClients(t)

	signedIn, _ := signInAsClientUser(t, "google-refusals", "refusals@test.com")

	for name, mutate := range map[string]func(*gen.ClientAuthorizationRequest){
		"unknown client":           func(r *gen.ClientAuthorizationRequest) { r.ClientId = "nobody" },
		"another client's URI":     func(r *gen.ClientAuthorizationRequest) { r.RedirectUri = "http://localhost:7890/callback" },
		"unregistered URI":         func(r *gen.ClientAuthorizationRequest) { r.RedirectUri = "https://evil.test/auth/callback" },
		"registered URI with path": func(r *gen.ClientAuthorizationRequest) { r.RedirectUri = testRedirectURI + "/extra" },
		"plain PKCE":               func(r *gen.ClientAuthorizationRequest) { r.CodeChallengeMethod = "plain" },
		"no challenge":             func(r *gen.ClientAuthorizationRequest) { r.CodeChallenge = "" },
	} {
		t.Run(name, func(t *testing.T) {
			request := authorizationRequest()
			mutate(request)
			_, err := testService.ValidateClientAuthorization(testCtx,
				&gen.ValidateClientAuthorizationRequest{Authorization: request})
			require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)

			// The same refusal applies after sign-in: a caller that skipped the
			// validation step gets no code either.
			_, err = testService.IssueClientAuthorizationCode(signedIn,
				&gen.IssueClientAuthorizationCodeRequest{Authorization: request})
			require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)
		})
	}
}

func TestClientAuthorizationCodeIsSingleUseAndBoundToItsRequest(t *testing.T) {
	clearData(t)
	registerTestClients(t)
	signedIn, _ := signInAsClientUser(t, "google-code-binding", "code-binding@test.com")

	redeem := func(code, redirectURI, verifier, clientID string) error {
		_, err := testService.ExchangeClientToken(testCtx, &gen.ExchangeClientTokenRequest{
			ClientId: clientID,
			Grant: &gen.ExchangeClientTokenRequest_AuthorizationCode{
				AuthorizationCode: &gen.AuthorizationCodeGrant{
					Code: code, RedirectUri: redirectURI, CodeVerifier: verifier,
				},
			},
		})
		return err
	}
	issue := func(t *testing.T) string {
		t.Helper()
		issued, err := testService.IssueClientAuthorizationCode(signedIn,
			&gen.IssueClientAuthorizationCodeRequest{Authorization: authorizationRequest()})
		require.NoError(t, err)
		return issued.Code
	}

	t.Run("a wrong verifier is refused and spends the code", func(t *testing.T) {
		code := issue(t)
		require.ErrorIs(t,
			redeem(code, testRedirectURI, "not-the-verifier-but-long-enough-0123456789abcdef", testClientID),
			auth.ErrClientAuthorizationRejected)
		require.ErrorIs(t, redeem(code, testRedirectURI, testCodeVerifier, testClientID),
			auth.ErrClientAuthorizationRejected,
			"a failed exchange must not leave the code redeemable")
	})

	t.Run("another registered URI of the same client is refused", func(t *testing.T) {
		code := issue(t)
		require.ErrorIs(t, redeem(code, testOtherRedirect, testCodeVerifier, testClientID),
			auth.ErrClientAuthorizationRejected)
	})

	t.Run("another client may not redeem it", func(t *testing.T) {
		code := issue(t)
		require.ErrorIs(t, redeem(code, testRedirectURI, testCodeVerifier, "example-cli"),
			auth.ErrClientAuthorizationRejected)
	})

	t.Run("an unregistered client may not redeem it", func(t *testing.T) {
		code := issue(t)
		require.ErrorIs(t, redeem(code, testRedirectURI, testCodeVerifier, "nobody"),
			auth.ErrClientAuthorizationRejected)
	})

	t.Run("a code redeems exactly once", func(t *testing.T) {
		code := issue(t)
		require.NoError(t, redeem(code, testRedirectURI, testCodeVerifier, testClientID))
		require.ErrorIs(t, redeem(code, testRedirectURI, testCodeVerifier, testClientID),
			auth.ErrClientAuthorizationRejected)
	})

	t.Run("a code nobody issued is refused", func(t *testing.T) {
		require.ErrorIs(t, redeem("never-issued", testRedirectURI, testCodeVerifier, testClientID),
			auth.ErrClientAuthorizationRejected)
	})
}

// A caller with no verified session is the unauthenticated case the interceptor
// would normally stop; the business layer refuses it on its own too.
func TestIssuingACodeRequiresASignedInSession(t *testing.T) {
	clearData(t)
	registerTestClients(t)

	_, err := testService.IssueClientAuthorizationCode(testCtx,
		&gen.IssueClientAuthorizationCodeRequest{Authorization: authorizationRequest()})
	require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)
}

// A deployment that declared no client refuses the whole flow rather than
// behaving as though the registry check were absent.
func TestUnconfiguredRegistryRefusesEveryClient(t *testing.T) {
	clearData(t)

	_, err := testService.ValidateClientAuthorization(testCtx,
		&gen.ValidateClientAuthorizationRequest{Authorization: authorizationRequest()})
	require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)

	_, err = testService.ExchangeClientToken(testCtx, &gen.ExchangeClientTokenRequest{
		ClientId: testClientID,
		Grant: &gen.ExchangeClientTokenRequest_AuthorizationCode{
			AuthorizationCode: &gen.AuthorizationCodeGrant{
				Code: "anything", RedirectUri: testRedirectURI, CodeVerifier: testCodeVerifier,
			},
		},
	})
	require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)
}

func TestRegisteredClientsAreReadableForTheGateway(t *testing.T) {
	clearData(t)
	registerTestClients(t)

	declared := testService.RegisteredClients(testCtx)
	require.Len(t, declared, 2)
	require.Equal(t, "example-addin", declared[0].ClientID)
	require.Equal(t, []string{"https://addin.example.com"}, declared[0].Origins)
	require.Empty(t, declared[1].Origins, "a client with no browser context grants no origin")
}
