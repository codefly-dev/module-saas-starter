package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	addinClient  = "example-addin"
	addinOrigin  = "https://addin.example"
	portalClient = "example-portal"
	portalOrigin = "https://portal.example"
)

// clientRegistryFake reaches the registry standing in for accounts behind gw.
func clientRegistryFake(t *testing.T, gw *Gateway) *fakeClientRegistry {
	t.Helper()
	registry, ok := gw.clients.client.(*fakeClientRegistry)
	require.True(t, ok, "harness gateway must be wired to the fake client registry")
	return registry
}

// registerClients states the whole registry and refreshes the gateway's view,
// standing in for the configuration accounts loads at startup.
func registerClients(t *testing.T, gw *Gateway, clients ...*accountsv1.RegisteredClient) {
	t.Helper()
	clientRegistryFake(t, gw).setClients(clients...)
	require.NoError(t, gw.clients.refresh(context.Background()))
}

// signClientToken mints an access token issued to a registered client — the
// same shape accounts mints, with `azp` naming the client.
func signClientToken(t *testing.T, priv ed25519.PrivateKey, clientID string) string {
	t.Helper()
	return signClaims(t, priv, accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   uuid.Must(uuid.NewV7()).String(),
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti-" + clientID,
		},
		OrgID:           uuid.Must(uuid.NewV7()).String(),
		OrgRole:         "admin",
		SessionID:       uuid.Must(uuid.NewV7()).String(),
		AuthorizedParty: clientID,
	})
}

// twoRegisteredClients is the shape every binding test needs: two clients that
// each speak from their own origin, so "this token, that client's origin" is
// expressible rather than only "registered vs not".
func twoRegisteredClients(t *testing.T, gw *Gateway) {
	t.Helper()
	registerClients(t, gw,
		registeredClient(addinClient, addinOrigin),
		registeredClient(portalClient, portalOrigin),
	)
}

// HOST-CLIENT-002, the whole story in one test: a registered client calls a
// solution route from its registered origin with nothing but its bearer — no
// internal token, no proxy — and the same token is refused from another
// client's origin and from an origin nobody registered.
func TestCORS_RegisteredClientCallsSolutionOnItsBearerAlone(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)
	solution := registerSolutionUpstream(t, gw, "example")
	token := signClientToken(t, priv, addinClient)

	call := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/solutions/example/search", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w
	}

	served := call(addinOrigin)
	require.Equal(t, http.StatusOK, served.Code)
	require.Equal(t, addinOrigin, served.Header().Get("Access-Control-Allow-Origin"))
	require.Contains(t, served.Header().Values("Vary"), "Origin")
	// Audited with the person and the client: the upstream sees both.
	require.Equal(t, addinClient, solution.lastHeaders.Get("X-Client-Id"))
	require.NotEmpty(t, solution.lastHeaders.Get("X-User-Id"))

	solution.lastHeaders = nil
	fromAnotherClientsOrigin := call(portalOrigin)
	require.Equal(t, http.StatusForbidden, fromAnotherClientsOrigin.Code)
	require.Empty(t, fromAnotherClientsOrigin.Header().Get("Access-Control-Allow-Origin"))
	require.Nil(t, solution.lastHeaders,
		"a refused cross-origin call must not reach the solution at all — an unreadable response still has its effect")

	fromNowhere := call("https://unregistered.example")
	require.Equal(t, http.StatusForbidden, fromNowhere.Code)
	require.Empty(t, fromNowhere.Header().Get("Access-Control-Allow-Origin"))
	require.Nil(t, solution.lastHeaders)
}

// The binding is not specific to the solution surface: a client cannot reach a
// catalog route from an origin it did not register either.
func TestCORS_BindingCoversCatalogRoutes(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)
	token := signClientToken(t, priv, addinClient)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", portalOrigin)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Nil(t, api.lastHeaders)
}

