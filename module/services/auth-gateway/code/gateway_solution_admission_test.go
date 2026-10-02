package main

import (
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
	gw.solutionEntitlements = &entitledTo{ids: []string{"reports"}}

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
	gw.solutionEntitlements = &entitledTo{ids: []string{"audit"}}

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
	authority := &entitledTo{ids: []string{"audit"}}
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
	authority := &entitledTo{ids: []string{"audit"}}
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
		Entitlements:  []*accountsv1.SolutionEntitlement{{SolutionIdentifier: "other"}},
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
			Entitlements:  []*accountsv1.SolutionEntitlement{{SolutionIdentifier: "reports"}},
			NextPageToken: "page-2",
		},
		{
			Entitlements: []*accountsv1.SolutionEntitlement{{SolutionIdentifier: "audit"}},
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
			{SolutionIdentifier: "audit", Healthy: false},
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"an unhealthy installation is still installed; the registry's resolve owns liveness")
}

// The public Module-Federation surface is NOT gated, and cannot be: the browser's
// module loader fetches it with no credential, so there is no viewer to ask
// about. It is named here as a test rather than left as a comment, because a
// later change that gated it would break same-origin remote loading and the
// reason would not be obvious.
func TestGatewaySolutionProxy_PublicAssetSurfaceIsNotAdmissionGated(t *testing.T) {
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

// A session that has selected no organization has no org-scoped admission to
// read. That is refused rather than routed: routing it would reach a solution on
// nobody's installation, and answering it as "not entitled" would send someone
// looking for a grant when what they need is to pick an organization.
func TestGatewaySolutionProxy_NoOrganizationInSessionIsRefused(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")
	authority := &entitledTo{ids: []string{"audit"}}
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
