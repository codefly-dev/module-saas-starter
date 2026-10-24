package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/workcontext"
	carrier "github.com/codefly-dev/sdk-go/workcontext"
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
		// NO frontend block, which is the real shape: a module has no browser
		// remote and nothing loads a manifest for it. Giving the fake one would
		// hide the rule that makes modules routable at all.
	}
	registry.route(alias, srv.URL)
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
//
// The bearer is asserted PRESERVED, and that is the recorded state of SA-F-BEARER
// rather than agreement with it. An earlier revision of this branch inverted this
// assertion; the withholding is not enabled, because a declared upstream
// authenticates by this header and the audience-bound capability that should
// replace it exists on neither side. See removePersonsSessionCredential in
// gateway.go. What IS asserted below is the cookie, which never travels.
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
// a different path contract, so the surfaces must continue refusing the other
// declared kind even though both now require installation admission. A 403 verdict rather than a 404 — the record is
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
// the declaration is required on both surfaces, so this asserts the refusal in
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
		"a declared module with an addressed endpoint and NO frontend block must route")

	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	record := registry.records["example-module"]
	record.Revision = registry.revision
	testModuleAddresses.Delete(fakeRouteEndpoint("example-module").String())
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))

	fake.lastHeaders = nil
	w := serve()
	require.Equal(t, http.StatusServiceUnavailable, w.Code,
		"a module the composition handed no address for has nothing to proxy to, and that is not the same fact as being undeclared")
	require.Contains(t, w.Body.String(), "module registration not active")
	require.Nil(t, fake.lastHeaders)
}

// ---------------------------------------------------------------------------
// The bearer-less arm (gateway_workload_operation.go): a composed workload's
// headless operation context, presented on the workload endpoint.
// ---------------------------------------------------------------------------

const (
	// operationTenant is the tenant every operation capability here is signed for.
	operationTenant = "11111111-1111-4111-8111-111111111111"
	// publisherSeal is the installation a module operation context is sealed to:
	// the MINTING workload's own installation in the tenant.
	publisherSeal = "22222222-2222-4222-8222-222222222222"
	// targetInstallation is the tenant's installation of the target module, the
	// one a person's capability for it would be sealed to.
	targetInstallation = "33333333-3333-4333-8333-333333333333"
)

// installationsFake answers ListInstallations from pages, page N answering the
// cursor "page-N" (page 0 the empty cursor), and records every request.
type installationsFake struct {
	pages []*accountsv1.ListInstallationsResponse
	err   error
	// endless answers every page with a further cursor.
	endless bool
	calls   []*accountsv1.ListInstallationsRequest
}

func (f *installationsFake) List(
	_ context.Context, req *accountsv1.ListInstallationsRequest,
) (*accountsv1.ListInstallationsResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.endless {
		return &accountsv1.ListInstallationsResponse{NextPageToken: "more"}, nil
	}
	for index, page := range f.pages {
		if req.GetPageToken() == pageCursor(index) {
			return page, nil
		}
	}
	return &accountsv1.ListInstallationsResponse{}, nil
}

func pageCursor(index int) string {
	if index == 0 {
		return ""
	}
	return fmt.Sprintf("page-%d", index)
}

// activeInstallation is one ACTIVE row of operationTenant.
func activeInstallation(id, target string) *accountsv1.InstallationSummary {
	return &accountsv1.InstallationSummary{Installation: &accountsv1.Installation{
		Id: id, OrgId: operationTenant, TargetId: target,
		Status: accountsv1.InstallationStatus_INSTALLATION_STATUS_ACTIVE,
	}}
}

// tenantInstalled is a tenant that installed the target module (E) and the
// publishing workload (W).
func tenantInstalled() *installationsFake {
	return &installationsFake{pages: []*accountsv1.ListInstallationsResponse{{
		Installations: []*accountsv1.InstallationSummary{
			activeInstallation(targetInstallation, fakeSolutionTarget("documents")),
			activeInstallation(publisherSeal, fakeSolutionTarget("publisher")),
		},
	}}}
}

// operationHarness is a gateway with the edge verifier, the module "documents"
// declared, and a tenant that installed it and the publishing workload.
func operationHarness(t *testing.T) (gw *Gateway, module *fakeUpstream, installations *installationsFake, kid string, wcPriv ed25519.PrivateKey) {
	t.Helper()
	gw, _, _, _ = newGatewayHarness(t)
	pub, wcPriv := mustEd25519(t)
	kid = "key-1"
	jwks := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)
	gw.workContext = newWorkContextVerifier(jwks.URL)
	module = declareModule(t, gw, "documents")
	installations = tenantInstalled()
	gw.installations = installations
	return gw, module, installations, kid, wcPriv
}

