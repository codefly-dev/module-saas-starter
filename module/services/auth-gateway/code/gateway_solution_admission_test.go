package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The hole these tests close.
//
// A solution became reachable on this host because it was AVAILABLE — delivery
// declared it, or, until the cutover, its runtime registered it. Whether the
// caller's organization had installed it, and whether a grant reached the
// caller, decided only what the navigation menu and the surface listing showed.
// So a viewer in any organization could call the data endpoints of every
// solution the deployment ran, with a real bearer forwarded to them, by typing
// the path the menu declined to show.
//
// Three layers, none inferred from another: available on the deployment,
// installed by an organization, exposed to a team. The proxy is where the second
// and third are enforced for traffic, because it is the component that holds the
// verified identity and forwards the credential.
//
// Every test here answers 200 against the pre-change gateway.

func TestGatewaySolutionProxy_RefusesASolutionTheOrganizationDidNotInstall(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	registerSolutionUpstream(t, gw, "reports")
	// The organization installed `reports` and nothing else. `audit` is
	// available on the deployment and reachable by path.
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("reports")}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Nil(t, fake.lastHeaders,
		"an uninstalled solution must never receive the request, and never the caller's bearer")
	require.Equal(t, refusalNotEntitled, w.Header().Get(solutionEntitlementRefusalHeader),
		"the refusal must be named, so a client can tell 'nobody installed this' from an ext_authz denial")
}

func TestGatewaySolutionProxy_AllowsASolutionTheOrganizationInstalled(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("audit")}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, fake.lastHeaders, "an installed solution still routes")
}

// The authority is asked about the identity ext_authz VERIFIED, never one the
// caller supplied. A proxy that read the caller's own X-Org-Id would let anyone
// admit themselves to every solution by editing a header.
func TestGatewaySolutionProxy_AsksAboutTheVerifiedIdentityNotTheCallersHeaders(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	authority := &entitledTo{ids: []string{fakeSolutionTarget("audit")}}
	gw.solutionEntitlements = authority

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("X-Org-Id", "00000000-0000-0000-0000-00000000dead")
	req.Header.Set("X-User-Id", "00000000-0000-0000-0000-00000000beef")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, authority.calls, 1)
	require.NotEqual(t, "00000000-0000-0000-0000-00000000dead", authority.calls[0].GetOrgId(),
		"the caller's own org header must never reach the authority")
	require.NotEqual(t, "00000000-0000-0000-0000-00000000beef", authority.calls[0].GetSubjectId(),
		"the caller's own user header must never reach the authority")
}

// During an impersonation the admission is the IMPERSONATED viewer's — the
// subject accounts authorizes against — not the administrator's. Otherwise an
// administrator impersonating a user would reach solutions that user cannot,
// which is the one thing impersonation exists to check.
func TestGatewaySolutionProxy_ImpersonationIsDecidedOnTheImpersonatedViewer(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	authority := &entitledTo{ids: []string{fakeSolutionTarget("audit")}}
	gw.solutionEntitlements = authority

	actor := uuid.Must(uuid.NewV7()).String()
	impersonated := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()
	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signImpersonationToken(t, priv, actor, impersonated, org))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, authority.calls, 1)
	require.Equal(t, impersonated, authority.calls[0].GetSubjectId(),
		"admission must be decided on the impersonated viewer, not the acting administrator")
}

// "I cannot ask" is not "you may not". An accounts outage must not read as a
// missing grant, and must not route either.
func TestGatewaySolutionProxy_AuthorityFailureIs503AndForwardsNothing(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &fakeSolutionEntitlements{err: errors.New("accounts down")}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Nil(t, fake.lastHeaders, "nothing is forwarded while admission is unknown")
	require.NotEqual(t, refusalNotEntitled, w.Header().Get(solutionEntitlementRefusalHeader),
		"an outage must not be named as a verdict on the grant")
}

// A deployment that never wired the authority fails closed. The behaviour this
// check replaces was "serve every solution to every organization", so an
// unwired client must not fall back to it.
func TestGatewaySolutionProxy_NoAuthorityClientFailsClosed(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = nil

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Nil(t, fake.lastHeaders)
}

