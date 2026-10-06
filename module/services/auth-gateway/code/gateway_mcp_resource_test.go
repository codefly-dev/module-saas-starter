package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A solution's MCP endpoint as an OAuth protected resource (issue #1003).
//
// Two things are enforced here and nowhere else: the RFC 8707 audience binding
// (a token minted for one solution is refused at another) and the RFC 6750 /
// RFC 9728 challenge that starts an MCP client's discovery chain.

const testPublicBase = "https://host.example.com"

// withPublicBase pins the operator-trusted public origin for one test. The
// gateway reads it from the `application` configuration group, which falls back
// to the process environment.
func withPublicBase(t *testing.T, base string) {
	t.Helper()
	t.Setenv("APP_BASE_URL", base)
}

// signResourceToken signs an access token carrying the host audience plus one
// resource audience — the shape accounts mints for a resource-bound session.
// signTwoResourceToken signs a token carrying the host audience plus TWO
// resource audiences — a shape no issuance path produces, which is the point:
// the verifier must refuse it rather than reduce it to something it admits.
func signTwoResourceToken(t *testing.T, priv ed25519.PrivateKey, first, second string) string {
	t.Helper()
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   uuid.Must(uuid.NewV7()).String(),
			Audience:  jwt.ClaimStrings{"saas-starter", first, second},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti",
		},
		OrgID:     uuid.Must(uuid.NewV7()).String(),
		SessionID: uuid.Must(uuid.NewV7()).String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func signResourceToken(t *testing.T, priv ed25519.PrivateKey, resource string) string {
	t.Helper()
	audience := jwt.ClaimStrings{"saas-starter"}
	if resource != "" {
		audience = append(audience, resource)
	}
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   uuid.Must(uuid.NewV7()).String(),
			Audience:  audience,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti",
		},
		OrgID:           uuid.Must(uuid.NewV7()).String(),
		OrgRole:         "admin",
		SessionID:       uuid.Must(uuid.NewV7()).String(),
		AuthorizedParty: "https://claude.ai/oauth/claude-code-client-metadata",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = accessKeyID(priv.Public().(ed25519.PublicKey))
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

// The whole point of the resource indicator: a token the person approved for
// one solution reaches that solution and is refused at every other.
func TestGateway_MCPToken_IsAdmittedAtItsOwnResourceOnly(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")
	other := registerSolutionUpstream(t, gw, "audit")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "/mcp", example.lastPath)
	// The challenge is not part of a served answer: a conforming client would
	// read it as "your token was refused" on a response carrying its data.
	require.Empty(t, w.Header().Get("WWW-Authenticate"))

	// The same token at another solution's MCP endpoint. 401, not 403: RFC 6750
	// classes a token that is not valid for this resource as invalid_token, and
	// an MCP client answers 401 by re-running discovery for the right resource.
	req = httptest.NewRequest(http.MethodPost, "/solutions/audit/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Body.String(), "issued for a different resource")
	require.Empty(t, other.lastPath, "the other solution must never be reached")
}

// A resource-bound token is refused at ANOTHER solution's ordinary data
// endpoints too, not only at its MCP path. Narrowing only the MCP path would
// leave the one replay the audience exists to stop.
func TestGateway_MCPToken_IsRefusedAtAnotherSolutionsDataRoutes(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")
	other := registerSolutionUpstream(t, gw, "audit")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Empty(t, other.lastPath)
}

// The solution whose resource the token names may serve its own other routes:
// its runtime, having been reached with this token, calls back through the
// gateway on the person's behalf.
func TestGateway_MCPToken_ReachesItsOwnSolutionsOtherRoutes(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
	req := httptest.NewRequest(http.MethodGet, "/solutions/example/resolve", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "/resolve", example.lastPath)
}

// A resource-bound token still reaches the host's own API. That is not a hole:
// the host is where a solution's runtime resolves the caller's authority, and
// the token names the host audience for precisely that reason. Refusing it here
// would break the runtime without closing anything — nothing a solution can do
// with the host's API is narrowed by which solution asked.
func TestGateway_MCPToken_StillReachesTheHostAPI(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, apiFake, _, priv := newGatewayHarness(t)

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "/v1/users", apiFake.lastPath)
}

