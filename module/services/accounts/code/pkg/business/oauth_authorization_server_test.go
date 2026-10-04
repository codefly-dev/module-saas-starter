package business_test

import (
	"context"
	"encoding/json"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// The authorization-server surface, without a database. Everything here is
// decided from configuration and the request: the registry, the metadata
// document, the PKCE shape, the scope, and the resource indicator. The code
// table and the minting are exercised in the DB-backed story beside it.

const claudeCodeMetadataClientID = "https://claude.ai/oauth/claude-code-client-metadata"

const theHost = "https://host.example.com"

// oauthService builds a service with the two client sources wired and nothing
// else. `declaredMetadata` is the IDENTITY_CLIENT_METADATA_DOCUMENTS value.
func oauthService(t *testing.T, declaredMetadata string) *business.Service {
	t.Helper()
	service, err := business.NewService(nil)
	require.NoError(t, err)

	registry, err := auth.NewClientRegistry(`[
		{"client_id": "example-addin", "name": "Example Add-in", "kind": "public",
		 "redirect_uris": ["https://addin.example.com/auth/callback"],
		 "origins": ["https://addin.example.com"]},
		{"client_id": "example-cli", "name": "Example CLI",
		 "redirect_uris": ["http://localhost/callback"]}
	]`)
	require.NoError(t, err)
	service.SetClientRegistry(registry)

	policy, err := auth.NewClientMetadataPolicy(declaredMetadata)
	require.NoError(t, err)
	service.SetClientMetadataResolver(auth.NewClientMetadataResolver(policy))
	// The issuer is configuration, resolved once — not the request's origin. A
	// test that wants it absent clears it explicitly (see
	// TestTheMetadataDocumentIsNotPublishedWithoutAnIssuer).
	service.SetOAuthIssuer(theHost)
	return service
}

// issuerContext is what the frontend's trusted public origin looks like by the
// time it reaches the service.
func issuerContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := auth.WithVerifiedPublicOrigin(context.Background(), theHost)
	require.NoError(t, err)
	return ctx
}

func authorizeRequest() business.OAuthAuthorizationRequest {
	return business.OAuthAuthorizationRequest{
		ResponseType:        "code",
		ClientID:            "example-addin",
		RedirectURI:         "https://addin.example.com/auth/callback",
		CodeChallenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CodeChallengeMethod: "S256",
		State:               "opaque-state",
	}
}

// RFC 8414 conformance, which is the first link in an MCP client's discovery
// chain. Every value is asserted because every value is a promise about what
// this code does, and a document claiming a capability the host lacks sends
// clients into a flow that cannot complete.
func TestTheAuthorizationServerMetadataDocumentConforms(t *testing.T) {
	service := oauthService(t, "any")
	metadata, err := service.AuthorizationServerMetadata(issuerContext(t))
	require.NoError(t, err)

	require.Equal(t, theHost, metadata.Issuer)
	require.Equal(t, theHost+"/oauth2/authorize", metadata.AuthorizationEndpoint)
	require.Equal(t, theHost+"/oauth2/token", metadata.TokenEndpoint)
	require.Equal(t, theHost+"/v1/auth/.well-known/jwks.json", metadata.JWKSURI)
	require.Equal(t, []string{"S256"}, metadata.CodeChallengeMethodsSupported)
	require.Equal(t, []string{"authorization_code", "refresh_token"}, metadata.GrantTypesSupported)
	require.Equal(t, []string{"none"}, metadata.TokenEndpointAuthMethodsSupported)
	require.Equal(t, []string{"code"}, metadata.ResponseTypesSupported)
	require.Equal(t, []string{"offline_access"}, metadata.ScopesSupported)
	require.True(t, metadata.ClientIDMetadataDocumentSupported)
	require.True(t, metadata.AuthorizationResponseISSParameterSupported)
	require.Empty(t, metadata.ServiceDocumentation)
}

// No registration_endpoint, ever. Dynamic client registration (RFC 7591) is
// deprecated by the MCP specification in favour of metadata documents, and it
// is the one mechanism here that would grow a durable registry on
// unauthenticated request. Its ABSENCE from the document is how a conforming
// client learns not to try, so it is asserted rather than assumed — the field
// is not in the struct at all, which this holds by serialising the document.
func TestTheMetadataDocumentPublishesNoRegistrationEndpoint(t *testing.T) {
	service := oauthService(t, "any")
	metadata, err := service.AuthorizationServerMetadata(issuerContext(t))
	require.NoError(t, err)
	require.NotContains(t, marshalJSON(t, metadata), "registration_endpoint")
}