// A cursor that never terminates is undecidable, not "not entitled". A
// truncated set is indistinguishable from a smaller one, and here the
// difference is whether a viewer's traffic is refused.
func TestGatewaySolutionProxy_NonTerminatingCursorIs503(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &fakeSolutionEntitlements{pages: []*accountsv1.ListSolutionEntitlementsResponse{{
		Entitlements:  []*accountsv1.SolutionEntitlement{{TargetId: fakeSolutionTarget("other")}},
		NextPageToken: "forever",
	}}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// A solution past the first page is still installed. The walk stops at the first
// MATCH, never at the first page.
func TestGatewaySolutionProxy_FindsAnEntitlementOnALaterPage(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &fakeSolutionEntitlements{pages: []*accountsv1.ListSolutionEntitlementsResponse{
		{
			Entitlements:  []*accountsv1.SolutionEntitlement{{TargetId: fakeSolutionTarget("reports")}},
			NextPageToken: "page-2",
		},
		{
			Entitlements: []*accountsv1.SolutionEntitlement{{TargetId: fakeSolutionTarget("audit")}},
		},
	}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

// An installed solution that is not serving right now stays a 503 from the
// registry's own resolve, and admission is never consulted for it: health and
// admission are different conditions and must not collapse into one answer.
func TestGatewaySolutionProxy_UnhealthyEntitlementIsStillAdmitted(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &fakeSolutionEntitlements{pages: []*accountsv1.ListSolutionEntitlementsResponse{{
		Entitlements: []*accountsv1.SolutionEntitlement{
			{TargetId: fakeSolutionTarget("audit"), Healthy: false},
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"an unhealthy installation is still installed; the registry's resolve owns liveness")
}

// The public Module-Federation surface is not gated PER VIEWER, and cannot be:
// the browser's module loader fetches it with no credential, so there is no
// viewer to ask about. It is named here as a test rather than left as a comment,
// because a later change that gated it per viewer would break same-origin remote
// loading and the reason would not be obvious.
//
// This test's EXPECTATION CHANGED. It used to be named
// ...IsNotAdmissionGated and assert that this branch checked nothing at all,
// which pinned a hole: "no viewer to ask about" was read as "nothing is
// checkable", and a record nothing declared had its remote entry and chunks
// served same-origin and executed inside the host origin under its own
// `'self'` — while the authenticated surface beside it refused the very same
// record. The declaration half needs no viewer, so it is checked now (see the
// test below), and what remains pinned here is only the per-viewer half.
//
// What was deliberately NOT changed, stated so a reviewer can overrule it: a
// credential-less fetch is still served 200 with the authority not asked. There
// is no identity on the request to decide an organisation's admission with, and
// gating only the fetches that happen to carry one adds nothing — an attacker
// omits the credential — while 503-ing a solution's own asset loads during an
// accounts outage.
func TestGatewaySolutionProxy_PublicAssetSurfaceIsNotGatedPerViewer(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")
	authority := &entitledTo{ids: nil}
	gw.solutionEntitlements = authority

	req := httptest.NewRequest(http.MethodGet, "/solutions/example-go/assets/mf-manifest.json", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, fake.lastHeaders)
	require.Empty(t, authority.calls,
		"an unauthenticated asset fetch has no viewer, so the authority is not asked")
}

// The declaration half of the same branch, which needs no viewer.
//
// A registration no declaration opened a target for cannot have been installed
// by any organisation, so nobody consented to this host serving its bytes. The
// authenticated surface has refused such a record since the target join landed;
// the public surface served it, same-origin, as a script the host origin
// executes.
//
// Against the pre-change gateway this answers 200 and the upstream is reached.
func TestGatewaySolutionProxy_PublicAssetSurfaceRefusesPresenceNothingDeclared(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "example-go")
	solutionRegistryFake(t, gw).undeclare("example-go")
	require.NoError(t, gw.solutions.refresh(context.Background()))
	authority := &entitledTo{ids: nil}
	gw.solutionEntitlements = authority

	for _, path := range []string{
		"/assets/mf-manifest.json",
		"/assets/remoteEntry.js",
		"/.well-known/capabilities",
	} {
		req := httptest.NewRequest(http.MethodGet, "/solutions/example-go"+path, nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code,
			"%q: presence nothing declared must serve no bytes through this host", path)
	}
	require.Nil(t, fake.lastHeaders,
		"an undeclared solution must never be reached, not even for its static bytes")
	require.Empty(t, authority.calls,
		"and the refusal is final without asking the authority: no viewer, and no target to ask about")
}

// A session that has selected no organization has no org-scoped admission to
// read. That is refused rather than routed: routing it would reach a solution on
// nobody's installation, and answering it as "not entitled" would send someone
// looking for a grant when what they need is to pick an organization.
func TestGatewaySolutionProxy_NoOrganizationInSessionIsRefused(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	authority := &entitledTo{ids: []string{fakeSolutionTarget("audit")}}
	gw.solutionEntitlements = authority

	orgless := signAccessToken(t, priv, accessKeyID(priv.Public().(ed25519.PublicKey)), accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   uuid.Must(uuid.NewV7()).String(),
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti-orgless",
		},
		SessionID: uuid.Must(uuid.NewV7()).String(),
	})

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+orgless)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, refusalNoOrganization, w.Header().Get(solutionEntitlementRefusalHeader))
	require.Nil(t, fake.lastHeaders)
	require.Empty(t, authority.calls, "there is no org-scoped set to ask about")
}

// The §9 attack, executed rather than described: a REPLACEMENT binding that
// claims a withdrawn route alias must inherit nothing from the installation that
// named it.
//
// The previous shape of this check compared the alias an installation named to
// the alias being requested. Both are the string "reports" here, so it admitted
// org A's viewers to binding Y and forwarded their bearers to it — with no
// administrator having acted, and with A's team grant now exposing Y.
//
// Against the pre-change gateway this test answers 200. It fails for the reason
// it exists rather than because the entitlement shape changed: the authority
// still answers an entitlement for the predecessor, the alias still resolves,
// the upstream is still live, and the only thing that differs is the identity
// behind the alias.
func TestGatewaySolutionProxy_AReplacementBindingDoesNotInheritTheWithdrawnInstallation(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "reports")

	// Org A installed the binding that held `reports`, and still holds that
	// entitlement: an installation is not withdrawn by delivery, it is revoked —
	// and this test is about the instant before any revocation has been seen.
	withdrawn := fakeSolutionTarget("reports")
	gw.solutionEntitlements = &entitledTo{ids: []string{withdrawn}}

	// A different binding now serves the same alias. Its target is its own.
	replacement := "target-replacement-binding"
	require.NotEqual(t, withdrawn, replacement)
	solutionRegistryFake(t, gw).declareTarget("reports", "acme.test.replacement", replacement)
	require.NoError(t, gw.solutions.refresh(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/solutions/reports/v1/reports/data", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code,
		"a replacement binding must not be reachable through the predecessor's installation")
	require.Nil(t, fake.lastHeaders,
		"the replacement must never receive the request, and never the caller's bearer")
	require.Equal(t, refusalNotEntitled, w.Header().Get(solutionEntitlementRefusalHeader))
}

// The other direction of the same rule, so the test above cannot pass by
// refusing everything: the organisation that installed the REPLACEMENT reaches
// it, on the same alias, in the same registry state.
func TestGatewaySolutionProxy_TheReplacementsOwnInstallationIsAdmitted(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "reports")

	replacement := "target-replacement-binding"
	solutionRegistryFake(t, gw).declareTarget("reports", "acme.test.replacement", replacement)
	require.NoError(t, gw.solutions.refresh(context.Background()))
	gw.solutionEntitlements = &entitledTo{ids: []string{replacement}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/reports/v1/reports/data", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, fake.lastHeaders)
}

// Presence that nothing declared is admissible to NOBODY. There is no target for
// an installation to have named, so there is no organisation whose consent could
// admit it — and that is a verdict (403), not an outage (503): the authority was
// reachable and the answer is final.
//
// Against the pre-change gateway this answers 200, because a self-registered
// record was routable and its alias was the join key.
func TestGatewaySolutionProxy_RefusesPresenceNothingDeclared(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	solutionRegistryFake(t, gw).undeclare("audit")
	require.NoError(t, gw.solutions.refresh(context.Background()))
	// Entitled to the alias's derived target, which is what an installation made
	// before the declaration was stripped would have named.
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("audit")}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Nil(t, fake.lastHeaders)
	require.Equal(t, refusalNotEntitled, w.Header().Get(solutionEntitlementRefusalHeader))
}

// A REFUSED request must cost the org's budget, because asking is what costs.
//
// Admission is an authority call to accounts — up to one scope-tree read per
// entitlement page — and that cost is identical whether the answer forwards the
// request or refuses it. With the limiter only in front of the proxy, a caller
// could ask about a solution it was not entitled to as fast as it liked: every
// refusal was free, so there was nothing to exhaust and the authority absorbed
// the whole load.
//
// The assertion is on the AUTHORITY's call count, not on the response code: a
// 429 proves the limiter ran, but only a bounded number of authority calls
// proves the limiter ran BEFORE it.
func TestGatewaySolutionProxy_RefusedRequestsAreMeteredBeforeTheAuthorityIsAsked(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	// Entitled to nothing, so every request is refused — the free path.
	authority := &entitledTo{ids: nil}
	gw.solutionEntitlements = authority
	// effective budget = limit(1) + burst(max(1/5,1)=1) = 2 requests / org / min.
	gw.rateLimiter = NewRateLimiter(1)

	token := signValidToken(t, priv)
	throttled := false
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			throttled = true
			continue
		}
		require.Equal(t, http.StatusForbidden, w.Code)
	}
	require.True(t, throttled, "refused solution requests must consume the budget")
	require.LessOrEqual(t, len(authority.calls), 2,
		"the authority must be asked only for requests the budget admitted: got %d calls for 8 requests",
		len(authority.calls))
}