// A token carrying no resource is the ordinary session credential the host has
// always minted — the browser's own session, and the first registered client.
// It keeps reaching every solution surface, so enabling resource indicators
// changes nothing for anything that predates them.
// SP-SOL-07, register finding SA-F-MCPAUD. A solution's tool endpoint is an
// OAuth protected resource in its own right, so a token carrying only the host
// audience — a credential addressed to a different thing — does not reach it.
//
// This test replaces one that asserted the opposite. Issue #1003's wording also
// admitted the plain session-kind token here; the register's invariant is
// narrower and wins, because "any token this host signed" is precisely the
// property a resource indicator exists to stop being sufficient.
func TestGateway_AGenericHostTokenIsRefusedAtTheToolEndpoint(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Body.String(), "requires a token issued for its own resource")
	// The refusal must carry the challenge, or the client it refuses has nothing
	// to act on: it learns what to authorize FOR from this header and nowhere
	// else. A 401 without one is the measured defect this surface exists to fix.
	require.Contains(t, w.Header().Get("WWW-Authenticate"), "resource_metadata=")
	require.Empty(t, example.lastPath, "the refused request must not have reached the solution")
}

// The line drawn above is the tool endpoint, NOT every solution route. A
// solution's other routes are the product's own API surface, reached by a
// signed-in person whose session token carries no resource at all; refusing
// those would refuse the product. Tightening one surface and tightening all of
// them are different changes, and only the first is the register's.
func TestGateway_AGenericHostTokenStillReachesASolutionsOtherRoutes(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/saas.example.v1.Items/List", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "/saas.example.v1.Items/List", example.lastPath)
}

// Fail closed, by name. Without a configured public address this process cannot
// state which resource its tool endpoint requires, so it cannot judge a token
// against one — and any challenge it emitted would carry no absolute URL for a
// client to follow. It says that, rather than falling back to admitting
// whatever arrived.
func TestGateway_TheToolEndpointFailsClosedWithoutAConfiguredPublicAddress(t *testing.T) {
	withPublicBase(t, "")
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization",
		"Bearer "+signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp"))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Body.String(), "no configured public address")
	require.Empty(t, example.lastPath)
}

// Point 5 of the staging measurements, answered deliberately: a browser session
// cookie is NOT a credential at this perimeter. The gateway verifies a bearer —
// this host's access token, or an API key — and reads no cookie anywhere, so a
// cookie-bearing caller is refused. What changes here is that the refusal says
// so, instead of answering "authentication required" to someone who believes
// they presented a credential.
func TestGateway_ASessionCookieIsRefusedAndSaysWhy(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, _ := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Cookie", "refresh_token=whatever")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Body.String(), "session cookie is not a credential")
	require.Contains(t, w.Body.String(), "Authorization: Bearer")
	require.Contains(t, w.Header().Get("WWW-Authenticate"), "resource_metadata=")
	require.Empty(t, example.lastPath)
}

// And a caller that presented nothing at all still gets the plain answer: the
// cookie sentence is an explanation for a specific mistake, not a new generic
// message for every unauthenticated request.
func TestGateway_ARequestWithNoCredentialKeepsThePlainRefusal(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Body.String(), "authentication required")
	require.NotContains(t, w.Body.String(), "session cookie")
}

// A resource at a host this deployment is not. The origin is pinned to
// APP_BASE_URL so a token minted against some other spelling of this solution
// is refused — which is what stops a second deployment's token being replayed
// here.
func TestGateway_MCPToken_MustNameThisHostsOrigin(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, "https://other-host.example.com/api/solutions/example/proxy/mcp")
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Empty(t, example.lastPath)
}