// operationCapability is a module operation context for audience, sealed to seal.
func operationCapability(t *testing.T, kid string, priv ed25519.PrivateKey, audience, seal string) string {
	t.Helper()
	return mintCapability(t, kid, priv, edgeCapability{
		Audience: audience, Tenant: operationTenant, Owner: "module-principal", Session: "session-op", Installation: seal,
	})
}

func publishRequest(capability string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/documents/definitions:publish", strings.NewReader(`{"bundle":"b"}`))
	if capability != "" {
		req.Header.Set(carrier.HeaderName, capability)
	}
	return req
}

// captureLog sends the standard logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

// THE ARM ADMITS A HEADLESS OPERATION CONTEXT. No bearer, the workload endpoint,
// a capability for this alias sealed to the publisher's own installation (W)
// in a tenant that installed the target (E, not W).
//
// What the upstream holds is the capability and the one header stating its
// seal, and nothing that names a person. ext_authz is not consulted: it denies
// every request without a bearer, so a 200 here is a request it never saw, and
// it stamps x-user-id and x-credential-kind on every allow, which are absent.
func TestModuleRoute_AWorkloadOperationContextIsAdmittedWithoutABearer(t *testing.T) {
	gw, module, installations, kid, wcPriv := operationHarness(t)
	logs := captureLog(t)
	capability := operationCapability(t, kid, wcPriv, "documents", publisherSeal)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, onWorkloadEndpoint(publishRequest(capability)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, module.calls)
	require.Equal(t, "/v1/documents/definitions:publish", module.lastPath, "the path is forwarded unchanged")
	require.Equal(t, `{"bundle":"b"}`, module.lastBody)
	require.Equal(t, capability, module.lastHeaders.Get(carrier.HeaderName), "the capability is forwarded as presented")
	require.Equal(t, publisherSeal, module.lastHeaders.Get(installationConfinementHeader),
		"the one stamped header is the capability's own seal")
	for _, header := range []string{
		"Authorization", "Cookie", "Proxy-Authorization", "X-User-Id", "X-Org-Id", "X-Session-Id", "X-Scopes",
		"X-Credential-Kind", resourceConfinementHeader, "X-Codefly-Gateway-Token",
	} {
		require.Empty(t, module.lastHeaders.Values(header), "%s must not reach the upstream on the bearer-less arm", header)
	}

	require.Len(t, installations.calls, 1)
	require.Equal(t, operationTenant, installations.calls[0].GetOrgId(), "the tenant is the capability's, signed")
	require.Equal(t, accountsv1.InstallationStatus_INSTALLATION_STATUS_ACTIVE, installations.calls[0].GetStatus())

	line := logs.String()
	require.Contains(t, line, "reason=workload_operation_admit")
	for _, fact := range []string{`alias="documents"`, `tenant="` + operationTenant + `"`, `task="task-1"`, `seal="` + publisherSeal + `"`, `presenter="local"`} {
		require.Contains(t, line, fact)
	}
	require.NotContains(t, line, capability, "the token is never logged")
}