// A deployment that has not enabled metadata documents says so in the
// document, so a client reads "do not bother" rather than trying and failing.
func TestTheMetadataDocumentReportsWhetherMetadataClientsAreEnabled(t *testing.T) {
	service := oauthService(t, "")
	metadata, err := service.AuthorizationServerMetadata(issuerContext(t))
	require.NoError(t, err)
	require.False(t, metadata.ClientIDMetadataDocumentSupported)
}

// Without a trusted origin every URL in the document would be a guess, and a
// client that fetched it would send its authorization request to whatever it
// named. Fail closed.
func TestTheMetadataDocumentIsNotPublishedWithoutAnIssuer(t *testing.T) {
	service := oauthService(t, "any")
	service.SetOAuthIssuer("")
	_, err := service.AuthorizationServerMetadata(context.Background())
	require.ErrorIs(t, err, business.ErrOAuthIssuerUnavailable)
}

func TestAnOperatorDeclaredClientNeedsNoConsent(t *testing.T) {
	service := oauthService(t, "any")
	resolved, err := service.ResolveOAuthAuthorization(issuerContext(t), authorizeRequest())
	require.NoError(t, err)
	require.Equal(t, "Example Add-in", resolved.Client.Name)
	require.False(t, resolved.Client.Metadata)
	require.Equal(t, "offline_access", resolved.Scope)
	// The deployment vouched for this client and the request narrows nothing,
	// so adding a prompt would change a shipped client's flow for no gain in
	// what the person can decide.
	require.False(t, resolved.RequiresConsent)
}

// A request naming a resource is a request to narrow a credential to something
// specific, which the person should see named.
func TestARequestNamingAResourceNeedsConsent(t *testing.T) {
	service := oauthService(t, "any")
	request := authorizeRequest()
	request.Resource = theHost + "/solutions/example/mcp"

	resolved, err := service.ResolveOAuthAuthorization(issuerContext(t), request)
	require.NoError(t, err)
	require.Equal(t, request.Resource, resolved.Resource.Value)
	require.Equal(t, "example", resolved.Resource.SolutionID)
	require.True(t, resolved.RequiresConsent)
}

// A resource at somebody else's origin is refused: this host does not mint
// audiences for hosts it is not.
func TestAResourceAtAnotherOriginIsRefused(t *testing.T) {
	service := oauthService(t, "any")
	request := authorizeRequest()
	request.Resource = "https://evil.example.com/solutions/example/mcp"

	_, err := service.ResolveOAuthAuthorization(issuerContext(t), request)
	refusal := requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorInvalidTarget, refusal.Code)
	require.True(t, refusal.Redirectable,
		"the redirect URI was validated first, so the client is told")
}

// Each authorize-side rule, and whether the refusal may be delivered to the
// client. The `redirectable` half is the load-bearing one: redirecting to a
// redirect_uri before it is validated turns this endpoint into an open
// redirector any site can use.
func TestEachAuthorizeRuleRefusesWithTheRightDeliverability(t *testing.T) {
	service := oauthService(t, "any")
	ctx := issuerContext(t)

	tests := []struct {
		name             string
		mutate           func(*business.OAuthAuthorizationRequest)
		wantCode         string
		wantRedirectable bool
	}{
		{
			name:     "an unregistered client id",
			mutate:   func(r *business.OAuthAuthorizationRequest) { r.ClientID = "nobody" },
			wantCode: business.OAuthErrorInvalidClient,
			// Nothing has validated a redirect URI for a client that does not
			// exist, so the browser must not be sent to the one it named.
			wantRedirectable: false,
		},
		{
			name:             "a redirect URI the client did not register",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.RedirectURI = "https://evil.example.com/cb" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: false,
		},
		{
			name:             "a redirect URI registered to a DIFFERENT client",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.RedirectURI = "http://localhost/callback" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: false,
		},
		{
			name:   "a response type other than code",
			mutate: func(r *business.OAuthAuthorizationRequest) { r.ResponseType = "token" },
			// RFC 6749 §4.1.2.1's own code, not invalid_request: the request is
			// well formed and asks for something this server does not serve.
			wantCode:         business.OAuthErrorUnsupportedResponse,
			wantRedirectable: true,
		},
		{
			name:             "no response type at all",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.ResponseType = "" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: true,
		},
		{
			name:             "no PKCE method at all",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.CodeChallengeMethod = "" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: true,
		},
		{
			name:             "the plain PKCE method",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.CodeChallengeMethod = "plain" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: true,
		},
		{
			name:             "no PKCE challenge at all",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.CodeChallenge = "" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: true,
		},
		{
			name:             "a malformed PKCE challenge",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.CodeChallenge = "not+base64url/at+all" },
			wantCode:         business.OAuthErrorInvalidRequest,
			wantRedirectable: true,
		},
		{
			name:             "a scope this host does not issue",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.Scope = "admin" },
			wantCode:         business.OAuthErrorInvalidScope,
			wantRedirectable: true,
		},
		{
			name:             "a resource that is not a solution MCP endpoint",
			mutate:           func(r *business.OAuthAuthorizationRequest) { r.Resource = theHost + "/v1/users" },
			wantCode:         business.OAuthErrorInvalidTarget,
			wantRedirectable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := authorizeRequest()
			test.mutate(&request)
			_, err := service.ResolveOAuthAuthorization(ctx, request)
			refusal := requireAuthorizationError(t, err)
			require.Equal(t, test.wantCode, refusal.Code)
			require.Equal(t, test.wantRedirectable, refusal.Redirectable)
		})
	}
}