// Without a configured public address a resource-bound token is refused at every
// solution path, the tool endpoint included.
//
// This replaces a test that asserted the origin simply went unchecked there. The
// comparison is now exact agreement with one composed identifier, and a process
// that cannot say what that identifier IS cannot find anything equal to it.
// Accounts refuses to ISSUE a resource-bound token in the same state, so the two
// sides agree rather than one minting what the other rejects — which is why this
// is fail-closed rather than a gap.
func TestGateway_WithNoConfiguredAddressAResourceBoundTokenIsRefused(t *testing.T) {
	withPublicBase(t, "")
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, "http://localhost:3000/api/solutions/example/proxy/mcp")

	for _, path := range []string{"/solutions/example/mcp", "/solutions/example/v1/items"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, 401, w.Code, path)
	}
	require.Empty(t, example.lastPath)

	// An unbound session token still reaches the product's own API surface, so
	// the refusal above is about the resource binding and not about this
	// deployment having no configured address.
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/v1/items", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
}

// R54-04. The audience must EQUAL this endpoint's resource identifier by
// code-point equality — not match a parsed, case-folded reconstruction of it.
//
// The resource URL is itself the audience value, the runtime publishes that same
// string as its `resource`, and RFC 9728 §3.3 has the client require the
// document's `resource` to equal the URL it dialled. No step in that chain folds
// case or re-renders the URL, so a variant admitted here is one the client it was
// issued for would reject — and a host treating several spellings as one resource
// has several resources.
func TestTheAudienceMustEqualThisEndpointsResourceExactly(t *testing.T) {
	const exact = testPublicBase + "/api/solutions/example/proxy/mcp"

	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	call := func(resource string) int {
		req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+signResourceToken(t, priv, resource))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w.Code
	}

	require.Equal(t, 200, call(exact), "the exact identifier must be admitted")
	require.Equal(t, "/mcp", example.lastPath)

	for _, variant := range []string{
		// Case-only differences, in the host and in the scheme. url.Parse and a
		// case-folded origin comparison call these equal; code-point equality
		// does not, and neither does the client.
		strings.Replace(exact, "host.example.com", "HOST.example.com", 1),
		strings.Replace(exact, "host.example.com", "Host.Example.Com", 1),
		strings.Replace(exact, "https://", "HTTPS://", 1),
		// Case in the path, which names the solution.
		strings.Replace(exact, "/example/", "/Example/", 1),
		// Shapes that differ by more than case.
		exact + "/",
		strings.Replace(exact, "host.example.com", "host.example.com:443", 1),
		exact + "/extra",
		strings.Replace(exact, "/proxy/mcp", "/mcp", 1),
	} {
		require.Equal(t, 401, call(variant), "must refuse %q", variant)
	}
}

func TestGateway_MCPDiscoveryChain_401ThenProtectedResourceMetadata(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	// Link 1: an unauthenticated request is refused WITH somewhere to go. A 401
	// without the challenge is a dead end — the client reports "unauthorized"
	// and has nothing to try.
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
	challenge := w.Header().Get("WWW-Authenticate")
	require.Contains(t, challenge, `error="invalid_token"`)
	// The URL decided in issue #1003 (2026-10-04): the solution's own
	// well-known, derivable from the route, which the runtime serves and the
	// gateway already proxies unauthenticated.
	require.Contains(t, challenge,
		`resource_metadata="`+testPublicBase+`/api/solutions/example/proxy/.well-known/oauth-protected-resource"`)

}

// An expired or wrong-resource token gets the challenge too, so a client whose
// token went stale re-discovers rather than reporting a flat failure.
func TestGateway_MCPChallengeIsStampedOnAWrongResourceRefusal(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/audit/proxy/mcp")
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Header().Get("WWW-Authenticate"),
		`resource_metadata="`+testPublicBase+`/api/solutions/example/proxy/.well-known/oauth-protected-resource"`)
}