// An outage is not a verdict about a solution, and the registry read is part of
// the admission decision.
//
// The on-demand registry read was issued and its error LOGGED AND DROPPED, after
// which the empty lookup that followed it was reported as a fact: "no such
// solution" (502 to the proxy) and, through the admission entry point, "your
// organization did not install this" (403, named `not-entitled`). Both are
// absence of evidence reported as evidence of absence, and the 403 is the worse
// of the two — it sends an operator to the installation and the grants over an
// accounts outage.
func TestGatewaySolutionProxy_RegistryOutageOnAnUnknownAliasIsNotAVerdict(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	// A snapshot HAS loaded — this is not a cold replica — and it does not hold
	// `reports`.
	registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("audit")}}
	// The read that would settle whether `reports` exists now fails.
	solutionRegistryFake(t, gw).listErr = errors.New("accounts down")
	// Past the on-demand refresh floor, so the miss really does issue a read.
	gw.solutions.now = func() time.Time { return time.Now().Add(time.Second) }

	req := httptest.NewRequest(http.MethodGet, "/solutions/reports/v1/reports/data", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code,
		"a registry read that failed must answer 'cannot tell', never 'no such solution'")
	require.NotEqual(t, refusalNotEntitled, w.Header().Get(solutionEntitlementRefusalHeader))
}

