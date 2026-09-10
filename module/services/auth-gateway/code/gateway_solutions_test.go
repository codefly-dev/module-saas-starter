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

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// registerSolutionUpstream wires a fully registered solution — both halves,
// live leases — into the registry behind gw and returns the upstream's capture
// struct. Both halves matter: a solution with only one is deliberately not
// routable (see TestGateway_Solution_BackendHalfAlone_NotRoutable).
func registerSolutionUpstream(t *testing.T, gw *Gateway, id string) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{body: "solution-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	registerSolutionHalves(t, gw, id, srv.URL)
	return fake
}

// solutionRegistryFake reaches the registry standing in for accounts behind gw.
func solutionRegistryFake(t *testing.T, gw *Gateway) *fakeSolutionRegistry {
	t.Helper()
	registry, ok := gw.solutions.client.(*fakeSolutionRegistry)
	require.True(t, ok, "harness gateway must be wired to the fake registry")
	return registry
}

// registerSolutionHalves writes both halves straight into the registry and
// refreshes the gateway's cache, standing in for the two self-registration
// calls a running solution makes.
func registerSolutionHalves(t *testing.T, gw *Gateway, id, upstream string) {
	t.Helper()
	registry := solutionRegistryFake(t, gw)
	ctx := context.Background()
	_, err := registry.Put(ctx, &accountsv1.PutSolutionRegistrationRequest{
		SolutionId:   id,
		Publisher:    id,
		LeaseSeconds: 120,
		Half: &accountsv1.PutSolutionRegistrationRequest_Frontend{
			Frontend: &accountsv1.SolutionFrontendRegistration{Manifest: `{"id":"` + id + `"}`},
		},
	})
	require.NoError(t, err)
	_, err = registry.Put(ctx, &accountsv1.PutSolutionRegistrationRequest{
		SolutionId:   id,
		Publisher:    id,
		LeaseSeconds: 120,
		Half: &accountsv1.PutSolutionRegistrationRequest_Backend{
			Backend: &accountsv1.SolutionBackendRegistration{Upstream: upstream, ServiceAlias: id},
		},
	})
	require.NoError(t, err)
	require.NoError(t, gw.solutions.refresh(ctx))
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
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	for _, path := range []string{
		"/assets/mf-manifest.json",
		"/assets/remoteEntry.js",
		"/assets/chunks/app.1a2b3c.js",
		"/assets",
		"/.well-known/capabilities",
	} {
		req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go"+path, nil)
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
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go/assets/./chunks/../app.1a2b3c.js", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "/assets/app.1a2b3c.js", fake.lastPath)
}

// The public read exemption never forwards caller-supplied identity: a request
// spoofing identity headers reaches the upstream with them stripped.
func TestGateway_Solution_PublicSurface_StripsSpoofedIdentity(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go/assets/mf-manifest.json", nil)
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
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodPost, "/solutions/lastlogin-go/assets/mf-manifest.json", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")
}

// A traversal suffix cannot borrow the /assets exemption to reach an
// authenticated endpoint: the decision is made on the cleaned path.
func TestGateway_Solution_PublicSurface_TraversalStillAuthRequired(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	req := httptest.NewRequest(http.MethodGet, "/solutions/lastlogin-go/assets/../lastlogin", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")
}

// A solution's data endpoints stay auth-required: /lastlogin needs a valid
// bearer for both GET and POST, and reaches the upstream once presented.
func TestGateway_Solution_DataEndpoint_RequiresBearer(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "lastlogin-go")

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		fake.lastHeaders = nil
		noTok := httptest.NewRequest(method, "/solutions/lastlogin-go/lastlogin", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, noTok)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s /lastlogin without a bearer must be denied", method)
		require.Nil(t, fake.lastHeaders, "upstream must not be reached without auth")

		withTok := httptest.NewRequest(method, "/solutions/lastlogin-go/lastlogin", nil)
		withTok.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
		w = httptest.NewRecorder()
		gw.ServeHTTP(w, withTok)
		require.Equal(t, http.StatusOK, w.Code, "%s /lastlogin with a valid bearer is forwarded", method)
		require.Equal(t, "/lastlogin", fake.lastPath)
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
	// The caller's bearer is preserved for the solution's own downstream calls.
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

// A request with no solution id after the prefix is a 404, not a proxy attempt.
func TestGateway_Solution_MissingID_NotFound(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/solutions/", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// Both self-registration endpoints write to the one durable record, and the
// solution becomes routable on this replica as soon as the second half lands —
// no waiting for the reconcile tick, because a local write refreshes the cache.
func TestGateway_Solution_Register(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	fake := &fakeUpstream{body: "solution-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	postSolutionRegistration(t, gw, "/solutions/_frontend",
		`{"id":"audit","manifest":"{\"id\":\"audit\"}"}`, http.StatusOK)
	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"`+srv.URL+`"}`, http.StatusOK)

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "/v1/ping", fake.lastPath)
}

// postSolutionRegistration drives one registration endpoint with the internal
// token and asserts the status, returning the decoded body.
func postSolutionRegistration(t *testing.T, gw *Gateway, path, body string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, want, w.Code, "%s -> %s", path, w.Body.String())
	decoded := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &decoded)
	}
	return decoded
}

func TestGateway_Solution_Register_Rejects(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	// Non-POST is rejected before any auth work.
	getReq := httptest.NewRequest(http.MethodGet, "/solutions/_register", nil)
	getW := httptest.NewRecorder()
	gw.ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusMethodNotAllowed, getW.Code)

	// Missing id (with a valid internal token, to isolate the id check).
	noIDReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(`{"upstream":"http://x:80"}`))
	noIDReq.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	noIDW := httptest.NewRecorder()
	gw.ServeHTTP(noIDW, noIDReq)
	require.Equal(t, http.StatusBadRequest, noIDW.Code)

	// Non-http scheme is rejected (no file://, no scheme-less host).
	badReq := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(`{"id":"x","upstream":"file:///etc/passwd"}`))
	badReq.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	badW := httptest.NewRecorder()
	gw.ServeHTTP(badW, badReq)
	require.Equal(t, http.StatusBadRequest, badW.Code)
}

// Registration is privileged: without the cluster-internal token it is
// rejected before the upstream is ever stored, so an edge caller cannot point
// authenticated traffic at an attacker-controlled host.
func TestGateway_Solution_Register_RequiresInternalToken(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	body := `{"id":"evil","upstream":"http://attacker.example"}`

	// No token.
	noTok := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(body))
	noTokW := httptest.NewRecorder()
	gw.ServeHTTP(noTokW, noTok)
	require.Equal(t, http.StatusUnauthorized, noTokW.Code)

	// Wrong token.
	badTok := httptest.NewRequest(http.MethodPost, "/solutions/_register", strings.NewReader(body))
	badTok.Header.Set("X-Codefly-Internal-Token", "not-the-token")
	badTokW := httptest.NewRecorder()
	gw.ServeHTTP(badTokW, badTok)
	require.Equal(t, http.StatusUnauthorized, badTokW.Code)

	// The upstream was never registered, so even a fully authenticated user
	// gets 502, not a proxy to attacker.example.
	req := httptest.NewRequest(http.MethodGet, "/solutions/evil/x", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
}

// A confused/compromised internal caller still cannot register a
// credential-theft SSRF sink (cloud metadata / link-local / unspecified).
// Loopback is intentionally allowed, so it is not asserted here.
func TestGateway_Solution_Register_RejectsSSRFHosts(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	for _, upstream := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/",
		"http://[fe80::1]:9000",
		"http://0.0.0.0:9000",
	} {
		req := httptest.NewRequest(http.MethodPost, "/solutions/_register",
			strings.NewReader(`{"id":"x","upstream":"`+upstream+`"}`))
		req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "upstream %q must be rejected", upstream)
	}
}

// The authenticated solution data path consumes the same per-org/per-IP budget
// as an equivalent catalog route: it must route through rateLimitThenProxy, not
// proxy directly. Without that, /solutions/<id>/* would be an unmetered proxy an
// authenticated caller could flood past the org budget. Regression for #513,
// mirroring TestGateway_Module_Federated_RateLimited (#512).
func TestGateway_Solution_RateLimited(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	// effective budget = limit(1) + burst(max(1/5,1)=1) = 2 requests / org / min.
	gw.rateLimiter = NewRateLimiter(1)
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
	gw.rateLimiter = NewRateLimiter(1)
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

// A solution that registered only its backend half is NOT routable. Serving it
// would advertise a page whose required half never arrived; the audit finding
// this fixes is exactly that the two halves could disagree.
func TestGateway_Solution_BackendHalfAlone_NotRoutable(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://127.0.0.1:9"}`, http.StatusOK)

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "not active")
}

