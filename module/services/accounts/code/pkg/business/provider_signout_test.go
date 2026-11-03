package business

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

const testEndSession = "https://idp.example.com/oidc/logout"

func signOutService(signOut ProviderSignOut) *Service {
	return &Service{providerSignOut: signOut}
}

// The id_token_hint the client supplied is what identifies the session to the
// provider, and the configured post-logout redirect travels with it. The host
// never stores a provider id_token, so a client that has one is the only source
// of the hint.
func TestEndSessionURLCarriesTheClientSuppliedHint(t *testing.T) {
	svc := signOutService(ProviderSignOut{
		EndSessionEndpoint:    testEndSession,
		ClientID:              "acme-host",
		PostLogoutRedirectURI: "https://app.example.com/signed-out",
	})

	parsed, err := url.Parse(svc.endSessionURL(context.Background(), "the-provider-id-token"))
	require.NoError(t, err)
	require.Equal(t, "https://idp.example.com/oidc/logout", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	require.Equal(t, "the-provider-id-token", parsed.Query().Get("id_token_hint"))
	require.Equal(t, "https://app.example.com/signed-out", parsed.Query().Get("post_logout_redirect_uri"))
	// RP-Initiated Logout 1.0 §2: client_id is for the case where no hint is
	// available. Sending both is allowed but says nothing more, so the hint
	// alone identifies the request.
	require.Empty(t, parsed.Query().Get("client_id"))
}

// With no hint, `client_id` identifies the relying party instead — the
// substitution §2 provides for exactly this case. A client that lost its
// id_token still reaches the provider's sign-out.
func TestEndSessionURLFallsBackToClientID(t *testing.T) {
	svc := signOutService(ProviderSignOut{EndSessionEndpoint: testEndSession, ClientID: "acme-host"})

	parsed, err := url.Parse(svc.endSessionURL(context.Background(), ""))
	require.NoError(t, err)
	require.Equal(t, "acme-host", parsed.Query().Get("client_id"))
	require.Empty(t, parsed.Query().Get("id_token_hint"))
	require.Empty(t, parsed.Query().Get("post_logout_redirect_uri"))
}

// A query the operator already put on the configured endpoint survives: some
// providers publish an end_session_endpoint carrying a tenant parameter, and
// replacing its query would break every sign-out on such a deployment.
func TestEndSessionURLPreservesTheConfiguredQuery(t *testing.T) {
	svc := signOutService(ProviderSignOut{
		EndSessionEndpoint: testEndSession + "?tenant=acme",
		ClientID:           "acme-host",
	})

	parsed, err := url.Parse(svc.endSessionURL(context.Background(), ""))
	require.NoError(t, err)
	require.Equal(t, "acme", parsed.Query().Get("tenant"))
	require.Equal(t, "acme-host", parsed.Query().Get("client_id"))
}

// No endpoint, and a configured endpoint that is not an absolute URL, both
// answer "". The host session has already ended by the time this runs, so a
// bad configuration must not turn a complete local sign-out into a reported
// failure — it is a warning at the log and an empty answer to the client,
// which the contract says to read as "nothing further to do".
func TestEndSessionURLIsEmptyWithoutAUsableEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "   ", "not-a-url", "/oidc/logout", "https://"} {
		svc := signOutService(ProviderSignOut{EndSessionEndpoint: endpoint, ClientID: "acme-host"})
		require.Empty(t, svc.endSessionURL(context.Background(), "a-hint"), "endpoint %q", endpoint)
	}
}

// The development fixture provider has no OIDC sign-out: work.go wires a
// ProviderSignOut only on the real-provider path, so a fixture deployment
// reaches here with nothing set. A sign-out then ends the host session and
// returns no URL, and the back-channel endpoint is not mounted at all.
func TestTheFixtureProviderOffersNoProviderSignOut(t *testing.T) {
	svc := signOutService(ProviderSignOut{})
	require.Empty(t, svc.endSessionURL(context.Background(), "a-hint"))
	require.False(t, svc.ProviderSignOutConfigured())

	_, err := svc.BackchannelLogout(context.Background(), "any.logout.token")
	require.Error(t, err)
	require.Contains(t, err.Error(), "verifies logout tokens")
}

// A verified token that names only the provider's own session id is a request
// this host cannot match: nothing here records a provider `sid`. It answers so
// rather than acknowledging a sign-out that did not happen.
func TestBackchannelLogoutRefusesASessionOnlyToken(t *testing.T) {
	svc := signOutService(ProviderSignOut{LogoutTokens: fixedLogoutToken{token: &auth.LogoutToken{
		Provider: "test-provider", SessionID: "provider-session-7", IssuedAt: time.Now(),
	}}})

	_, err := svc.BackchannelLogout(context.Background(), "a.logout.token")
	require.ErrorIs(t, err, ErrProviderSessionNotResolvable)
}

// A token the verifier refuses never reaches the store.
func TestBackchannelLogoutSurfacesAVerifierRefusal(t *testing.T) {
	svc := signOutService(ProviderSignOut{LogoutTokens: fixedLogoutToken{err: auth.ErrNotALogoutToken}})

	_, err := svc.BackchannelLogout(context.Background(), "a.logout.token")
	require.ErrorIs(t, err, auth.ErrNotALogoutToken)
}

type fixedLogoutToken struct {
	token *auth.LogoutToken
	err   error
}

func (f fixedLogoutToken) ValidateLogoutToken(context.Context, string) (*auth.LogoutToken, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.token, nil
}

// The interface the business layer declares is the one the OIDC validator
// satisfies. Asserted here because the only production assignment is a type
// switch in work.go, which would silently stop matching rather than fail to
// compile.
var _ LogoutTokenVerifier = fixedLogoutToken{}
