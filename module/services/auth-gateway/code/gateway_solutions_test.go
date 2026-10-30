package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestGateway_SignedFrontendPayloadNeverFallsBackToMutableBackend(t *testing.T) {
	for _, kind := range []accountsv1.SolutionDeclaredKind{
		accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION,
		accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			gw, _, _, _ := newGatewayHarness(t)
			upstream := registerSolutionUpstream(t, gw, "example")
			record := solutionRegistryFake(t, gw).records["example"]
			record.Declared.Kind = kind
			var frontend solutionhost.Frontend
			require.NoError(t, json.Unmarshal([]byte(record.Frontend.Manifest), &frontend))
			frontend.Payload = &solutionhost.FrontendPayload{
				Artifact: "frontend", Digest: solutionhost.PayloadDigest("sha256:" + strings.Repeat("ab", 32)), MediaType: "application/x-tar",
			}
			encoded, err := json.Marshal(frontend)
			require.NoError(t, err)
			record.Frontend.Manifest = string(encoded)
			require.NoError(t, gw.solutions.refresh(context.Background()))
			_, advertised := solutionManifest(record, false)
			require.False(t, advertised)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, path := range []string{"assets", "assets/mf-manifest.json", "assets/deferred-chunk.js"} {
					upstream.lastHeaders = nil
					w := httptest.NewRecorder()
					gw.ServeHTTP(w, httptest.NewRequest(method, "/solutions/example/"+path, nil))
					require.Equal(t, http.StatusServiceUnavailable, w.Code)
					require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
					require.Nil(t, upstream.lastHeaders, "unverified executable bytes must not reach the host origin")
				}
			}
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/solutions/example/.well-known/capabilities", nil))
			require.Equal(t, http.StatusOK, w.Code, "backend capability discovery is independent of frontend preparation")
		})
	}
}

// registerSolutionUpstream wires a declared, serving solution — its presence
// applied with a frontend block, its endpoint addressed by the composition —
// into the registry behind gw and returns the upstream's capture struct. Both
// facts matter: a solution missing either is deliberately not routable.
func registerSolutionUpstream(t *testing.T, gw *Gateway, id string) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{body: "solution-response"}
	srv := newSolutionTestServer(t, fake)
	declareSolution(t, gw, id, srv.URL)
	return fake
}

// newSolutionTestServer serves a solution's backend the way the gateway now
// reaches one: plain HTTP at the address the composition resolved, inside the
// cell's mesh.
func newSolutionTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// solutionRegistryFake reaches the registry standing in for accounts behind gw.
func solutionRegistryFake(t *testing.T, gw *Gateway) *fakeSolutionRegistry {
	t.Helper()
	registry, ok := gw.solutions.client.(*fakeSolutionRegistry)
	require.True(t, ok, "harness gateway must be wired to the fake registry")
	return registry
}

// declareSolution writes a declared solution record whose applied presence
// carries a frontend block, addresses its endpoint at upstream the way the
// composition's carrier would, and refreshes the gateway's cache.
func declareSolution(t *testing.T, gw *Gateway, id, upstream string) {
	t.Helper()
	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.route(id, upstream)
	registry.revision++
	registry.records[id] = &accountsv1.SolutionRegistration{
		SolutionId: id, Publisher: "solution:" + id, Revision: registry.revision,
		Frontend: &accountsv1.SolutionFrontendBinding{Revision: registry.revision, Manifest: fakeFrontendBlock(id)},
		Declared: &accountsv1.SolutionDeclaredBinding{BindingId: "acme.test." + id, Generation: 1, Release: "acme/" + id + "@1.0.0", TargetId: fakeSolutionTarget(id),
			Kind: accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION},
	}
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))
}

// A registered solution is still ALWAYS auth-required: no bearer means the
// ext_authz Check denies and the upstream is never reached.
func TestGateway_Solution_NoToken_Denied(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "solution upstream must NEVER be reached without auth")
}