// The host's own frontend proxies server-side and copies the browser's Origin
// header through verbatim, so a host page's ordinary same-site POST arrives
// here carrying an Origin the client registry has never heard of. Such a
// request must be completely untouched by any of this: it belongs to no client,
// so there is nothing to bind it to and nothing to grant it.
func TestCORS_SessionTokenWithForwardedOriginIsUntouched(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("Origin", "https://host.example")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, api.lastHeaders.Get("X-Client-Id"),
		"a token that names no client must not arrive at an upstream carrying one")
}

// x-client-id states what the perimeter verified. A caller that stamps its own
// must have it replaced, or the registry binding and the audit attribution both
// read a value the caller chose.
func TestCORS_SpoofedClientHeaderIsReplaced(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("X-Client-Id", addinClient)
	req.Header.Set("Origin", addinOrigin)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, api.lastHeaders.Get("X-Client-Id"))
	require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"),
		"a spoofed client header must not buy the cross-origin grant its real token cannot")
}

func TestCORS_PreflightIsAnsweredForARegisteredOrigin(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	twoRegisteredClients(t, gw)
	registerSolutionUpstream(t, gw, "example")

	req := httptest.NewRequest(http.MethodOptions, "/solutions/example/search", nil)
	req.Header.Set("Origin", addinOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, addinOrigin, w.Header().Get("Access-Control-Allow-Origin"))
	require.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
	require.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "authorization")
	require.Contains(t, w.Header().Values("Vary"), "Origin")
	// The bearer is the credential, so the host's cookie must never ride along.
	require.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORS_PreflightFromAnUnregisteredOriginIsNotAnswered(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	req := httptest.NewRequest(http.MethodOptions, "/v1/users", nil)
	req.Header.Set("Origin", "https://unregistered.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

// A solution serves its own origin permissively, so a proxied response can
// arrive with an access-control-allow-origin of its own. Two values is what a
// browser refuses outright, and the gateway — not the upstream — is the
// authority for what it granted.
func TestCORS_UpstreamGrantIsReplacedNotDuplicated(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	permissive := &fakeUpstream{body: "solution-response"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		permissive.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	registerSolutionHalves(t, gw, "example", srv.URL)

	req := httptest.NewRequest(http.MethodGet, "/solutions/example/search", nil)
	req.Header.Set("Authorization", "Bearer "+signClientToken(t, priv, addinClient))
	req.Header.Set("Origin", addinOrigin)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{addinOrigin}, w.Header().Values("Access-Control-Allow-Origin"))
	require.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
}

// The public origin an accounts route is told about decides which relying party
// a WebAuthn credential binds to and which redirect an OAuth start may use. A
// registered client establishes it from its own registration, which names the
// client — the frontend's shared-secret path only ever named the process.
func TestCORS_PublicOriginComesFromTheRegistration(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signClientToken(t, priv, addinClient))
	req.Header.Set("Origin", addinOrigin)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, addinOrigin, api.lastHeaders.Get("X-Codefly-Public-Origin"))
	require.Equal(t, "test-gateway-token", api.lastHeaders.Get("X-Codefly-Gateway-Token"))
}

// The frontend still has its door: presenting the internal token alongside the
// origin it resolved server-side keeps establishing the public origin, whether
// or not any client is registered.
func TestCORS_FrontendInternalTokenPathStillEstablishesPublicOrigin(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	authenticateFrontendOrigin(req, "https://host.example")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "https://host.example", api.lastHeaders.Get("X-Codefly-Public-Origin"))
}

// An origin nobody has registered is the common case — every host page's own
// cross-site POST supplies one, and an attacker can supply as many as it likes.
// The refresh floor is what stops each of those turning into a registry read.
func TestClientRegistry_UnknownOriginRefreshesAtMostOncePerFloor(t *testing.T) {
	registry := newFakeClientRegistry(registeredClient(addinClient, addinOrigin))
	cache := newClientRegistryCache(registry)
	now := time.Now()
	cache.now = func() time.Time { return now }
	ctx := context.Background()

	require.NoError(t, cache.refresh(ctx))
	require.Equal(t, 1, registry.calls())

	for i := 0; i < 5; i++ {
		require.False(t, cache.originRegistered(ctx, "https://unregistered.example"))
	}
	require.Equal(t, 1, registry.calls(), "misses inside the floor must collapse onto the snapshot already held")

	now = now.Add(clientRefreshFloor)
	require.False(t, cache.originRegistered(ctx, "https://unregistered.example"))
	require.Equal(t, 2, registry.calls())
}