// Every refusal, each with one fact broken and the rest held equal.
func TestModuleRoute_TheBearerLessArmRefuses(t *testing.T) {
	for name, test := range map[string]struct {
		prepare func(t *testing.T, gw *Gateway, installations *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request
		code    int
		body    string
		asked   bool
	}{
		// The arm is the workload endpoint's alone: the public listener never
		// selects it, so the request takes the person path and has no bearer.
		"the same request on the public listener": {
			prepare: func(t *testing.T, _ *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				return publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal))
			},
			code: http.StatusUnauthorized, body: "authentication required",
		},
		// A module's own identity verifies at this route, and is never a
		// headless operation context.
		"a module identity capability": {
			prepare: func(t *testing.T, _ *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, moduleCapabilitiesAudience, publisherSeal)))
			},
			code: http.StatusUnauthorized, body: "authentication required",
		},
		"a capability for another module": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				declareModule(t, gw, "ledgers")
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "ledgers", publisherSeal)))
			},
			code: http.StatusUnauthorized, body: "invalid work context",
		},
		// A person's capability for this module is sealed to the target's own
		// installation, and without its bearer it is answered as it always was.
		"a person-sealed capability": {
			prepare: func(t *testing.T, _ *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", targetInstallation)))
			},
			code: http.StatusUnauthorized, body: "authentication required", asked: true,
		},
		"a seal that names no active installation in the tenant": {
			prepare: func(t *testing.T, _ *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", "44444444-4444-4444-8444-444444444444")))
			},
			code: http.StatusUnauthorized, body: "authentication required", asked: true,
		},
		"a tenant with no active installation of the target": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				gw.installations = &installationsFake{pages: []*accountsv1.ListInstallationsResponse{{
					Installations: []*accountsv1.InstallationSummary{activeInstallation(publisherSeal, fakeSolutionTarget("publisher"))},
				}}}
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
			},
			code: http.StatusForbidden, body: "module is not installed for this organization",
		},
		"a route that resolves no target": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				registry := solutionRegistryFake(t, gw)
				registry.mu.Lock()
				registry.revision++
				record := registry.records["documents"]
				record.Revision = registry.revision
				record.Declared.TargetId = ""
				registry.mu.Unlock()
				require.NoError(t, gw.solutions.refresh(context.Background()))
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
			},
			code: http.StatusForbidden, body: "module is not installed for this organization",
		},
		"an authority that errors": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				gw.installations = &installationsFake{err: errors.New("accounts down: dial tcp 10.0.0.7:9090")}
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
			},
			code: http.StatusServiceUnavailable, body: "module installation authority unavailable",
		},
		"a cursor that never terminates": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				gw.installations = &installationsFake{endless: true}
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
			},
			code: http.StatusServiceUnavailable, body: "module installation authority unavailable",
		},
		"no installation authority wired": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				gw.installations = nil
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
			},
			code: http.StatusServiceUnavailable, body: "module installation authority unavailable",
		},
		// No edge verifier means no inspected capability, so the arm is never
		// selected and the person path answers.
		"no edge verifier": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				capability := operationCapability(t, kid, wcPriv, "documents", publisherSeal)
				gw.workContext = nil
				return onWorkloadEndpoint(publishRequest(capability))
			},
			code: http.StatusUnauthorized, body: "authentication required",
		},
		"a cookie and no bearer": {
			prepare: func(t *testing.T, _ *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				req := onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
				req.Header.Set("Cookie", "session=abc")
				return req
			},
			code: http.StatusUnauthorized, body: "a session cookie is not a credential at this surface",
		},
		// On a cell the listener requires the waypoint presenter; the arm
		// requires it again, so a listener wired without it admits nothing.
		"a cell gateway with no presenter": {
			prepare: func(t *testing.T, gw *Gateway, _ *installationsFake, kid string, wcPriv ed25519.PrivateKey) *http.Request {
				gw.requireWorkloadPresenter = true
				return onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
			},
			code: http.StatusUnauthorized, body: "authentication required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			gw, module, installations, kid, wcPriv := operationHarness(t)
			logs := captureLog(t)
			req := test.prepare(t, gw, installations, kid, wcPriv)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, test.code, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), test.body)
			require.Zero(t, module.calls, "nothing reaches the upstream")
			if test.asked {
				require.Len(t, installations.calls, 1)
			}
			require.NotContains(t, logs.String(), req.Header.Get(carrier.HeaderName), "no refusal logs the token")
			require.NotContains(t, w.Body.String(), "10.0.0.7", "no refusal carries the authority's error")
			require.NotContains(t, logs.String(), "10.0.0.7", "nor logs it")
		})
	}
}

// Every refusal ON THE ARM is a log line naming the signed facts and the
// presenter, because the arm runs no ext_authz and this line is its only record.
func TestModuleRoute_TheBearerLessArmLogsEachRefusal(t *testing.T) {
	gw, _, _, kid, wcPriv := operationHarness(t)
	logs := captureLog(t)
	capability := operationCapability(t, kid, wcPriv, "documents", targetInstallation)
	req := onWorkloadEndpoint(publishRequest(capability))
	req = req.WithContext(context.WithValue(req.Context(), workloadPresenterKey{}, "spiffe://cluster.local/ns/apps/sa/publisher"))

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	line := logs.String()
	require.Contains(t, line, "reason=workload_operation_person_sealed")
	for _, fact := range []string{`alias="documents"`, `tenant="` + operationTenant + `"`, `task="task-1"`, `seal="` + targetInstallation + `"`, `presenter="spiffe://cluster.local/ns/apps/sa/publisher"`} {
		require.Contains(t, line, fact)
	}
	require.NotContains(t, line, capability)
}

