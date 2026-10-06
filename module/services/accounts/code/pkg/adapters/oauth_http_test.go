package adapters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The wire shape of the OAuth 2.1 surface. The shape IS the contract here: an
// MCP client reaches this host through a standard OAuth library, which sends a
// form-encoded token request and reads an RFC 6749 §5.2 error object. These
// tests exist because a transcoded RPC would have satisfied every business-level
// test in this repo and still been unusable by the clients the surface is for.

func oauthHandler(t *testing.T, declaredMetadata string) http.Handler {
	t.Helper()
	service, err := business.NewService(nil)
	require.NoError(t, err)

	registry, err := auth.NewClientRegistry(`[{
		"client_id": "example-addin", "name": "Example Add-in",
		"redirect_uris": ["https://addin.example.com/auth/callback"]
	}]`)
	require.NoError(t, err)
	service.SetClientRegistry(registry)

	policy, err := auth.NewClientMetadataPolicy(declaredMetadata)
	require.NoError(t, err)
	service.SetClientMetadataResolver(auth.NewClientMetadataResolver(policy))
	// The issuer is configuration, resolved once at startup — not the request's
	// origin. The published metadata and the `iss` of every minted token are the
	// same value, which is the only way the two can be proved equal.
	service.SetOAuthIssuer("https://host.example.com")
	return NewOAuthHTTPHandler(service)
}

// trustedOrigin stamps what the gateway stamps on an accounts route: the
// gateway credential plus the public origin it resolved. The origin is only
// believed beside the credential, which is the one rule that must not slip —
// a metadata document served under an attacker's issuer would send the next
// client's authorization request there.
func trustedOrigin(req *http.Request, origin string) {
	req.Header.Set("X-Codefly-Gateway-Token", "test-gateway-token")
	req.Header.Set("X-Codefly-Public-Origin", origin)
}

func TestTheMetadataDocumentIsServedAtItsOwnPath(t *testing.T) {
	withGatewayToken(t)
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodGet, OAuthMetadataPath, nil)
	trustedOrigin(req, "https://host.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))

	var document map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &document))
	require.Equal(t, "https://host.example.com", document["issuer"])
	require.Equal(t, "https://host.example.com/oauth2/authorize", document["authorization_endpoint"])
	require.Equal(t, "https://host.example.com/oauth2/token", document["token_endpoint"])
	require.Equal(t, true, document["client_id_metadata_document_supported"])
	require.NotContains(t, document, "registration_endpoint")
}

// A1007-02. No request can choose the issuer: it is configuration, resolved
// once, and the same value every minted token carries. A claimed origin — with
// or without the gateway credential — changes nothing about the document.
//
// This is stronger than what it replaced. The earlier version read the issuer
// from the request's VERIFIED origin and so only had to refuse an unverified
// claim; the issuer could still vary by Host header between two legitimately
// forwarded requests, and a token minted under one would fail verification
// against the other.
func TestNoRequestCanChooseTheIssuer(t *testing.T) {
	withGatewayToken(t)
	handler := oauthHandler(t, "any")

	for _, header := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("X-Codefly-Public-Origin", "https://evil.example.com") },
		func(r *http.Request) { trustedOrigin(r, "https://also-not-the-issuer.example.com") },
		func(*http.Request) {},
	} {
		req := httptest.NewRequest(http.MethodGet, OAuthMetadataPath, nil)
		header(req)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		require.Equal(t, 200, w.Code)
		var document map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &document))
		require.Equal(t, "https://host.example.com", document["issuer"])
		require.NotContains(t, w.Body.String(), "evil.example.com")
		require.NotContains(t, w.Body.String(), "also-not-the-issuer")
	}
}