// A1007-11. Every protected solution path carries the challenge, not only the
// exact `/mcp`. Adopted from the Astra review
// (TestReview1007GeneralSolutionChallenge), which measured the dead ends: a
// trailing slash and any ordinary solution route got a 401 with no header, so a
// client had nowhere to begin.
//
// This gateway is where such a request is denied — it strips identity, runs
// ext_authz and answers itself, never proxying — so a challenge the runtime
// would have sent cannot reach anyone through it.
func TestGateway_EveryProtectedSolutionPathCarriesTheChallenge(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	for _, path := range []string{
		"/solutions/example/mcp",
		"/solutions/example/mcp/",
		"/solutions/example/v1/items",
		"/solutions/example/resolve",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, 401, w.Code, path)
		require.Contains(t, w.Header().Get("WWW-Authenticate"),
			`resource_metadata="`+testPublicBase+`/api/solutions/example/proxy/.well-known/oauth-protected-resource"`,
			"no challenge on %s leaves a client with nowhere to begin", path)
	}
}

// And it is absent once a request is served: leaving it on a 200 would tell a
// conforming client its token was refused on the response carrying its data.
func TestGateway_TheChallengeIsAbsentOnAServedSolutionResponse(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	// Each path with a credential that path actually admits: the tool endpoint
	// takes only a token issued for its own resource, the product surface takes
	// the ordinary session token.
	for path, token := range map[string]string{
		"/solutions/example/mcp": signResourceToken(t, priv,
			testPublicBase+"/api/solutions/example/proxy/mcp"),
		"/solutions/example/v1/items": signValidToken(t, priv),
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, 200, w.Code, path)
		require.Empty(t, w.Header().Get("WWW-Authenticate"), path)
	}
}

// A1007-02. The gateway verifies the issuer accounts mints — the configured
// public base URL — and keeps accepting the pre-metadata literal while tokens
// minted under it are unexpired. A token naming anything else is refused.
func TestGateway_AcceptsTheConfiguredIssuerAndTheLegacyLiteralOnly(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, apiFake, _, priv := newGatewayHarness(t)

	for _, issuer := range []string{testPublicBase, legacyTokenIssuer} {
		req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
		req.Header.Set("Authorization", "Bearer "+signTokenWithIssuer(t, priv, issuer))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, issuer)
	}

	for _, issuer := range []string{"", "https://evil.example.com", "saas-starter-2"} {
		apiFake.lastPath = ""
		req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
		req.Header.Set("Authorization", "Bearer "+signTokenWithIssuer(t, priv, issuer))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, 401, w.Code, "should refuse iss=%q", issuer)
		require.Empty(t, apiFake.lastPath)
	}
}

func signTokenWithIssuer(t *testing.T, priv ed25519.PrivateKey, issuer string) string {
	t.Helper()
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   uuid.Must(uuid.NewV7()).String(),
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti",
		},
		OrgID:     uuid.Must(uuid.NewV7()).String(),
		OrgRole:   "admin",
		SessionID: uuid.Must(uuid.NewV7()).String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = accessKeyID(priv.Public().(ed25519.PublicKey))
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

// An MCP token is a session credential, which is what lets the solution SDK
// mint the viewer's Work Context from it. The identity headers the runtime
// receives are the same ones every other authenticated route gets — this is
// the assertion that "the runtime changes nothing for auth" is true.
func TestGateway_MCPToken_StampsTheSameSessionIdentityHeaders(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	// A caller-supplied identity header must not survive the allow decision.
	req.Header.Set("X-User-Id", "00000000-0000-0000-0000-000000000000")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "session", example.lastHeaders.Get("x-credential-kind"))
	require.NotEmpty(t, example.lastHeaders.Get("x-session-id"))
	require.NotEmpty(t, example.lastHeaders.Get("x-user-id"))
	require.NotEqual(t, "00000000-0000-0000-0000-000000000000",
		example.lastHeaders.Get("x-user-id"))
	require.NotEmpty(t, example.lastHeaders.Get("x-org-id"))
	// The client the person came through, so audit reads "person, via client".
	require.Equal(t, "https://claude.ai/oauth/claude-code-client-metadata",
		example.lastHeaders.Get("x-client-id"))
	// The caller's bearer is preserved so the runtime can call the host as them.
	require.Equal(t, "Bearer "+token, example.lastHeaders.Get("authorization"))
}

