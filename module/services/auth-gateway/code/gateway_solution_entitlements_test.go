package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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

// fakeSolutionEntitlements stands in for accounts. It records what it was asked,
// because the point of this endpoint is WHICH org and subject reach the authority:
// they must be the ones ext_authz verified, never values the caller supplied.
type fakeSolutionEntitlements struct {
	calls []*accountsv1.ListSolutionEntitlementsRequest
	// pages is served in order, so the paging loop can be exercised.
	pages []*accountsv1.ListSolutionEntitlementsResponse
	err   error
}

func (f *fakeSolutionEntitlements) List(
	_ context.Context, req *accountsv1.ListSolutionEntitlementsRequest,
) (*accountsv1.ListSolutionEntitlementsResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return &accountsv1.ListSolutionEntitlementsResponse{}, nil
	}
	page := f.pages[0]
	if len(f.pages) > 1 {
		f.pages = f.pages[1:]
	}
	return page, nil
}

func entitlementGateway(t *testing.T) (*Gateway, *fakeSolutionEntitlements, ed25519.PrivateKey) {
	t.Helper()
	gw, _, _, priv := newGatewayHarness(t)
	authority := &fakeSolutionEntitlements{}
	gw.solutionEntitlements = authority
	return gw, authority, priv
}

func entitlementRequest(internalToken, bearer string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/solutions/_entitlements", nil)
	if internalToken != "" {
		req.Header.Set("X-Codefly-Internal-Token", internalToken)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func decodeEntitlements(t *testing.T, body []byte) solutionEntitlementProjection {
	t.Helper()
	var out solutionEntitlementProjection
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// The whole reason this endpoint exists: a projection cannot derive a verified
// organization, so it asks the component that authenticated. The org and subject
// the authority is asked about must therefore be ext_authz's, and an
// unauthenticated caller must be refused rather than answered.
func TestGatewaySolutionEntitlements_AsksAboutTheVerifiedIdentity(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	token := signValidToken(t, priv)
	authority.pages = []*accountsv1.ListSolutionEntitlementsResponse{{
		Entitlements: []*accountsv1.SolutionEntitlement{
			{TargetId: fakeSolutionTarget("audit"), InstallationId: "install-1", RootScopeNodeId: "node-1", Healthy: true},
		},
	}}

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", token))

	require.Equal(t, http.StatusOK, w.Code)
	body := decodeEntitlements(t, w.Body.Bytes())
	require.Len(t, authority.calls, 1)
	// Identity comes from the check response, so the answer names the same pair.
	require.Equal(t, body.Org, authority.calls[0].GetOrgId())
	require.Equal(t, body.Viewer, authority.calls[0].GetSubjectId())
	require.NotEmpty(t, body.Org)
	require.Equal(t, []solutionEntitlementEntry{
		{TargetID: fakeSolutionTarget("audit"), Healthy: true, ScopeNodeID: "node-1", InstallationID: "install-1"},
	}, body.Usable)
}

// A forged identity header must never reach the authority. This is the attack the
// endpoint exists to close: the frontend's own routes are not proxied through the
// gateway, so anything they read off a request is caller-controlled.
func TestGatewaySolutionEntitlements_IgnoresCallerSuppliedIdentity(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	req := entitlementRequest("test-internal-token", signValidToken(t, priv))
	req.Header.Set("X-Org-Id", "11111111-1111-1111-1111-111111111111")
	req.Header.Set("X-User-Id", "22222222-2222-2222-2222-222222222222")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, authority.calls, 1)
	require.NotEqual(t, "11111111-1111-1111-1111-111111111111", authority.calls[0].GetOrgId(),
		"the forged organization must have been stripped before ext_authz stamped its own")
	require.NotEqual(t, "22222222-2222-2222-2222-222222222222", authority.calls[0].GetSubjectId())
}

func TestGatewaySolutionEntitlements_UnauthenticatedIsRefusedNotAnsweredEmpty(t *testing.T) {
	gw, authority, _ := entitlementGateway(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", ""))

	// 401, never 200 with an empty list: an empty projection is indistinguishable
	// from "your organization installed nothing".
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, authority.calls, "the authority must not be asked for an unauthenticated caller")
}

func TestGatewaySolutionEntitlements_RequiresTheInternalCredential(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("", signValidToken(t, priv)))

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, authority.calls)
}

func TestGatewaySolutionEntitlements_RejectsNonGET(t *testing.T) {
	gw, _, priv := entitlementGateway(t)
	req := httptest.NewRequest(http.MethodPost, "/solutions/_entitlements", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// An authority that cannot answer must produce 503, never an empty projection: a
// consumer told "[]" retracts every solution the viewer is currently using.
func TestGatewaySolutionEntitlements_AuthorityFailureIs503(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	authority.err = errors.New("accounts unreachable")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signValidToken(t, priv)))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// Same rule for an unwired client: fail closed and distinguishably.
func TestGatewaySolutionEntitlements_NoAuthorityClientIs503(t *testing.T) {
	gw, _, priv := entitlementGateway(t)
	gw.solutionEntitlements = nil

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signValidToken(t, priv)))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// The answer must be the viewer's WHOLE entitled set. A truncated one is
// indistinguishable to a consumer from a smaller set, and it would narrow a menu
// on it.
func TestGatewaySolutionEntitlements_FollowsEveryPage(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	authority.pages = []*accountsv1.ListSolutionEntitlementsResponse{
		{
			Entitlements: []*accountsv1.SolutionEntitlement{
				{TargetId: fakeSolutionTarget("audit"), RootScopeNodeId: "node-1", Healthy: true},
			},
			NextPageToken: "cursor-1",
		},
		{
			Entitlements: []*accountsv1.SolutionEntitlement{
				{TargetId: fakeSolutionTarget("ledger"), RootScopeNodeId: "node-2", Healthy: false},
			},
		},
	}

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signValidToken(t, priv)))

	require.Equal(t, http.StatusOK, w.Code)
	body := decodeEntitlements(t, w.Body.Bytes())
	require.Equal(t, []solutionEntitlementEntry{
		{TargetID: fakeSolutionTarget("audit"), Healthy: true, ScopeNodeID: "node-1"},
		// Carried, not dropped: an unhealthy installation stays visible and is
		// marked unavailable by the consumer.
		{TargetID: fakeSolutionTarget("ledger"), Healthy: false, ScopeNodeID: "node-2"},
	}, body.Usable)
	require.Len(t, authority.calls, 2)
	require.Equal(t, "cursor-1", authority.calls[1].GetPageToken())
}