// The SECOND request of a burst must answer the same thing as the first.
//
// A miss issues at most one registry read per refresh floor, so the requests
// that arrive inside that window decide on a read they did not issue. Reporting
// "nothing to do" for a suppressed read drew a clean bill of health from the
// failed one before it: the first request in a burst answered 503 and every
// other one answered 502, for the same alias, in the same outage.
func TestGatewaySolutionProxy_RegistryOutageStaysUndecidableInsideTheRefreshFloor(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "audit")
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("audit")}}
	registry := solutionRegistryFake(t, gw)
	registry.listErr = errors.New("accounts down")
	// A frozen clock: the first miss is past the floor, and every request after
	// it is inside it — the burst, with no sleeping.
	frozen := time.Now().Add(time.Second)
	gw.solutions.now = func() time.Time { return frozen }

	token := signValidToken(t, priv)
	for attempt := 0; attempt < 3; attempt++ {
		req := httptest.NewRequest(http.MethodGet, "/solutions/reports/v1/reports/data", nil)
		req.Header.Set("authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusServiceUnavailable, w.Code,
			"attempt %d: a suppressed read must report the outage it suppressed", attempt)
	}
	require.Equal(t, 1, registry.listCalls-1,
		"the floor must still hold the reads down to one: this is about the ANSWER, not the rate")
}