// The audience classification, directly. Three outcomes, never two:
// "unbound", "bound to this resource" and "I cannot read this" are different
// facts, and an empty string cannot carry the third — it reads as the first,
// which is the classification admitted most widely.
//
// This replaces a test that asserted several resource audiences were read as
// none.
func TestTheAudienceClassificationHasThreeOutcomes(t *testing.T) {
	const host = "saas-starter"
	const good = "https://h.example.com/api/solutions/w/proxy/mcp"

	kind, resource := classifyResourceAudience(jwt.ClaimStrings{host}, host)
	require.Equal(t, resourceUnbound, kind)
	require.Empty(t, resource)

	kind, resource = classifyResourceAudience(jwt.ClaimStrings{host, good}, host)
	require.Equal(t, resourceBound, kind)
	require.Equal(t, good, resource)

	// Two resources: there is no single resource this token is bound to, so no
	// answer to "is it bound to THIS one" is true of it.
	kind, resource = classifyResourceAudience(
		jwt.ClaimStrings{host, good, "https://h.example.com/api/solutions/x/proxy/mcp"}, host)
	require.Equal(t, resourceInvalid, kind)
	require.Empty(t, resource)

	// Shapes the issuer would never produce. The identifier is meant to be
	// byte-exact with the URL a client dialled, so a query, a fragment or
	// userinfo means the audience names something other than what it appears
	// to name — and the verifier must be no looser than the issuer.
	for _, malformed := range []string{
		good + "?x=1",
		good + "#f",
		"https://user@h.example.com/api/solutions/w/proxy/mcp",
		"http://h.example.com/api/solutions/w/proxy/mcp", // plain http, not loopback
		"ftp://h.example.com/api/solutions/w/proxy/mcp",
		"/api/solutions/w/proxy/mcp", // not absolute
		"https:///api/solutions/w/proxy/mcp",
		"mailto:someone@example.com",
		" " + good,
	} {
		kind, resource = classifyResourceAudience(jwt.ClaimStrings{host, malformed}, host)
		require.Equal(t, resourceInvalid, kind, "should be invalid: %q", malformed)
		require.Empty(t, resource, "an invalid binding carries no resource: %q", malformed)
	}

	// Loopback http IS accepted, because local development's origin is not
	// https and the issuer accepts it there too.
	kind, _ = classifyResourceAudience(
		jwt.ClaimStrings{host, "http://localhost:3000/api/solutions/w/proxy/mcp"}, host)
	require.Equal(t, resourceBound, kind)
}

// And an unreadable audience is refused on EVERY path, including the host's own
// API — not only at a solution. A credential whose scope cannot be determined
// has no surface it is known to be good for.
func TestAnUnreadableAudienceIsRefusedEverywhere(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	twoResources := signTwoResourceToken(t, priv,
		testPublicBase+"/api/solutions/example/proxy/mcp",
		testPublicBase+"/api/solutions/other/proxy/mcp")

	for _, path := range []string{
		"/solutions/example/mcp",
		"/solutions/example/v1/items",
		"/v1/users",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+twoResources)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, 401, w.Code, path)
		require.Contains(t, w.Body.String(), "does not name a single valid resource", path)
	}
	require.Empty(t, example.lastPath, "no path may be reached with an unreadable audience")
}

func TestSolutionIDFromPathOnlyMatchesSolutionPaths(t *testing.T) {
	id, ok := solutionIDFromPath("/solutions/example/mcp")
	require.True(t, ok)
	require.Equal(t, "example", id)

	id, ok = solutionIDFromPath("/solutions/example/mcp?x=1")
	require.True(t, ok)
	require.Equal(t, "example", id)

	for _, path := range []string{"/v1/users", "/solutions/", "/solutions/_register", "/"} {
		_, ok := solutionIDFromPath(path)
		require.False(t, ok, "should not be a solution path: %q", path)
	}
}