// An unregistered solution is rejected before any auth work, with 502.
func TestGateway_Solution_Unregistered_BadGateway(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/solutions/ghost/v1/thing", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "not registered")
}

// A solution's static Module-Federation surface (/assets/*) and public
// discovery documents (/.well-known/*) are served unauthenticated for reads: a
// browser's module loader fetches the MF manifest, remote entry, and chunks
// with no bearer, so gating them would break same-origin loading.
func TestGateway_Solution_PublicSurface_NoToken_OK(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")

	for _, path := range []string{
		"/assets/mf-manifest.json",
		"/assets/remoteEntry.js",
		"/assets/chunks/app.1a2b3c.js",
		"/assets",
		"/.well-known/capabilities",
	} {
		req := httptest.NewRequest(http.MethodGet, "/solutions/example-go"+path, nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "path %q must be served without a bearer", path)
		require.Equal(t, "solution-response", w.Body.String())
		require.Equal(t, path, fake.lastPath, "path is rewritten to the solution suffix")
	}
}

// A non-canonical public path is forwarded to the upstream in cleaned form, so
// the path the gateway decided was public and the path the upstream resolves
// can't diverge (e.g. an upstream that reads the raw dot segments differently).
func TestGateway_Solution_PublicSurface_ForwardsCleanedPath(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/example-go/assets/./chunks/../app.1a2b3c.js", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "/assets/app.1a2b3c.js", fake.lastPath)
}

// The public read exemption never forwards caller-supplied identity: a request
// spoofing identity headers reaches the upstream with them stripped.
func TestGateway_Solution_PublicSurface_StripsSpoofedIdentity(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/example-go/assets/mf-manifest.json", nil)
	req.Header.Set("x-user-id", "attacker")
	req.Header.Set("x-org-role", "super_admin")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, fake.lastHeaders.Get("x-user-id"))
	require.Empty(t, fake.lastHeaders.Get("x-org-role"))
}

// The exemption is read-only: a non-GET/HEAD request to the public surface is
// still auth-required, so it cannot be used as an unauthenticated write path.
func TestGateway_Solution_PublicSurface_NonReadStillAuthRequired(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")

	req := httptest.NewRequest(http.MethodPost, "/solutions/example-go/assets/mf-manifest.json", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")
}

// A traversal suffix cannot borrow the /assets exemption to reach an
// authenticated endpoint: the decision is made on the cleaned path.
func TestGateway_Solution_PublicSurface_TraversalStillAuthRequired(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/example-go/assets/../example", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")
}

// A solution's data endpoints stay auth-required: /example needs a valid
// bearer for both GET and POST, and reaches the upstream once presented.
func TestGateway_Solution_DataEndpoint_RequiresBearer(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		fake.lastHeaders = nil
		noTok := httptest.NewRequest(method, "/solutions/example-go/example", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, noTok)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s /example without a bearer must be denied", method)
		require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")

		withTok := httptest.NewRequest(method, "/solutions/example-go/example", nil)
		withTok.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
		w = httptest.NewRecorder()
		gw.ServeHTTP(w, withTok)
		require.Equal(t, http.StatusOK, w.Code, "%s /example with a valid bearer is forwarded", method)
		require.Equal(t, "/example", fake.lastPath)
	}
}