// An upstream failure on the arm answers a static 502: the dial detail stays in
// the gateway's log.
func TestModuleRoute_TheBearerLessArmAnswersAStaticBadGateway(t *testing.T) {
	gw, _, _, kid, wcPriv := operationHarness(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	registry.records["documents"].Revision = registry.revision
	registry.route("documents", deadURL)
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal))))

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Equal(t, "upstream error", strings.TrimSpace(w.Body.String()))
	require.NotContains(t, w.Body.String(), strings.TrimPrefix(deadURL, "http://"))
}

// The edge check still runs first: a capability the Inspector refuses never
// reaches the arm, whatever its seal.
func TestModuleRoute_TheBearerLessArmRefusesWhatTheEdgeRefuses(t *testing.T) {
	for name, mint := range map[string]func(t *testing.T, kid string, wcPriv ed25519.PrivateKey) string{
		"expired": func(t *testing.T, kid string, wcPriv ed25519.PrivateKey) string {
			return mintCapability(t, kid, wcPriv, edgeCapability{
				Audience: "documents", Tenant: operationTenant, Session: "s", Installation: publisherSeal,
				Now: func() time.Time { return time.Now().Add(-time.Hour) },
			})
		},
		"a forged signature": func(t *testing.T, kid string, _ ed25519.PrivateKey) string {
			_, attacker := mustEd25519(t)
			return operationCapability(t, kid, attacker, "documents", publisherSeal)
		},
		"a foreign issuer": func(t *testing.T, kid string, wcPriv ed25519.PrivateKey) string {
			return mintCapability(t, kid, wcPriv, edgeCapability{
				Audience: "documents", Tenant: operationTenant, Session: "s", Installation: publisherSeal, Issuer: "https://issuer.example.com",
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			gw, module, installations, kid, wcPriv := operationHarness(t)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, onWorkloadEndpoint(publishRequest(mint(t, kid, wcPriv))))

			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.Contains(t, w.Body.String(), "invalid work context")
			require.Zero(t, module.calls)
			require.Empty(t, installations.calls)
		})
	}
}

// An empty tenant or seal cannot come from core: its mint and its decode path
// both refuse one (tenant_id is required, and every capability is sealed). The
// arm checks both anyway, before anything is stripped or asked, and this holds
// that check to its answer with the only empty capability that can be built.
func TestModuleRoute_TheBearerLessArmRefusesACapabilityWithNoTenantOrSeal(t *testing.T) {
	gw, module, installations, _, _ := operationHarness(t)
	routing, resolution := gw.solutions.resolveModuleRouting(context.Background(), "documents")
	require.Equal(t, solutionRoutable, resolution)

	w := httptest.NewRecorder()
	gw.serveWorkloadOperation(w, onWorkloadEndpoint(publishRequest("")), "documents", routing, &workcontext.Inspected{})

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "invalid work context")
	require.Zero(t, module.calls)
	require.Empty(t, installations.calls)
}

// The two facts may sit on different pages: the arm follows the cursor until it
// holds both, and a row the authority should not have returned (another tenant,
// not ACTIVE) counts for neither.
func TestModuleRoute_TheBearerLessArmReadsEveryPageItNeeds(t *testing.T) {
	gw, module, _, kid, wcPriv := operationHarness(t)
	foreign := activeInstallation(publisherSeal, fakeSolutionTarget("publisher"))
	foreign.Installation.OrgId = "66666666-6666-4666-8666-666666666666"
	installations := &installationsFake{pages: []*accountsv1.ListInstallationsResponse{
		{Installations: []*accountsv1.InstallationSummary{activeInstallation(targetInstallation, fakeSolutionTarget("documents")), foreign}, NextPageToken: pageCursor(1)},
		{Installations: []*accountsv1.InstallationSummary{activeInstallation(publisherSeal, fakeSolutionTarget("publisher"))}},
	}}
	gw.installations = installations

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal))))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, module.calls)
	require.Len(t, installations.calls, 2)
	require.Equal(t, pageCursor(1), installations.calls[1].GetPageToken())

	// The seal only on the foreign row: not an active installation of THIS tenant.
	installations.pages = installations.pages[:1]
	installations.pages[0].NextPageToken = ""
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal))))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, 1, module.calls)
}