// The two halves must agree on a contract version. A page built against one
// backend contract must not be served against another.
func TestGateway_Solution_MismatchedContractVersions_NotRoutable(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)

	postSolutionRegistration(t, gw, "/solutions/_frontend",
		`{"id":"audit","manifest":"{}","contractVersion":"v2"}`, http.StatusOK)
	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://127.0.0.1:9","contractVersion":"v1"}`, http.StatusOK)

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// A registration survives the process that received it. A fresh cache — what a
// restarted replica starts with — rebuilds from the registry and routes with
// nobody re-registering.
func TestGateway_Solution_RestartRebuildsFromRegistry(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionHalves(t, gw, "audit", "http://10.0.0.7:8080")
	registry := solutionRegistryFake(t, gw)

	restarted := newSolutionRegistryCache(registry)
	upstream, resolution := restarted.resolve(context.Background(), "audit")

	require.Equal(t, solutionRoutable, resolution)
	require.Equal(t, "10.0.0.7:8080", upstream.Host)
}

// A registration made through one replica reaches the others: the second
// replica's reconcile converges it onto the same registry revision.
func TestGateway_Solution_ReplicasConvergeOnSameRevision(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registry := solutionRegistryFake(t, gw)
	other := newSolutionRegistryCache(registry)
	require.NoError(t, other.refresh(context.Background()))

	registerSolutionHalves(t, gw, "audit", "http://10.0.0.7:8080")

	_, otherRevision, _ := other.snapshot()
	_, writerRevision, _ := gw.solutions.snapshot()
	require.NotEqual(t, writerRevision, otherRevision, "the second replica has not seen the write yet")

	require.NoError(t, other.refresh(context.Background()))
	_, otherRevision, loaded := other.snapshot()
	require.True(t, loaded)
	require.Equal(t, writerRevision, otherRevision, "both replicas converge on one registry revision")

	_, resolution := other.resolve(context.Background(), "audit")
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
	registerSolutionHalves(t, gw, "audit", "http://10.0.0.7:8080")

	// Past the refresh floor, so the miss is allowed to consult the registry.
	lagging.now = func() time.Time { return time.Now().Add(solutionRefreshFloor) }
	before := registry.listCalls
	_, resolution := lagging.resolve(context.Background(), "audit")
	require.Equal(t, solutionRoutable, resolution)
	require.Equal(t, before+1, registry.listCalls)

	// A second miss inside the floor does not read the registry again.
	lagging.now = time.Now
	before = registry.listCalls
	_, resolution = lagging.resolve(context.Background(), "ghost")
	require.Equal(t, solutionUnregistered, resolution)
	require.Equal(t, before, registry.listCalls)
}

