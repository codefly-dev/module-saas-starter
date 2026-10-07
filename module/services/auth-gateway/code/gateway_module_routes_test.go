package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// Routing a declared module (issue #952).
//
// The three cases the ruling names, each on the real ServeHTTP path rather than
// on the resolver alone: an undeclared prefix is not served, a declared module is
// routed, and an applied tombstone stops the routing.
//
// Every record here is created the way delivery creates one — a declaration with
// a kind — because the kind is the whole mechanism: it is what lets one registry
// feed two surfaces whose admission differs.

// declareModule wires a declared MODULE record into the registry behind gw and
// returns the upstream's capture struct.
func declareModule(t *testing.T, gw *Gateway, alias string) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{body: "module-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	registry.records[alias] = &accountsv1.SolutionRegistration{
		SolutionId: alias,
		Publisher:  "solution:" + alias,
		Revision:   registry.revision,
		Declared: &accountsv1.SolutionDeclaredBinding{
			BindingId:  "acme.test." + alias,
			Generation: 1,
			Release:    "acme/" + alias + "@1.0.0",
			TargetId:   fakeSolutionTarget(alias),
			Kind:       accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE,
		},
		// NO frontend half, which is the real shape: a module has no browser
		// remote and nothing loads a manifest for it. Giving the fake one would
		// hide the rule that makes modules routable at all — requiring both
		// halves leaves every module permanently PENDING.
		Backend: &accountsv1.SolutionBackendBinding{
			Revision: registry.revision, Upstream: srv.URL,
			ServiceAlias: alias, ContractVersion: "v1",
		},
	}
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))
	return fake
}

// A /v1/<prefix>/* path no declaration holds is answered exactly as any other
// unexposed path: 404, with no hint that the prefix was looked up.
//
// That is deliberate and not merely convenient. A distinct answer here would make
// the surface a probe for which modules a deployment runs — an unauthenticated
// caller could enumerate the composition by reading status codes.
func TestGateway_UndeclaredModulePrefixIsNotServed(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	for _, name := range []string{"no bearer", "valid bearer"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/nothing-declares-this/things", nil)
		if name == "valid bearer" {
			req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
		}
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code,
			"%s: an undeclared /v1 prefix must be indistinguishable from any unexposed path", name)
		require.Contains(t, w.Body.String(), "endpoint not exposed", name)
	}
}

// A declared module IS routed: the full /v1/<alias>/... path reaches the declared
// upstream, with the ordinary authenticated pipeline in front of it — identity
// projected from the validated token, caller-supplied identity stripped, the
// caller's bearer preserved, and the gateway's own accounts credential withheld.
func TestGateway_DeclaredModuleIsRoutedThroughTheAuthenticatedPipeline(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := declareModule(t, gw, "example-module")

	// Without a bearer the module is never reached: a declared module adds a
	// proxy target and never widens the authenticated surface.
	noTok := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, noTok)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")

	token := signValidToken(t, priv)
	req := httptest.NewRequest(http.MethodGet, "/v1/example-module/things?limit=5", nil)
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("x-user-id", "attacker")
	req.Header.Set("x-org-role", "super_admin")
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "module-response", w.Body.String())
	// The path is forwarded UNCHANGED: the module owns its own /v1/<alias>
	// surface, unlike a solution whose prefix is stripped.
	require.Equal(t, "/v1/example-module/things", fake.lastPath)
	require.NotEqual(t, "attacker", fake.lastHeaders.Get("x-user-id"))
	require.Equal(t, "admin", fake.lastHeaders.Get("x-org-role"))
	require.Equal(t, "Bearer "+token, fake.lastHeaders.Get("authorization"))
	require.Empty(t, fake.lastHeaders.Get("x-codefly-gateway-token"),
		"the gateway token is an accounts-only capability and must not reach a module upstream")
}

// An applied tombstone STOPS the routing, within the cache bound.
//
// This is the revocation path, and it is the reason the declared row is re-read
// per route instead of being resolved once into a static table: carrier deletion
// is not revocation — a mount going quiet means only that delivery stopped
// talking — and the applied tombstone is. The same record, the same alias, the
// same upstream still listening; what changed is the declaration.
func TestGateway_TombstonedModuleStopsRouting(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := declareModule(t, gw, "example-module")

	token := signValidToken(t, priv)
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusOK, serve().Code, "the module must route first, or the tombstone below proves nothing")

	solutionRegistryFake(t, gw).tombstone("example-module")
	require.NoError(t, gw.solutions.refresh(context.Background()))

	fake.lastHeaders = nil
	w := serve()
	require.Equal(t, http.StatusNotFound, w.Code,
		"a tombstoned module routes exactly like an alias nothing ever declared")
	require.Nil(t, fake.lastHeaders, "the upstream must not be reached after the tombstone")
}