// The one scope this host publishes is accepted, present or absent, and
// anything else is refused rather than silently dropped. A dropped scope is a
// client believing it was granted something.
func TestTheSupportedScopeIsAcceptedAndEveryOtherIsRefused(t *testing.T) {
	service := oauthService(t, "any")
	ctx := issuerContext(t)

	for _, scope := range []string{"", "offline_access"} {
		request := authorizeRequest()
		request.Scope = scope
		resolved, err := service.ResolveOAuthAuthorization(ctx, request)
		require.NoError(t, err, "should accept scope %q", scope)
		require.Equal(t, "offline_access", resolved.Scope)
	}
	for _, scope := range []string{"admin", "offline_access admin", "openid"} {
		request := authorizeRequest()
		request.Scope = scope
		_, err := service.ResolveOAuthAuthorization(ctx, request)
		require.Error(t, err, "should refuse scope %q", scope)
	}
}

// A metadata client is refused outright where the deployment did not enable
// the mechanism — the same answer an unregistered slug gets, so an
// unauthenticated caller cannot tell the two apart.
func TestAMetadataClientIsRefusedWhenTheDeploymentHasNotEnabledThem(t *testing.T) {
	service := oauthService(t, "")
	request := authorizeRequest()
	request.ClientID = claudeCodeMetadataClientID
	request.RedirectURI = "http://localhost:54321/callback"

	_, err := service.ResolveOAuthAuthorization(issuerContext(t), request)
	refusal := requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorInvalidClient, refusal.Code)
	require.False(t, refusal.Redirectable)
}

// An unsupported grant type is named as such, not collapsed into a bad grant:
// a client that asked for client_credentials needs to know this host has none,
// not that its credential was wrong.
func TestAnUnsupportedGrantTypeIsNamed(t *testing.T) {
	service := oauthService(t, "any")
	_, err := service.ExchangeOAuthToken(context.Background(), business.OAuthTokenRequest{
		GrantType: "client_credentials",
		ClientID:  "example-addin",
	})
	refusal := requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorUnsupportedGrant, refusal.Code)

	_, err = service.ExchangeOAuthToken(context.Background(), business.OAuthTokenRequest{
		ClientID: "example-addin",
	})
	refusal = requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal.Code)
}

func requireAuthorizationError(t *testing.T, err error) *business.OAuthAuthorizationError {
	t.Helper()
	require.Error(t, err)
	refusal, ok := err.(*business.OAuthAuthorizationError)
	require.True(t, ok, "expected an OAuthAuthorizationError, got %T: %v", err, err)
	return refusal
}

func marshalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

// The grant endpoint re-resolves the whole request rather than trusting what
// the page hands it. The page carries the request through sessionStorage across
// a sign-in and a navigation, so by the time it asks for a code the values have
// been outside the host's hands — and a tampered one must not be able to widen
// anything. This is the test that re-resolution is real and not an optimisation
// someone could remove.
func TestTheGrantPathReResolvesTheRequestItIsGiven(t *testing.T) {
	service := oauthService(t, "any")
	ctx := issuerContext(t)

	// Same values the page would hold, with the redirect URI swapped for one the
	// client never registered. No session is needed to prove the point: the
	// refusal must come from resolution, before the code issuer is reached at
	// all, which is what makes it independent of who is calling.
	request := authorizeRequest()
	request.RedirectURI = "https://evil.example.com/cb"

	_, _, err := service.GrantOAuthAuthorization(ctx, request)
	refusal := requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal.Code)
	require.False(t, refusal.Redirectable)
}