// A valid JWT is projected into identity headers exactly as for a catalog
// route, the path is rewritten to the solution suffix, and the caller's bearer
// is preserved so the solution can call downstream services on the user's
// behalf.
func TestGateway_Solution_ValidJWT_ForwardsWithIdentity(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	token := signValidToken(t, priv)
	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs?limit=5", nil)
	req.Header.Set("authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "solution-response", w.Body.String())
	// Path is rewritten to the suffix after /solutions/{id}.
	require.Equal(t, "/v1/audit/logs", fake.lastPath)
	// Identity is projected from the validated JWT.
	require.NotEmpty(t, fake.lastHeaders.Get("x-user-id"))
	require.NotEmpty(t, fake.lastHeaders.Get("x-org-id"))
	require.Equal(t, "admin", fake.lastHeaders.Get("x-org-role"))
	// The caller's bearer DOES travel to the solution, which is the recorded
	// state of SA-F-BEARER and not agreement with it: the solution authenticates
	// by it and nothing mints the audience-bound capability that should replace
	// it. See removePersonsSessionCredential in gateway.go. The cookie does not
	// travel, and TestSolutionUpstreamReceivesNoCookie asserts that.
	require.Equal(t, "Bearer "+token, fake.lastHeaders.Get("authorization"))
	// The gateway token is an accounts-only capability and must not leak to a
	// solution upstream.
	require.Empty(t, fake.lastHeaders.Get("x-codefly-gateway-token"))
}

// Caller-supplied identity headers are stripped and re-stamped from the token,
// never trusted as presented — same discipline as every protected route.
func TestGateway_Solution_StripsSpoofedIdentity(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("x-user-id", "attacker")
	req.Header.Set("x-org-role", "super_admin")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotEqual(t, "attacker", fake.lastHeaders.Get("x-user-id"))
	require.Equal(t, "admin", fake.lastHeaders.Get("x-org-role"))
}

// A solution backend may transcode REST to gRPC the way accounts does, so the
// Grpc-Metadata- spelling of an identity header is dropped here too.
func TestGateway_Solution_StripsGRPCMetadataPrefixedHeaders(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	setGRPCMetadataSpellings(req, "attacker")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	requireNoGRPCMetadataHeaders(t, fake.lastHeaders)
}

// A request with no solution id after the prefix is a 404, not a proxy attempt.
func TestGateway_Solution_MissingID_NotFound(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/solutions/", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// The authenticated solution data path consumes the same per-org/per-IP budget
// as an equivalent catalog route: it must route through rateLimitThenProxy, not
// proxy directly. Without that, /solutions/<id>/* would be an unmetered proxy an
// authenticated caller could flood past the org budget. Regression for #513,
// mirroring TestGateway_Module_Federated_RateLimited (#512).
func TestGateway_Solution_RateLimited(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	// effective budget = limit(1) + burst(max(1/5,1)=1) = 2 requests / org / min.
	gw.rateLimiter = newInProcessRateLimiter(1)
	fake := registerSolutionUpstream(t, gw, "audit")

	// Reuse ONE token so every request keys on the same injected x-org-id.
	token := signValidToken(t, priv)
	got429 := false
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.True(t, got429, "authenticated solution data route must be subject to the rate-limit budget")
	// The earlier allowed requests still reached the solution upstream.
	require.NotNil(t, fake.lastHeaders)
	require.Equal(t, "/v1/audit/logs", fake.lastPath)
}

// The public static surface is metered too — IP-keyed, exactly like every public
// catalog route — so it is not an unmetered unauthenticated proxy. Guards against
// a regression to bare proxyTo on the public branch (#513).
func TestGateway_Solution_PublicSurface_RateLimited(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	// effective budget = limit(1) + burst(max(1/5,1)=1) = 2 requests / key / min.
	gw.rateLimiter = newInProcessRateLimiter(1)
	fake := registerSolutionUpstream(t, gw, "audit")

	got429 := false
	for i := 0; i < 5; i++ {
		// No bearer: identity is stripped, so the limiter keys on the client IP,
		// which httptest holds constant across these requests.
		req := httptest.NewRequest(http.MethodGet, "/solutions/audit/assets/remoteEntry.js", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.True(t, got429, "public solution asset surface must carry an IP-keyed budget, not be an unmetered proxy")
	require.Equal(t, "/assets/remoteEntry.js", fake.lastPath)
}

// failingLimiter builds a rate limiter whose backend always errors, to exercise
// the limiter-backend-outage (e.g. Redis down) failure path. Mirrors the
// construction in ratelimit_failure_test.go.
func failingLimiter() *RateLimiter {
	return &RateLimiter{
		backend: failingRateLimitBackend{},
		limit:   1000,
		burst:   200,
		stop:    make(chan struct{}),
		proxies: newProxyTrust(""),
	}
}

// When the limiter backend is unavailable, the authenticated data path fails
// OPEN and still proxies — it does NOT 503. This pins the deliberate class-policy
// choice (only authentication/MFA budgets fail closed); flipping this route to
// fail closed, or to a fail-closed class, would break this test.
func TestGateway_Solution_DataPath_FailsOpenOnLimiterOutage(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	gw.rateLimiter = failingLimiter()
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "authenticated solution data route must fail open on limiter-backend outage")
	require.Equal(t, "/v1/audit/logs", fake.lastPath, "the request must still reach the upstream when failing open")
}

// The public static surface also fails OPEN on a limiter-backend outage, so a
// Redis blip never blocks module-loader asset/manifest fetches.
func TestGateway_Solution_PublicSurface_FailsOpenOnLimiterOutage(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	gw.rateLimiter = failingLimiter()
	fake := registerSolutionUpstream(t, gw, "audit")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/assets/remoteEntry.js", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "public solution asset surface must fail open on limiter-backend outage")
	require.Equal(t, "/assets/remoteEntry.js", fake.lastPath, "the request must still reach the upstream when failing open")
}

// --- durable registry behaviour (#534) ---

// A headless solution serves its protected backend without advertising a page.
func TestGateway_Solution_NoFrontendBlock_BackendRoutable(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	upstream := registerSolutionUpstream(t, gw, "audit")
	registry := solutionRegistryFake(t, gw)
	registry.records["audit"].Frontend = nil
	require.NoError(t, gw.solutions.refresh(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "/v1/ping", upstream.lastPath)
}

// A declared solution the composition handed this gateway no address for is
// registered and not serving: the Codefly SDK answers for its endpoint or
// nothing does, and nothing on the record is a fallback.
func TestGateway_Solution_NoCarrier_NotRoutable(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	declareSolution(t, gw, "audit", "http://example.svc")
	testModuleAddresses.Delete(fakeRouteEndpoint("audit").String())
	require.NoError(t, gw.solutions.refresh(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "not active")
}

// A snapshot the registry has not confirmed within the revocation lease stops
// authorising routes: a withdrawal applied during the outage would be invisible
// here. The answer names the stale registry, so an operator can tell it from
// a replica that never loaded one, and routes resume on the next read that
// succeeds. A tombstone already held keeps answering as the registry would.
func TestGateway_Solution_StaleSnapshotStopsAuthorisingRoutes(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	registry := solutionRegistryFake(t, gw)
	registry.seedDeclared("gone", "http://gone.svc")
	registry.tombstone("gone")
	require.NoError(t, gw.solutions.refresh(context.Background()))

	serve := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusOK, serve("/solutions/audit/v1/ping").Code)

	// The registry goes away, and the snapshot ages past the lease.
	registry.listErr = grpcstatus.Error(codes.Unavailable, "registry down")
	gw.solutions.now = func() time.Time { return time.Now().Add(solutionRegistryLease + time.Second) }
	fake.lastHeaders = nil
	w := serve("/solutions/audit/v1/ping")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "stale")
	require.Nil(t, fake.lastHeaders, "nothing is forwarded on a stale snapshot")
	_, resolution := gw.solutions.resolveRouting(context.Background(), "gone")
	require.Equal(t, solutionUnregistered, resolution, "a withdrawal already held is still a withdrawal")

	// The registry answers again: the next read that succeeds — the reconcile
	// tick, here — renews the lease, and routes resume on the snapshot it read.
	registry.listErr = nil
	require.NoError(t, gw.solutions.refresh(context.Background()))
	require.Equal(t, http.StatusOK, serve("/solutions/audit/v1/ping").Code)
	require.NotNil(t, fake.lastHeaders)
}

// A registration survives the process that received it. A fresh cache — what a
// restarted replica starts with — rebuilds from the registry and routes with
// nobody re-registering.
func TestGateway_Solution_RestartRebuildsFromRegistry(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	declareSolution(t, gw, "audit", "http://10.0.0.7:8080")
	registry := solutionRegistryFake(t, gw)

	restarted := newSolutionRegistryCache(registry)
	routing, resolution := restarted.resolveRouting(context.Background(), "audit")

	require.Equal(t, solutionRoutable, resolution)
	require.Equal(t, "10.0.0.7:8080", routing.Upstream.Host)
}

// A registration made through one replica reaches the others: the second
// replica's reconcile converges it onto the same registry revision.
func TestGateway_Solution_ReplicasConvergeOnSameRevision(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registry := solutionRegistryFake(t, gw)
	other := newSolutionRegistryCache(registry)
	require.NoError(t, other.refresh(context.Background()))

	declareSolution(t, gw, "audit", "http://10.0.0.7:8080")

	_, otherRevision, _, _ := other.snapshot()
	_, writerRevision, _, _ := gw.solutions.snapshot()
	require.NotEqual(t, writerRevision, otherRevision, "the second replica has not seen the write yet")

	require.NoError(t, other.refresh(context.Background()))
	_, otherRevision, loaded, _ := other.snapshot()
	require.True(t, loaded)
	require.Equal(t, writerRevision, otherRevision, "both replicas converge on one registry revision")

	_, resolution := other.resolveRouting(context.Background(), "audit")
	require.Equal(t, solutionRoutable, resolution)
}

// A request for a solution this replica has not seen triggers one on-demand
// refresh, so a registration that landed on another replica is reachable here
// without waiting out the reconcile interval — and the refresh floor keeps a
// flood of misses from becoming a flood of registry reads.
func TestGateway_Solution_CacheMissRefreshesOnceWithinFloor(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registry := solutionRegistryFake(t, gw)
	lagging := newSolutionRegistryCache(registry)
	require.NoError(t, lagging.refresh(context.Background()))
	declareSolution(t, gw, "audit", "http://10.0.0.7:8080")

	// Past the refresh floor, so the miss is allowed to consult the registry.
	lagging.now = func() time.Time { return time.Now().Add(solutionRefreshFloor) }
	before := registry.listCalls
	_, resolution := lagging.resolveRouting(context.Background(), "audit")
	require.Equal(t, solutionRoutable, resolution)
	require.Equal(t, before+1, registry.listCalls)

	// A second miss inside the floor does not read the registry again.
	lagging.now = time.Now
	before = registry.listCalls
	_, resolution = lagging.resolveRouting(context.Background(), "ghost")
	require.Equal(t, solutionUnregistered, resolution)
	require.Equal(t, before, registry.listCalls)
}

// A replica that has never loaded a snapshot cannot tell an unregistered
// solution from a registered one, so it fails closed and says the registry is
// unavailable — a different answer from "not registered".
func TestGateway_Solution_RegistryNeverLoaded_FailsClosed(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	solutionRegistryFake(t, gw).listErr = grpcstatus.Error(codes.Unavailable, "registry down")

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "registry unavailable")
}

// A registry outage after a snapshot has loaded must not empty the routing
// table: the last known state keeps serving.
func TestGateway_Solution_RegistryOutageKeepsLastSnapshot(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	registry := solutionRegistryFake(t, gw)

	registry.listErr = grpcstatus.Error(codes.Unavailable, "registry down")
	require.Error(t, gw.solutions.refresh(context.Background()))

	_, resolution := gw.solutions.resolveRouting(context.Background(), "audit")
	require.Equal(t, solutionRoutable, resolution)
	require.NotNil(t, fake)
}

// The snapshot endpoint is the frontend's read path and an operator's
// diagnostic. It is a projection: it carries the states that tell those cases
// apart and deliberately not the upstream URL, which only this process routes
// to.
func TestGateway_Solution_RegistrySnapshot(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	declareSolution(t, gw, "reports", "http://example.svc")
	registry := solutionRegistryFake(t, gw)
	registry.records["reports"].Frontend = nil
	require.NoError(t, gw.solutions.refresh(context.Background()))

	noTok := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	noTokW := httptest.NewRecorder()
	gw.ServeHTTP(noTokW, noTok)
	require.Equal(t, http.StatusUnauthorized, noTokW.Code)

	req := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var snapshot solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.NotZero(t, snapshot.Revision)
	require.Len(t, snapshot.Solutions, 2)
	require.Equal(t, "audit", snapshot.Solutions[0].ID)
	require.Equal(t, "active", snapshot.Solutions[0].Status)
	require.Equal(t, "reports", snapshot.Solutions[1].ID)
	require.Equal(t, "active", snapshot.Solutions[1].Status)
	require.Nil(t, snapshot.Solutions[1].Manifest, "a presence without its frontend block has no page to render")
	require.NotContains(t, w.Body.String(), "upstream")
}

// The manifest the projection carries is built here, from the frontend block
// the applied presence declared plus the host's own constants — never
// reported by the running solution. Its shape is the one the frontend parses.
func TestGateway_Solution_RegistrySnapshotBuildsTheManifestFromThePresence(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	declareSolution(t, gw, "audit", "http://example.svc")
	registry := solutionRegistryFake(t, gw)
	registry.records["audit"].Frontend.Manifest = `{"title":"Audit","order":3,"exposed_module":"./Page","host_contract":1,` +
		`"dashboard":{"events":[]},"surfaces":[{"id":"footnote","client":"word","title":"Footnote","description":"Cite it","module":"/surfaces/footnote.js","contract":1,"applies":null,"events":["document.saved"]}]}`
	require.NoError(t, gw.solutions.refresh(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var snapshot solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.Len(t, snapshot.Solutions, 1)
	require.NotNil(t, snapshot.Solutions[0].Manifest)
	require.JSONEq(t, `{
		"id":"audit","schemaVersion":1,
		"nav":{"title":"Audit","path":"/s/audit","order":3},
		"frontend":{"type":"module-federation","manifestUrl":"/assets/mf-manifest.json","exposedModule":"./Page","hostContract":1},
		"backend":{"serviceAlias":"audit","capabilityPath":"/.well-known/capabilities"},
		"dashboard":{"events":[]},
		"surfaces":[{"id":"footnote","client":"word","title":"Footnote","description":"Cite it","module":"/surfaces/footnote.js","contract":1,"events":["document.saved"]}]
	}`, *snapshot.Solutions[0].Manifest)

	// A block this host cannot read renders nothing, rather than a manifest
	// the frontend would refuse for a reason it cannot name.
	registry.records["audit"].Frontend.Manifest = `{"title":`
	require.NoError(t, gw.solutions.refresh(context.Background()))
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	var unreadable solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &unreadable))
	require.Nil(t, unreadable.Solutions[0].Manifest)
}

// A module contributes a page through the same loader as a solution. Only its
// public assets use /solutions; its authenticated API remains on /v1.
func TestGateway_ModuleFrontendUsesCommonAssetsOnly(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	upstream := registerSolutionUpstream(t, gw, "shared-example")
	registry := solutionRegistryFake(t, gw)
	registry.records["shared-example"].Declared.Kind = accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE
	require.NoError(t, gw.solutions.refresh(context.Background()))
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/assets/mf-manifest.json", http.StatusOK},
		{http.MethodHead, "/assets/chunk.js", http.StatusOK},
		{http.MethodGet, "/.well-known/capabilities", http.StatusOK},
		{http.MethodGet, "/records", http.StatusForbidden},
		{http.MethodPost, "/assets/write", http.StatusForbidden},
		{http.MethodGet, "/assets/../records", http.StatusForbidden},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			upstream.lastHeaders = nil
			r := httptest.NewRequest(tc.method, "/solutions/shared-example"+tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code)
			if tc.status == http.StatusOK {
				require.Equal(t, tc.path, upstream.lastPath)
				require.Empty(t, upstream.lastHeaders.Get("Authorization"))
			} else {
				require.Nil(t, upstream.lastHeaders)
			}
		})
	}
	// The module stays routable as a backend when headless, but exposes no UI assets.
	registry.records["shared-example"].Frontend = nil
	require.NoError(t, gw.solutions.refresh(context.Background()))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/solutions/shared-example/assets/chunk.js", nil))
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestGateway_ModuleFrontendWithdrawalAndLease(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired lease", true: "withdrawn"}[withdrawn], func(t *testing.T) {
			gw, _, _, _ := newGatewayHarness(t)
			upstream := registerSolutionUpstream(t, gw, "shared-example")
			registry := solutionRegistryFake(t, gw)
			registry.records["shared-example"].Declared.Kind = accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE
			if withdrawn {
				registry.tombstone("shared-example")
			}
			require.NoError(t, gw.solutions.refresh(context.Background()))
			if !withdrawn {
				registry.listErr = grpcstatus.Error(codes.Unavailable, "offline")
				gw.solutions.now = func() time.Time { return time.Now().Add(solutionRegistryLease + time.Second) }
			}
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/solutions/shared-example/assets/chunk.js", nil))
			if withdrawn {
				require.Equal(t, http.StatusBadGateway, w.Code)
			} else {
				require.Equal(t, http.StatusServiceUnavailable, w.Code)
			}
			require.Nil(t, upstream.lastHeaders)
		})
	}
}