// replacementArrivesMidRequest wires a solution whose alias is taken over by a
// REPLACEMENT binding — new target, new address — part-way through one request.
//
// The takeover runs inside ext_authz's revocation lookup, which is a real step
// on the production request path between the proxy's resolution of the upstream
// and its admission decision. That is the window the two reads straddled: a
// reconcile tick, a lease lapse or a declared record's heartbeat lands there in
// production, and nothing about the request is faked to put it there.
//
// It returns the two upstreams: the one the record named when the request
// arrived, and the one it names by the time admission runs.
func replacementArrivesMidRequest(
	t *testing.T, gw *Gateway, alias, replacementTarget string,
) (predecessor, replacement *fakeUpstream) {
	t.Helper()
	predecessor = registerSolutionUpstream(t, gw, alias)
	replacement = &fakeUpstream{body: "replacement-response"}
	replacementSrv := httptest.NewServer(replacement)
	t.Cleanup(replacementSrv.Close)

	registry := solutionRegistryFake(t, gw)
	gw.authz.revoker = revokerDuring(func() {
		registry.repointUpstream(alias, replacementSrv.URL)
		registry.declareTarget(alias, "acme.test.replacement", replacementTarget)
		require.NoError(t, gw.solutions.refresh(context.Background()))
	})
	return predecessor, replacement
}

// The identity admitted and the address reached must come from ONE resolution.
//
// The proxy took the upstream from one read of the registry snapshot and the
// target from another, further down the same request. A replacement binding
// claiming the alias in between was then admitted on ITS installation — the
// second read saw its target, and the requesting organisation holds it — while
// the bearer was forwarded to the address the FIRST read returned, which is the
// predecessor's. An organisation reached a binding it had not installed, and the
// predecessor received a real bearer for a viewer nobody had granted it to.
//
// Against the two-read gateway this answers 200 and the predecessor's capture is
// populated.
func TestGatewaySolutionProxy_OneResolutionDecidesAdmissionAndDestination(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	const replacementTarget = "target-replacement-binding"
	predecessor, replacement := replacementArrivesMidRequest(t, gw, "reports", replacementTarget)
	// The caller's organisation installed the REPLACEMENT and nothing else.
	gw.solutionEntitlements = &entitledTo{ids: []string{replacementTarget}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/reports/v1/reports/data", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code,
		"the resolution this request was admitted against is the predecessor's, which this organisation did not install")
	require.Nil(t, predecessor.lastHeaders,
		"the predecessor must never receive a request admitted on the replacement's installation, and never the bearer")
	require.Nil(t, replacement.lastHeaders,
		"nor the replacement, which this request never resolved")
}

// The other direction of the same rule, so the test above cannot pass by
// refusing whatever changes under it: with the SAME takeover landing in the same
// window, the organisation that installed the solution this request RESOLVED is
// admitted, and the request reaches that one's address.
//
// Against the two-read gateway this answers 403: the second read had already
// moved on to the replacement's target, which this organisation does not hold.
func TestGatewaySolutionProxy_TheResolvedBindingIsTheOneReachedWhenTheAliasMovesMidRequest(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	predecessor, replacement := replacementArrivesMidRequest(t, gw, "reports", "target-replacement-binding")
	// The caller's organisation installed the binding this request resolved.
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("reports")}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/reports/v1/reports/data", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, predecessor.lastHeaders,
		"the address reached must be the one whose identity was admitted")
	require.Nil(t, replacement.lastHeaders,
		"and never the binding that took the alias after this request had resolved it")
}