// A1007-02. One issuer: what the metadata publishes is what tokens carry. The
// metadata is built from the CONFIGURED value, never from the request, so two
// clients reaching the same deployment through different Host headers cannot be
// handed two different issuers — and a token minted under one cannot fail
// verification against the other. Adopted from the Astra review
// (issuer_matches_metadata).
func TestTheMetadataIssuerIsTheConfiguredOneAndNotTheRequestOrigin(t *testing.T) {
	service := oauthService(t, "any")
	service.SetOAuthIssuer(theHost)

	// A request arriving with a different verified origin must not move it.
	elsewhere, err := auth.WithVerifiedPublicOrigin(context.Background(),
		"https://other-host.example.com")
	require.NoError(t, err)
	metadata, err := service.AuthorizationServerMetadata(elsewhere)
	require.NoError(t, err)
	require.Equal(t, theHost, metadata.Issuer)
	require.Equal(t, theHost+"/oauth2/authorize", metadata.AuthorizationEndpoint)

	// And with no configured issuer nothing is published, whatever the request
	// claims: every URL in the document would name a host this service cannot
	// vouch for, and a client that fetched it would authorize somewhere else.
	bare := oauthService(t, "any")
	bare.SetOAuthIssuer("")
	_, err = bare.AuthorizationServerMetadata(elsewhere)
	require.ErrorIs(t, err, business.ErrOAuthIssuerUnavailable)
}

// A1007-07. response_type is required and `code` is the only one served, and a
// request for another gets RFC 6749 §4.1.2.1's own code rather than
// invalid_request — a client asking for `token` has made a well-formed request
// for something this server does not do.
func TestTheResponseTypeIsRequiredAndOnlyCodeIsServed(t *testing.T) {
	service := oauthService(t, "any")
	ctx := issuerContext(t)

	request := authorizeRequest()
	request.ResponseType = ""
	refusal := requireAuthorizationError(t,
		mustFailResolve(t, service, ctx, request))
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal.Code)
	require.Contains(t, refusal.Description, "response_type is required")

	request.ResponseType = "token"
	refusal = requireAuthorizationError(t, mustFailResolve(t, service, ctx, request))
	require.Equal(t, business.OAuthErrorUnsupportedResponse, refusal.Code)
	require.True(t, refusal.Redirectable)

	// And the method is not defaulted either: a request naming none has not
	// asked for S256, and supplying it would mask a client that believed it was
	// sending `plain`.
	request = authorizeRequest()
	request.CodeChallengeMethod = ""
	refusal = requireAuthorizationError(t, mustFailResolve(t, service, ctx, request))
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal.Code)
}

// A1007-07. A refresh request's scope must not exceed the grant (RFC 6749 §6),
// and it is refused BEFORE the rotation — so a client that asked for more does
// not lose the token it has for asking.
func TestARefreshScopeBeyondTheGrantIsRefusedBeforeRotation(t *testing.T) {
	service := oauthService(t, "any")

	_, err := service.ExchangeOAuthToken(context.Background(), business.OAuthTokenRequest{
		GrantType:    "refresh_token",
		ClientID:     "example-addin",
		RefreshToken: "whatever",
		Scope:        "admin",
	})
	refusal := requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorInvalidScope, refusal.Code)
}

// Consent is enforced by the HOST, not merely advertised to the page. A caller
// told consent is required and asking for a code without saying the person
// approved is refused — which is what stops a browser-side defect (a
// mis-decoded `requires_consent`, a dropped navigation) from issuing
// credentials nobody approved.
func TestAGrantNeedingConsentIsRefusedWithoutIt(t *testing.T) {
	service := oauthService(t, "any")
	ctx := issuerContext(t)
	request := authorizeRequest()
	request.Resource = theHost + "/solutions/example/mcp"

	resolved, err := service.ResolveOAuthAuthorization(ctx, request)
	require.NoError(t, err)
	require.True(t, resolved.RequiresConsent)

	_, _, err = service.GrantOAuthAuthorization(ctx, request)
	refusal := requireAuthorizationError(t, err)
	require.Equal(t, business.OAuthErrorAccessDenied, refusal.Code)
	require.Contains(t, refusal.Description, "approval")
}

func mustFailResolve(
	t *testing.T,
	service *business.Service,
	ctx context.Context,
	request business.OAuthAuthorizationRequest,
) error {
	t.Helper()
	_, err := service.ResolveOAuthAuthorization(ctx, request)
	require.Error(t, err)
	return err
}