// A client added to the deployment's configuration reaches a running replica
// without waiting out the reconcile interval: the miss it arrives on is what
// pulls it in, once the refresh floor has passed.
func TestClientRegistry_MissPicksUpANewlyRegisteredClient(t *testing.T) {
	registry := newFakeClientRegistry()
	cache := newClientRegistryCache(registry)
	now := time.Now()
	cache.now = func() time.Time { return now }
	ctx := context.Background()
	require.NoError(t, cache.refresh(ctx))
	require.False(t, cache.registers(ctx, addinClient, addinOrigin))

	registry.setClients(registeredClient(addinClient, addinOrigin))
	now = now.Add(clientRefreshFloor)
	require.True(t, cache.registers(ctx, addinClient, addinOrigin))
}

// An accounts outage must not withdraw cross-origin access from every
// registered client at once.
func TestClientRegistry_ReadFailureKeepsThePreviousSnapshot(t *testing.T) {
	registry := newFakeClientRegistry(registeredClient(addinClient, addinOrigin))
	cache := newClientRegistryCache(registry)
	ctx := context.Background()
	require.NoError(t, cache.refresh(ctx))

	registry.listErr = context.DeadlineExceeded
	require.Error(t, cache.refresh(ctx))
	require.True(t, cache.registers(ctx, addinClient, addinOrigin))
}

// A registry that has never loaded — a gateway built without an accounts
// connection, or one whose every read has failed — grants nothing. That is the
// behaviour the gateway had before any client could be registered.
func TestClientRegistry_NeverLoadedGrantsNothing(t *testing.T) {
	cache := newClientRegistryCache(nil)
	ctx := context.Background()
	require.False(t, cache.originRegistered(ctx, addinOrigin))
	require.False(t, cache.registers(ctx, addinClient, addinOrigin))
}

// A client that never runs in a browser registers no origin, and is granted
// none rather than defaulting to one.
func TestClientRegistry_ClientWithoutOriginsGetsNoGrant(t *testing.T) {
	cache := newClientRegistryCache(newFakeClientRegistry(registeredClient("example-cli")))
	ctx := context.Background()
	require.NoError(t, cache.refresh(ctx))
	require.False(t, cache.registers(ctx, "example-cli", addinOrigin))
	require.False(t, cache.originRegistered(ctx, ""))
}

// A solution's authenticated event stream flushes before it ends, so the grant
// has to be on the wire with the first chunk — a browser that cannot read the
// response until EOF is not streaming. Driven over a real connection because
// the headers only prove that once they have actually been written out.
func TestCORS_StreamedResponseCarriesTheGrantWithItsFirstChunk(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	twoRegisteredClients(t, gw)

	release := make(chan struct{})
	solution := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		http.NewResponseController(w).Flush()
		<-release
	}))
	t.Cleanup(solution.Close)
	registerSolutionHalves(t, gw, "example", solution.URL)

	edge := httptest.NewServer(gw)
	t.Cleanup(edge.Close)
	t.Cleanup(func() { close(release) })

	req, err := http.NewRequest(http.MethodGet, edge.URL+"/solutions/example/events", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+signClientToken(t, priv, addinClient))
	req.Header.Set("Origin", addinOrigin)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	// The response headers arrived while the solution is still holding the
	// stream open, which is the whole point.
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, addinOrigin, resp.Header.Get("Access-Control-Allow-Origin"))

	chunk := make([]byte, len("data: one\n\n"))
	_, err = io.ReadFull(resp.Body, chunk)
	require.NoError(t, err)
	require.Equal(t, "data: one\n\n", string(chunk))
}