// A1007B-01. The challenge must name the path a client can actually fetch. On
// a deployed cell the only public route to a solution's backend is the host
// frontend's solution proxy; `/solutions/<id>/*` is this gateway's internal
// surface, and on the public origin that path is a frontend page that redirects
// to login — so the first attempt's challenge sent a client to a login page
// instead of a JSON document.
//
// Spelled against the frontend's own source, because the two are in different
// languages and nothing else makes them agree.
func TestTheChallengeNamesTheFrontendProxyBase(t *testing.T) {
	registry, err := os.ReadFile(filepath.Join("..", "..", "frontend", "code",
		"src", "solutions", "registry.ts"))
	require.NoError(t, err, "the frontend's proxy base must exist where this expects it")

	// registry.ts: `return `/api/solutions/${encodeURIComponent(id)}/proxy`;`
	require.Contains(t, string(registry), "/api/solutions/",
		"solutionProxyBase's prefix must match this gateway's challenge")
	require.Contains(t, string(registry), "/proxy",
		"solutionProxyBase's suffix must match this gateway's challenge")
	require.Equal(t, "/api/solutions/", solutionProxyBase)
	require.Equal(t, "/proxy", solutionProxyMid)

	require.Equal(t,
		"/api/solutions/example/proxy/.well-known/oauth-protected-resource",
		solutionResourceMetadataPath("example"))
}

// r1/f1. The challenge rides on a 401 and ONLY on a 401.
//
// `error="invalid_token"` is an assertion about the caller's credential, and RFC
// 6750 §3 has a client read the challenge on a 403 as well as a 401. Putting it
// on an answer that is not about the credential tells a client to
// re-authenticate where that cannot help: on the 503 this gateway returns when a
// revocation store is unavailable, every refused request would trigger a fresh
// discovery and authorization round against this host's own endpoints — adding
// load while a dependency is already down.
func TestOnlyA401CarriesTheDiscoveryChallenge(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")
	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")

	// A 401 carries it: an unauthenticated caller has nowhere else to begin.
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Header().Get("WWW-Authenticate"), "resource_metadata=")

	// A 503 does not. Verification being unavailable says nothing about the
	// token, and this is a credential that would otherwise be admitted.
	gw.authz.keys = nil
	req = httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 503, w.Code)
	require.Empty(t, w.Header().Get("WWW-Authenticate"),
		"a 503 must not tell a client its token was refused")
}

// r1/f2. With no configured public address there is no absolute URL to name, and
// a relative one is unusable rather than merely weaker: a header carries no
// document base for a client to resolve it against. The challenge still says
// `invalid_token`, because that is what a 401 means.
func TestWithoutAPublicAddressTheChallengeNamesNoMetadataURL(t *testing.T) {
	withPublicBase(t, "")
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	challenge := w.Header().Get("WWW-Authenticate")
	require.Equal(t, `Bearer error="invalid_token"`, challenge)
	require.NotContains(t, challenge, "resource_metadata",
		"a relative metadata URL is not a usable answer")
}

// r1/f3. The public address is resolved ONCE, at construction, so a request's
// outcome does not depend on the environment as it stands at request time.
//
// This is the property a per-request read cannot have. `workspaceEnv` falls back
// to `os.Getenv` when the SDK read fails, so a deployment that supplies
// APP_BASE_URL through the `application` configuration group rather than as a
// process variable sees empty on a transient failure — and because the resource
// identifier is compared exactly, an empty base refuses every resource-bound
// token. That would be one request refused between two that are admitted, with
// no state changed and nothing logged.
//
// The environment is cleared AFTER the gateway is built, which is what such a
// transient read looks like from inside checkJWT. A test that only sets the
// value before construction cannot tell the two implementations apart — reading
// it per request returns the same string — which is why this test moves it.
func TestThePublicAddressIsResolvedOnceAtConstruction(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")
	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")

	t.Setenv("APP_BASE_URL", "")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code,
		"a resource-bound token must not be refused because the environment moved under the process")
	require.Equal(t, "/mcp", example.lastPath)
}
