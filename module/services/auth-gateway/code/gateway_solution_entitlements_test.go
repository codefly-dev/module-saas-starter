package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

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
			{SolutionIdentifier: "audit", InstallationId: "install-1", RootScopeNodeId: "node-1", Healthy: true},
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
		{ID: "audit", Healthy: true, ScopeNodeID: "node-1"},
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
				{SolutionIdentifier: "audit", RootScopeNodeId: "node-1", Healthy: true},
			},
			NextPageToken: "cursor-1",
		},
		{
			Entitlements: []*accountsv1.SolutionEntitlement{
				{SolutionIdentifier: "ledger", RootScopeNodeId: "node-2", Healthy: false},
			},
		},
	}

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, entitlementRequest("test-internal-token", signValidToken(t, priv)))

	require.Equal(t, http.StatusOK, w.Code)
	body := decodeEntitlements(t, w.Body.Bytes())
	require.Equal(t, []solutionEntitlementEntry{
		{ID: "audit", Healthy: true, ScopeNodeID: "node-1"},
		// Carried, not dropped: an unhealthy installation stays visible and is
		// marked unavailable by the consumer.
		{ID: "ledger", Healthy: false, ScopeNodeID: "node-2"},
	}, body.Usable)
	require.Len(t, authority.calls, 2)
	require.Equal(t, "cursor-1", authority.calls[1].GetPageToken())
}

// A cursor that never terminates must not become a silently short answer.
func TestGatewaySolutionEntitlements_NonTerminatingCursorIs503(t *testing.T) {
	gw, authority, priv := entitlementGateway(t)
	authority.pages = []*accountsv1.ListSolutionEntitlementsResponse{{
		Entitlements: []*accountsv1.SolutionEntitlement{
			{SolutionIdentifier: "audit", RootScopeNodeId: "node-1", Healthy: true},
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