// And with no configured issuer nothing is published at all: every URL in the
// document would name a host this service cannot vouch for, and a client that
// fetched it would authorize somewhere else.
func TestNoIssuerMeansNoPublishedMetadata(t *testing.T) {
	withGatewayToken(t)
	service, err := business.NewService(nil)
	require.NoError(t, err)
	handler := NewOAuthHTTPHandler(service)

	req := httptest.NewRequest(http.MethodGet, OAuthMetadataPath, nil)
	trustedOrigin(req, "https://host.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 503, w.Code)
	require.NotContains(t, w.Body.String(), "host.example.com")
}

// RFC 6749 §3.2: the token request is form-encoded. This is the assertion that
// a standard OAuth library can talk to this endpoint at all.
func TestTheTokenEndpointReadsAFormEncodedRequest(t *testing.T) {
	handler := oauthHandler(t, "any")

	body := strings.NewReader(
		"grant_type=authorization_code&client_id=example-addin&code=abc" +
			"&redirect_uri=https%3A%2F%2Faddin.example.com%2Fauth%2Fcallback" +
			"&code_verifier=ZmFrZS12ZXJpZmllci10aGF0LWlzLWxvbmctZW5vdWdoLTAxMjM0NTY3ODk")
	req := httptest.NewRequest(http.MethodPost, OAuthTokenPath, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// There is no store behind this service, so the exchange cannot succeed —
	// what matters is that the request was PARSED and answered as an OAuth
	// error rather than refused as a malformed body.
	require.Equal(t, 400, w.Code)
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, business.OAuthErrorInvalidGrant, refusal["error"])
	// The response IS the credential on success, so nothing between here and
	// the client may cache it.
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

// A grant type this host does not serve is NAMED as such. A client that asked
// for client_credentials needs to know the host has none, not that its
// credential was wrong.
func TestTheTokenEndpointNamesAnUnsupportedGrant(t *testing.T) {
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodPost, OAuthTokenPath,
		strings.NewReader("grant_type=client_credentials&client_id=example-addin"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 400, w.Code)
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, business.OAuthErrorUnsupportedGrant, refusal["error"])
}

// An unknown client is `invalid_client` with 400, not 401. There is no client
// authentication at this endpoint to have failed — a public client authenticates
// with `none` — and a 401 would make a browser prompt for credentials.
func TestAnUnknownClientIsInvalidClientWithoutA401(t *testing.T) {
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodPost, OAuthTokenPath,
		strings.NewReader("grant_type=authorization_code&client_id=nobody&code=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 400, w.Code)
	require.Empty(t, w.Header().Get("WWW-Authenticate"))
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, business.OAuthErrorInvalidClient, refusal["error"])
}

// The host's own frontend and its first registered client speak JSON
// everywhere else. A 400 for the wrong content type would be a trap with no
// security value — the credential is in the body either way.
func TestTheTokenEndpointAlsoReadsJSON(t *testing.T) {
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodPost, OAuthTokenPath,
		strings.NewReader(`{"grant_type":"client_credentials","client_id":"example-addin"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var refusal map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, business.OAuthErrorUnsupportedGrant, refusal["error"])
}

// The authorize validate endpoint tells the frontend whether a refusal may be
// delivered to the client's redirect URI. Before the URI is validated it may
// NOT — that is what keeps this from being an open redirector.
func TestTheValidateEndpointReportsWhetherARefusalIsDeliverable(t *testing.T) {
	withGatewayToken(t)
	handler := oauthHandler(t, "any")

	// An unregistered client: nothing has validated a redirect URI, so the
	// browser must not be sent to the one it named.
	req := httptest.NewRequest(http.MethodPost, OAuthAuthorizeValidatePath,
		strings.NewReader(`{"response_type":"code","client_id":"nobody","redirect_uri":"https://evil.example.com/cb","code_challenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM","code_challenge_method":"S256"}`))
	req.Header.Set("Content-Type", "application/json")
	trustedOrigin(req, "https://host.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 400, w.Code)
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, business.OAuthErrorInvalidClient, refusal["error"])
	require.Equal(t, false, refusal["redirectable"])

	// A registered client with a bad scope: the URI is its own, so the client
	// is told rather than the person being shown an error page.
	req = httptest.NewRequest(http.MethodPost, OAuthAuthorizeValidatePath,
		strings.NewReader(`{"response_type":"code","client_id":"example-addin","redirect_uri":"https://addin.example.com/auth/callback","code_challenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM","code_challenge_method":"S256","scope":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	trustedOrigin(req, "https://host.example.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 400, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, business.OAuthErrorInvalidScope, refusal["error"])
	require.Equal(t, true, refusal["redirectable"])
}

func TestTheValidateEndpointNamesTheClientAndTheResource(t *testing.T) {
	withGatewayToken(t)
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodPost, OAuthAuthorizeValidatePath,
		strings.NewReader(`{"response_type":"code","client_id":"example-addin","redirect_uri":"https://addin.example.com/auth/callback","code_challenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM","code_challenge_method":"S256","resource":"https://host.example.com/api/solutions/example/proxy/mcp"}`))
	req.Header.Set("Content-Type", "application/json")
	trustedOrigin(req, "https://host.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	var resolved map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resolved))
	require.Equal(t, "Example Add-in", resolved["client_name"])
	require.Equal(t, "registry", resolved["client_source"])
	require.Equal(t, "https://host.example.com/api/solutions/example/proxy/mcp", resolved["resource"])
	require.Equal(t, "example", resolved["resource_name"])
	require.Equal(t, true, resolved["requires_consent"])
}

// The code is issued from the signed-in person's own session, so the grant call
// is authenticated. Without a credential it must refuse rather than mint.
func TestTheGrantEndpointRefusesAnUnauthenticatedCaller(t *testing.T) {
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodPost, OAuthAuthorizeGrantPath,
		strings.NewReader(`{"response_type":"code","client_id":"example-addin","redirect_uri":"https://addin.example.com/auth/callback","code_challenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM","code_challenge_method":"S256"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
}

// Anything else under the prefix is a 404 from here. The prefix match has
// already shadowed the grpc-gateway mux, so falling through would be a 404 with
// a confusing body at best and a shadowed RPC at worst.
func TestAnUnknownPathUnderThePrefixIs404(t *testing.T) {
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodGet, OAuthRoutePrefix+"whatever", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, 404, w.Code)
}

// The prefix must be disjoint from every other route registered through
// RegisterHTTPRoute, in both directions: combineHandlers iterates a map, so an
// overlap would resolve nondeterministically and shadow an RPC at random.
// `/v1/auth/oauth/begin` — the host's own provider hop — is the near miss this
// guards.
func TestTheOAuthPrefixIsDisjointFromTheProviderHop(t *testing.T) {
	require.False(t, strings.HasPrefix("/v1/auth/oauth/begin", OAuthRoutePrefix))
	require.False(t, strings.HasPrefix(OAuthRoutePrefix, "/v1/auth/oauth/"))
	for _, path := range []string{
		OAuthMetadataPath, OAuthAuthorizeValidatePath, OAuthAuthorizeGrantPath, OAuthTokenPath,
	} {
		require.True(t, strings.HasPrefix(path, OAuthRoutePrefix), "%q", path)
	}
}

// A1007B-03. A deployment with no configured issuer publishes no OAuth
// metadata, and that is a supported mode — the registered-client browser
// handoff predates discovery entirely. An earlier revision read the issuer out
// of the metadata document on the grant path and answered 503 when there was
// none, AFTER the code had been created: the browser never received a code that
// existed, so an add-in that worked before the change stopped working.
//
// Adopted from the Astra review (registered_client_without_issuer).
func TestTheGrantPathWorksWithNoPublishedIssuer(t *testing.T) {
	withGatewayToken(t)
	service, err := business.NewService(nil)
	require.NoError(t, err)
	registry, err := auth.NewClientRegistry(`[{
		"client_id": "example-addin", "name": "Example Add-in",
		"redirect_uris": ["https://addin.example.com/auth/callback"]
	}]`)
	require.NoError(t, err)
	service.SetClientRegistry(registry)
	// No SetOAuthIssuer: exactly the default local configuration.
	handler := NewOAuthHTTPHandler(service)

	body := `{"response_type":"code","client_id":"example-addin",` +
		`"redirect_uri":"https://addin.example.com/auth/callback",` +
		`"code_challenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",` +
		`"code_challenge_method":"S256"}`

	// Validation answers, with no consent required and an empty issuer.
	req := httptest.NewRequest(http.MethodPost, OAuthAuthorizeValidatePath,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	trustedOrigin(req, "https://host.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	var resolved map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resolved))
	require.Equal(t, false, resolved["requires_consent"])
	require.Equal(t, "", resolved["issuer"])

	// And the grant with a VERIFIED caller is not a 503.
	//
	// The caller matters: with no bearer the handler refuses at authentication,
	// before it could ever consult the issuer, so such a request stays green
	// however the issuer is handled and proves nothing about it. A verified
	// session is what carries the request past authentication and into the code
	// issuer, which is where the published issuer would be read.
	service.SetJWTMinter(&fixedAccessMinter{identity: &auth.Identity{
		UserID:    uuid.New(),
		OrgID:     uuid.New(),
		SessionID: uuid.New(),
	}})

	req = httptest.NewRequest(http.MethodPost, OAuthAuthorizeGrantPath,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer verified")
	trustedOrigin(req, "https://host.example.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.NotEqual(t, 503, w.Code,
		"a missing OAuth issuer must not break the registered-client handoff")
	require.NotEqual(t, 401, w.Code,
		"the caller is verified, so a 401 here would mean this test never reached the issuer")
	require.NotContains(t, strings.ToLower(w.Body.String()), "issuer",
		"whatever refuses this harness must not be the absent issuer")
}

// A1007B-05. The JSON encoding must be as strict as the form one. Go's decoder
// keeps the LAST of a repeated member and ignores anything after the first
// value, so a body with two `resource` members — naming different solutions —
// was accepted here while the identical form request was refused. One lenient
// encoding makes the whole strict-input policy advisory.
//
// Adopted from the Astra review (json_duplicate_token_parameters,
// json_trailing_content).
func TestTheJSONTokenRequestIsAsStrictAsTheForm(t *testing.T) {
	handler := oauthHandler(t, "any")

	post := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code, body)
		var refusal map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refusal))
		return refusal
	}

	// A repeated member naming two different solutions.
	refusal := post(`{"resource":"https://host.example.com/api/solutions/other/proxy/mcp",` +
		`"grant_type":"authorization_code","client_id":"example-addin","code":"abc",` +
		`"resource":"https://host.example.com/api/solutions/example/proxy/mcp"}`)
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal["error"])
	require.Contains(t, refusal["error_description"], "more than once")

	// A second object appended after the first.
	refusal = post(`{"grant_type":"authorization_code","client_id":"example-addin",` +
		`"code":"abc"}{"grant_type":"refresh_token"}`)
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal["error"])

	// A non-string value, refused rather than coerced.
	refusal = post(`{"grant_type":"authorization_code","client_id":"example-addin","resource":1}`)
	require.Equal(t, business.OAuthErrorInvalidRequest, refusal["error"])

	// A single well-formed object still parses: it reaches the grant and is
	// refused for the grant's own reason, not for its encoding.
	req := httptest.NewRequest(http.MethodPost, OAuthTokenPath,
		strings.NewReader(`{"grant_type":"client_credentials","client_id":"example-addin"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var answered map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &answered))
	require.Equal(t, business.OAuthErrorUnsupportedGrant, answered["error"])
}