// A lease that lapsed suspends routing without discarding the registration:
// the record is still there, and the answer says "not active" rather than "not
// registered".
func TestGateway_Solution_ExpiredLease_NotRoutable(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionHalves(t, gw, "audit", "http://10.0.0.7:8080")

	registry := solutionRegistryFake(t, gw)
	registry.records["audit"].Backend.LeaseExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
	require.NoError(t, gw.solutions.refresh(context.Background()))

	_, resolution := gw.solutions.resolve(context.Background(), "audit")
	require.Equal(t, solutionNotActive, resolution)
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

	_, resolution := gw.solutions.resolve(context.Background(), "audit")
	require.Equal(t, solutionRoutable, resolution)
	require.NotNil(t, fake)
}

// Deregistration removes routing on this replica in the same request, and a
// plain re-registration afterwards carries no compare-and-swap token — which
// is what the registry refuses, so a retiring deployment's retry cannot
// resurrect the removed solution. Naming `reactivate` is the authorized way
// back.
func TestGateway_Solution_Deregister_BlocksResurrection(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	registry := solutionRegistryFake(t, gw)

	del := httptest.NewRequest(http.MethodDelete, "/solutions/_register?id=audit", nil)
	del.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	delW := httptest.NewRecorder()
	gw.ServeHTTP(delW, del)
	require.Equal(t, http.StatusOK, delW.Code)
	require.Equal(t, []string{"audit"}, registry.deletes)
	tombstoneRevision := registry.records["audit"].GetRevision()

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code, "a tombstoned solution is gone from the live snapshot")

	// A retiring deployment's heartbeat: no revision named, so the tombstone
	// refuses it outright rather than recreating the registration.
	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://10.0.0.7:8080"}`, http.StatusConflict)
	require.Nil(t, registry.puts[len(registry.puts)-1].ExpectedRevision,
		"a plain retry must name no revision")

	req = httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/ping", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code, "the refused retry did not resurrect the route")

	// An explicit re-registration names the tombstone's own revision and is
	// admitted, so a removed solution can be deliberately brought back.
	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://10.0.0.7:8080","reactivate":true}`, http.StatusOK)
	require.NotNil(t, registry.puts[len(registry.puts)-1].ExpectedRevision,
		"an authorized re-registration names the tombstone's revision")
	require.Equal(t, tombstoneRevision, registry.puts[len(registry.puts)-1].GetExpectedRevision(),
		"and it must be the tombstone's revision, not a live record's")
}

