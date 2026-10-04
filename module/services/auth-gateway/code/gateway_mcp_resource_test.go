package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	token := signResourceToken(t, priv, testPublicBase+"/solutions/example/mcp")

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
	require.Contains(t, w.Body.String(), "does not name this resource")
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

	token := signResourceToken(t, priv, testPublicBase+"/solutions/example/mcp")
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

	token := signResourceToken(t, priv, testPublicBase+"/solutions/example/mcp")
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

	token := signResourceToken(t, priv, testPublicBase+"/solutions/example/mcp")
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
func TestGateway_SessionTokenWithNoResource_IsStillAdmitted(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "/mcp", example.lastPath)
}

// A resource at a host this deployment is not. The origin is pinned to
// APP_BASE_URL so a token minted against some other spelling of this solution
// is refused — which is what stops a second deployment's token being replayed
// here.
func TestGateway_MCPToken_MustNameThisHostsOrigin(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, "https://other-host.example.com/solutions/example/mcp")
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Empty(t, example.lastPath)
}

// Without a configured public origin the origin cannot be checked, so only the
// solution the resource names is — which still stops one solution's token
// reaching another, and is the property local development needs.
func TestGateway_WithNoConfiguredOrigin_OnlyTheSolutionIsChecked(t *testing.T) {
	withPublicBase(t, "")
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")
	audit := registerSolutionUpstream(t, gw, "audit")

	token := signResourceToken(t, priv, "http://localhost:3000/solutions/example/mcp")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "/mcp", example.lastPath)

	req = httptest.NewRequest(http.MethodPost, "/solutions/audit/mcp", nil)
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
	require.Contains(t, challenge,
		`resource_metadata="`+testPublicBase+`/.well-known/oauth-protected-resource/solutions/example/mcp"`)

	// Link 2: that document, unauthenticated, naming this host as the
	// authorization server.
	req = httptest.NewRequest(http.MethodGet,
		"/.well-known/oauth-protected-resource/solutions/example/mcp", nil)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	var metadata protectedResourceMetadata
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metadata))
	require.Equal(t, testPublicBase+"/solutions/example/mcp", metadata.Resource)
	require.Equal(t, []string{testPublicBase}, metadata.AuthorizationServers)
	require.Equal(t, []string{"header"}, metadata.BearerMethodsSupported)
	require.Equal(t, []string{"offline_access"}, metadata.ScopesSupported)
}

// An expired or wrong-resource token gets the challenge too, so a client whose
// token went stale re-discovers rather than reporting a flat failure.
func TestGateway_MCPChallengeIsStampedOnAWrongResourceRefusal(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/solutions/audit/mcp")
	req := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
	require.Contains(t, w.Header().Get("WWW-Authenticate"),
		`resource_metadata="`+testPublicBase+`/.well-known/oauth-protected-resource/solutions/example/mcp"`)
}

// The metadata path describes a resource shape, not an inventory. Answering 404
// for an unregistered id would make an unauthenticated endpoint a list of every
// solution the deployment runs; a token for a solution that is not serving
// still meets a 503 at the resource itself, which is the honest answer.
func TestGateway_ProtectedResourceMetadata_DescribesAnyWellFormedSolutionID(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet,
		"/.well-known/oauth-protected-resource/solutions/never-registered/mcp", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/solutions/example",
		"/.well-known/oauth-protected-resource/solutions/example/mcp/tools",
		"/.well-known/oauth-protected-resource/v1/users",
		"/.well-known/oauth-protected-resource/solutions/Example/mcp",
	} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		w = httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, 404, w.Code, "should not describe %q", path)
	}
}

// Without a configured public origin every URL in the document would be a
// guess, and a client that fetched it would send its authorization request to
// whatever it named. Say so rather than publishing one.
func TestGateway_ProtectedResourceMetadata_RefusesWithoutAPublicOrigin(t *testing.T) {
	withPublicBase(t, "")
	gw, _, _, _ := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet,
		"/.well-known/oauth-protected-resource/solutions/example/mcp", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 503, w.Code)
}

// An MCP token is a session credential, which is what lets the solution SDK
// mint the viewer's Work Context from it. The identity headers the runtime
// receives are the same ones every other authenticated route gets — this is
// the assertion that "the runtime changes nothing for auth" is true.
func TestGateway_MCPToken_StampsTheSameSessionIdentityHeaders(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/solutions/example/mcp")
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

// The scope the resource advertises and the scope the authorization server
// issues must be the same string. A resource advertising one its own
// authorization server does not issue sends every client to ask for something
// it cannot get.
func TestProtectedResourceScopeMatchesTheAuthorizationServer(t *testing.T) {
	// accounts' supportedOAuthScope. Spelled out rather than imported: the
	// gateway must not depend on the accounts module, so this is the one place
	// the two are compared, and it fails loudly if either moves.
	require.Equal(t, "offline_access", publishedOAuthScope)
}

// The audience projection, directly. More than one non-host audience is read as
// none: a token this host minted carries at most one resource, so a second is a
// token it did not produce and must not be treated as a binding.
func TestResourceAudienceReadsAtMostOneResource(t *testing.T) {
	require.Empty(t, resourceAudience(jwt.ClaimStrings{"saas-starter"}, "saas-starter"))
	require.Equal(t, "https://h/solutions/w/mcp",
		resourceAudience(jwt.ClaimStrings{"saas-starter", "https://h/solutions/w/mcp"}, "saas-starter"))
	require.Empty(t, resourceAudience(
		jwt.ClaimStrings{"saas-starter", "https://h/solutions/w/mcp", "https://h/solutions/x/mcp"},
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