// A record declared as a SOLUTION is refused on the module surface by name, and a
// record declared as a MODULE is refused on the solution surface.
//
// Both halves, because the asymmetry is the point: the module surface carries no
// per-viewer installation admission, so serving a solution there would drop its
// installation check entirely. A 403 verdict rather than a 404 — the record is
// here, and it is not what this surface serves.
func TestGateway_ASurfaceRefusesTheOtherKindByName(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	// A declared SOLUTION, reached on the module surface.
	registerSolutionUpstream(t, gw, "example-go")
	token := signValidToken(t, priv)

	req := httptest.NewRequest(http.MethodGet, "/v1/example-go/things", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "module is not declared on this host")

	// A declared MODULE, reached on the solution surface.
	declareModule(t, gw, "example-module")
	req = httptest.NewRequest(http.MethodGet, "/solutions/example-module/things", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "solution is not declared on this host")
}

// A record whose declaration carries NO kind is served by NEITHER surface.
//
// UNSPECIFIED is what a reader decodes from a writer that did not set the field,
// and it is not a third kind. The permissive reading would be "module", because
// that surface has no installation admission — so this asserts the refusal in
// both directions rather than only the one the implementation happens to check
// first.
func TestGateway_ADeclarationWithNoKindIsServedByNeitherSurface(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := declareModule(t, gw, "example-module")
	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	record := registry.records["example-module"]
	record.Revision = registry.revision
	record.Declared.Kind = accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_UNSPECIFIED
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))

	token := signValidToken(t, priv)
	for path, want := range map[string]string{
		"/v1/example-module/things":        "module is not declared on this host",
		"/solutions/example-module/things": "solution is not declared on this host",
	} {
		fake.lastHeaders = nil
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, path)
		require.Contains(t, w.Body.String(), want, path)
		require.Nil(t, fake.lastHeaders, "%s: the upstream must not be reached", path)
	}
}

// The CATALOG always wins, and an alias it owns is refused rather than
// half-served.
//
// Two separate properties. First, a catalog route is matched before the module
// surface is consulted at all, so a declared alias can never shadow one. Second —
// and this is what the refusal is for — the matcher takes only the paths it
// matches, so a module declared on a catalog-owned prefix would serve exactly the
// paths the catalog happens not to match: one prefix split between two
// authorities, which nobody declared.
func TestGateway_ACatalogOwnedPrefixIsRefusedRatherThanHalfServed(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	fake := declareModule(t, gw, "users")
	token := signValidToken(t, priv)

	// The catalog route wins outright: GET /v1/users is the accounts route.
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "api-response", w.Body.String())
	require.NotNil(t, api.lastHeaders, "the catalog route must be the one served")
	require.Nil(t, fake.lastHeaders, "the declared module must not be reached for a catalog path")

	// And the paths the catalog does NOT match are refused rather than proxied.
	req = httptest.NewRequest(http.MethodGet, "/v1/users/self/declared-module-surface", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "module alias is owned by this host's own catalog")
	require.Nil(t, fake.lastHeaders, "a catalog-owned prefix must never reach a declared module")
}

// A module upstream takes the GUARDED transport, the one that re-validates the
// resolved IPs at dial time.
//
// The address comes out of a delivered document and user bearers are forwarded to
// it, which is the same exposure a solution upstream has. A module route added
// without this would have been the one declared upstream dialled on the plain
// transport, and nothing about the route's shape would have said so.
func TestModuleRoutesTakeTheGuardedUpstreamTransport(t *testing.T) {
	require.True(t, isRuntimeRegisteredRoute(&RouteEntry{Service: moduleServicePrefix + "example-module"}),
		"a declared module upstream must take the resolve-validating transport")
	require.True(t, isRuntimeRegisteredRoute(&RouteEntry{Service: solutionServicePrefix + "example-go"}))
	require.False(t, isRuntimeRegisteredRoute(&RouteEntry{Service: "accounts"}),
		"a static catalog route must not")
}

// A module is SERVING on its backend half alone, and a module with no backend is
// not.
//
// This is the rule that makes modules routable at all. A solution needs both
// halves — it serves a page and a backend — but a module has no browser remote,
// so requiring a frontend would leave every module PENDING for ever: serving
// traffic the gateway refuses, while the registry reports it as waiting for a
// deployment that is not coming.
//
// Both directions, because a check that only ever saw "backend present" would
// pass against a gateway that stopped checking the backend at all.
func TestAModuleIsServingOnItsBackendHalfAlone(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := declareModule(t, gw, "example-module")
	token := signValidToken(t, priv)
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusOK, serve().Code,
		"a declared module with a backend and NO frontend half must route")

	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	record := registry.records["example-module"]
	record.Revision = registry.revision
	record.Backend = nil
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))

	fake.lastHeaders = nil
	w := serve()
	require.Equal(t, http.StatusServiceUnavailable, w.Code,
		"a module with no backend half has nothing to proxy to, and that is not the same fact as being undeclared")
	require.Contains(t, w.Body.String(), "module registration not active")
	require.Nil(t, fake.lastHeaders)
}