// Deregistration is privileged, exactly like registration.
func TestGateway_Solution_Deregister_RequiresInternalToken(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")

	del := httptest.NewRequest(http.MethodDelete, "/solutions/_register?id=audit", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, del)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, solutionRegistryFake(t, gw).deletes)
}

// Changing an existing half rides on the revision this replica last saw, so
// two concurrent updates serialize on the registry rather than racing.
func TestGateway_Solution_Register_CarriesCachedRevision(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	registry := solutionRegistryFake(t, gw)
	before := registry.records["audit"].GetRevision()

	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://10.0.0.9:8080"}`, http.StatusOK)

	last := registry.puts[len(registry.puts)-1]
	require.NotNil(t, last.ExpectedRevision)
	require.Equal(t, before, *last.ExpectedRevision)
}

// A registry refusal is relayed as a conflict, not as a success or a generic
// 502: the registrant has to re-read and retry, and must be told so.
// A replica that has not seen a registration cannot name its revision, so the
// registry refuses the write — recoverably. While that refusal shared a code
// with the tombstone refusal, which must never be retried, the gateway retried
// neither, and a legitimate upstream change 409'd until the next reconcile tick.
func TestGateway_Solution_ColdCacheRecoversRegistrationChange(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	registry := solutionRegistryFake(t, gw)
	before := len(registry.puts)

	// The state right after a restart, or a registration that landed on another
	// replica moments ago.
	gw.solutions.mu.Lock()
	gw.solutions.records = map[string]*accountsv1.SolutionRegistration{}
	gw.solutions.mu.Unlock()

	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://10.0.0.9:8080"}`, http.StatusOK)

	require.Equal(t, before+2, len(registry.puts),
		"the refused write is retried once, against a freshly read snapshot")
	require.Nil(t, registry.puts[before].ExpectedRevision,
		"the cold attempt can name no revision")
	require.NotNil(t, registry.puts[before+1].ExpectedRevision,
		"the retry names the revision it just read")
}

func TestGateway_Solution_Register_RelaysConflict(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	solutionRegistryFake(t, gw).putErr = grpcstatus.Error(codes.Aborted, "stale")

	postSolutionRegistration(t, gw, "/solutions/_register",
		`{"id":"audit","upstream":"http://10.0.0.9:8080"}`, http.StatusConflict)
}

// The frontend half is registered through the gateway because the frontend has
// no route to the accounts internal listener. It is the same privileged
// endpoint, and it refuses a body that is not a JSON document.
func TestGateway_Solution_FrontendRegister(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	noTok := httptest.NewRequest(http.MethodPost, "/solutions/_frontend",
		strings.NewReader(`{"id":"audit","manifest":"{}"}`))
	noTokW := httptest.NewRecorder()
	gw.ServeHTTP(noTokW, noTok)
	require.Equal(t, http.StatusUnauthorized, noTokW.Code)

	postSolutionRegistration(t, gw, "/solutions/_frontend",
		`{"id":"audit","manifest":"not json"}`, http.StatusBadRequest)

	body := postSolutionRegistration(t, gw, "/solutions/_frontend",
		`{"id":"audit","manifest":"{\"id\":\"audit\"}"}`, http.StatusOK)
	require.Equal(t, "pending", body["status"], "one half alone is a durable but pending registration")
	require.NotZero(t, body["revision"])
}

// The snapshot endpoint is the frontend's read path and an operator's
// diagnostic. It is a projection: it carries the states that tell those cases
// apart and deliberately not the upstream URL, which only this process routes
// to.
func TestGateway_Solution_RegistrySnapshot(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	postSolutionRegistration(t, gw, "/solutions/_frontend",
		`{"id":"reports","manifest":"{\"id\":\"reports\"}"}`, http.StatusOK)

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
	require.Equal(t, "pending", snapshot.Solutions[1].Status)
	// A consumer bounds its own cache staleness with this, so it has to travel
	// with the snapshot rather than being mirrored in the consumer.
	require.Equal(t, uint32(solutionLease.Seconds()), snapshot.LeaseSeconds,
		"the snapshot carries the lease window this gateway grants")
	require.NotContains(t, w.Body.String(), "upstream")
}