func TestGateway_ModuleFrontendSnapshot(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "shared-example")
	registry := solutionRegistryFake(t, gw)
	registry.records["shared-example"].Declared.Kind = accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE
	require.NoError(t, gw.solutions.refresh(context.Background()))
	r := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	r.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	var snapshot solutionRegistryProjection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.Len(t, snapshot.Solutions, 1)
	require.Equal(t, "module", snapshot.Solutions[0].Kind)
	require.Equal(t, fakeSolutionTarget("shared-example"), snapshot.Solutions[0].TargetID)
	require.NotNil(t, snapshot.Solutions[0].Manifest)
	require.Contains(t, *snapshot.Solutions[0].Manifest, "/assets/mf-manifest.json")
}

func TestGateway_FrontendAssetsCannotChangeGenerationUnderAStableURL(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	upstream := registerSolutionUpstream(t, gw, "example")
	registry := solutionRegistryFake(t, gw)
	record := registry.records["example"]
	original := frontendAssetVersion(record)
	request := func(version string) int {
		upstream.lastHeaders = nil
		r := httptest.NewRequest(http.MethodGet, "/solutions/example/assets/remoteEntry.js", nil)
		r.Header.Set(frontendVersionHeader, version)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			require.Nil(t, upstream.lastHeaders)
		}
		return w.Code
	}
	require.Equal(t, http.StatusGone, request(original), "an unsigned legacy frontend cannot serve a versioned immutable URL")
	record.Declared.Generation++
	record.Declared.Release = "acme/example@2.0.0"
	require.NoError(t, gw.solutions.refresh(context.Background()))
	upgraded := frontendAssetVersion(record)
	require.NotEqual(t, original, upgraded)
	require.Equal(t, http.StatusGone, request(original), "a stale frontend replica must not serve new bytes under the previous URL")
	require.Equal(t, http.StatusGone, request(upgraded))
	record.Declared.Generation++
	record.Declared.Release = "acme/example@1.0.0"
	require.NoError(t, gw.solutions.refresh(context.Background()))
	rolledBack := frontendAssetVersion(record)
	require.NotEqual(t, original, rolledBack, "rollback is a new applied generation")
	require.Equal(t, http.StatusGone, request(upgraded))
	require.Equal(t, http.StatusGone, request(rolledBack))
}
