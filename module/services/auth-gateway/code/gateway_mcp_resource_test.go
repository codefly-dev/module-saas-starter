package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// Without a configured public origin the origin cannot be checked, so on the
// product's own API surface only the solution the resource names is — which
// still stops one solution's token reaching another.
//
// The TOOL endpoint does not share this leniency: it fails closed instead (see
// TestGateway_TheToolEndpointFailsClosedWithoutAConfiguredPublicAddress). The
// difference is deliberate. Here the check is a narrowing of a token that was
// already accepted; there it is the whole admission rule, and a rule that
// cannot be evaluated must refuse rather than wave the request through.
func TestGateway_WithNoConfiguredOrigin_OnlyTheSolutionIsChecked(t *testing.T) {
	withPublicBase(t, "")
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")
	audit := registerSolutionUpstream(t, gw, "audit")

	token := signResourceToken(t, priv, "http://localhost:3000/api/solutions/example/proxy/mcp")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/v1/items", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "/v1/items", example.lastPath)

	req = httptest.NewRequest(http.MethodPost, "/solutions/audit/v1/items", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
	require.Empty(t, audit.lastPath)
}

// The discovery chain an MCP client runs, in order: 401 with a challenge →
// protected-resource metadata → the authorization server's own metadata. The
// first two links are this gateway's; the third is the accounts document the
// frontend publishes at /.well-known/oauth-authorization-server.
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

// The audience projection, directly. More than one non-host audience is read as
// none: a token this host minted carries at most one resource, so a second is a
// token it did not produce and must not be treated as a binding.
func TestResourceAudienceReadsAtMostOneResource(t *testing.T) {
	require.Empty(t, resourceAudience(jwt.ClaimStrings{"saas-starter"}, "saas-starter"))
	require.Equal(t, "https://h/api/solutions/w/proxy/mcp",
		resourceAudience(jwt.ClaimStrings{"saas-starter", "https://h/api/solutions/w/proxy/mcp"}, "saas-starter"))
	require.Empty(t, resourceAudience(
		jwt.ClaimStrings{"saas-starter", "https://h/api/solutions/w/proxy/mcp", "https://h/api/solutions/x/proxy/mcp"},
		"saas-starter"))
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