// A cursor that never terminates must not become a silently short answer.
func TestGatewaySolutionEntitlements_NonTerminatingCursorIs503(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	authority.pages = []*accountsv1.ListSolutionEntitlementsResponse{{
		Entitlements: []*accountsv1.SolutionEntitlement{
			{TargetId: fakeSolutionTarget("audit"), RootScopeNodeId: "node-1", Healthy: true},
		},
		NextPageToken: "forever",
	}}

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signValidToken(t, priv)))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Len(t, authority.calls, solutionEntitlementPageLimit)
}

// An empty entitled set is a legitimate 200: the organization installed nothing,
// or nothing was granted to this viewer. It must be distinguishable from every
// failure above, which is why those are 401/403/503.
func TestGatewaySolutionEntitlements_EmptySetIsAnAnswer(t *testing.T) {
	gw, _, priv := entitlementGateway(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signValidToken(t, priv)))

	require.Equal(t, http.StatusOK, w.Code)
	body := decodeEntitlements(t, w.Body.Bytes())
	require.Empty(t, body.Usable)
	require.NotEmpty(t, body.Org)
}

// signImpersonationToken mints the token an impersonation session carries: `sub`
// stays the real actor and `acting` names the user being viewed.
func signImpersonationToken(t *testing.T, priv ed25519.PrivateKey, actor, acting, org string) string {
	t.Helper()
	return signAccessToken(t, priv, accessKeyID(priv.Public().(ed25519.PublicKey)), accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   actor,
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti-impersonation",
		},
		OrgID:          org,
		OrgRole:        "member",
		SessionID:      uuid.Must(uuid.NewV7()).String(),
		ActingAsUserID: acting,
	})
}

// During impersonation the menu is the IMPERSONATED user's. accounts authorizes
// against the effective subject, so asking about the real actor would show an
// administrator their own grants inside someone else's organization — never the
// menu the user they are debugging actually sees.
func TestGatewaySolutionEntitlements_ImpersonationAsksAboutTheImpersonatedUser(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	actor := uuid.Must(uuid.NewV7()).String()
	viewed := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signImpersonationToken(t, priv, actor, viewed, org)))

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, authority.calls, 1)
	require.Equal(t, viewed, authority.calls[0].GetSubjectId(), "the effective subject, not the real actor")
	require.Equal(t, org, authority.calls[0].GetOrgId())
	require.Equal(t, viewed, decodeEntitlements(t, w.Body.Bytes()).Viewer)
}

// A caller cannot put itself into someone else's menu by sending the
// impersonation header: it is stripped before ext_authz stamps its own.
func TestGatewaySolutionEntitlements_IgnoresCallerSuppliedActingHeader(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	req := entitlementRequest("test-internal-token", signValidToken(t, priv))
	req.Header.Set("X-Acting-As-User-Id", "33333333-3333-3333-3333-333333333333")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, authority.calls, 1)
	require.NotEqual(t, "33333333-3333-3333-3333-333333333333", authority.calls[0].GetSubjectId())
}

// Every read fans into the authority, so it spends the per-org budget like the
// equivalent catalog read. Unmetered, a signed-in browser could drive unbounded
// accounts load through the menu poll.
func TestGatewaySolutionEntitlements_SpendsThePerOrgBudget(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	// effective budget = limit(1) + burst(max(1/5,1)=1) = 2 requests / org / min.
	gw.rateLimiter = newInProcessRateLimiter(1)
	// One token, so every request keys on the same stamped X-Org-Id.
	token := signValidToken(t, priv)

	got429 := false
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, entitlementRequest("test-internal-token", token))
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.True(t, got429, "the entitlement read must draw on the per-org budget")
	require.Less(t, len(authority.calls), 5, "a throttled request must not reach the authority")
}

// The endpoint's OWN refusals are named, so a caller can tell a fault in its
// cluster credential from a verdict on the user's. Both are 401 on the wire.
func TestGatewaySolutionEntitlements_NamesItsOwnRefusals(t *testing.T) {
	gw, _, priv := entitlementGateway(t)

	internal := httptest.NewRecorder()
	gw.ServeHTTP(internal, entitlementRequest("wrong-internal-token", signValidToken(t, priv)))
	require.Equal(t, http.StatusUnauthorized, internal.Code)
	require.Equal(t, "internal-credential", internal.Header().Get("X-Codefly-Entitlement-Refusal"))

	user := httptest.NewRecorder()
	gw.ServeHTTP(user, entitlementRequest("test-internal-token", "not-a-token"))
	require.Equal(t, http.StatusUnauthorized, user.Code)
	require.Empty(t, user.Header().Get("X-Codefly-Entitlement-Refusal"),
		"a refused USER credential is ext_authz's verdict, not this endpoint's refusal")
}
