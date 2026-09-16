//go:build !pure

package infra_test

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDelegatedRecordAccessUsesRealPlacementAndAllSubjects(t *testing.T) {
	org, owner, role := layeredFixture(t, "example.record", "read")
	actor := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, actor, "member"))
	registerNode(t, org, "root", "root", "", "")
	registerNode(t, org, "root.allowed", "collection", "", "")
	registerNode(t, org, "root.private", "collection", "", "")
	registerNode(t, org, "root.allowed.record", "record", "example.record", "record-a")
	registerNode(t, org, "root.private.record", "record", "example.record", "record-b")
	for _, subject := range []string{owner, actor} {
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: subject, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root.allowed", RoleId: role})
		}))
	}
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, owner, org)
	current := func(context.Context) error { return nil }
	allowed, err := svc.CheckDelegatedRecordAccess(ctx, org, []string{owner, actor}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, allowed.GetAllowed())
	for _, id := range []string{"record-b", "unplaced"} {
		allowed, err = svc.CheckDelegatedRecordAccess(ctx, org, []string{owner, actor}, "example.record", id, "read", current)
		require.NoError(t, err)
		require.False(t, allowed.GetAllowed())
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, org, actor, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "root.allowed", role)
	}))
	allowed, err = svc.CheckDelegatedRecordAccess(ctx, org, []string{owner, actor}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.False(t, allowed.GetAllowed())
	allowed, err = svc.CheckDelegatedRecordAccess(ctx, org, []string{owner}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, allowed.GetAllowed())
	_, err = svc.CheckDelegatedRecordAccess(ctx, business.NewIDString(), []string{owner}, "example.record", "record-a", "read", current)
	require.Error(t, err)
}

// Both halves of the decision — the grant check's record CTE and the placement
// disclosure — resolve a record on (resource_type, resource_id) with no tenant
// predicate, because scope_nodes is RLS-scoped to app.current_org_id. The unique
// index is per tenant, so the same resource id may be placed in two of them, and
// a scope path is spelled the same in both; a leak between them would therefore
// still resolve to a real grant rather than fail closed.
func TestDelegatedRecordAccessIsolatesCollidingPlacementsAcrossTenants(t *testing.T) {
	const resource, collided = "example.record", "shared-record-id"
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{resource}},
	})
	place := func(org, path, id string) string {
		t.Helper()
		node, err := svc.ModulePlaceRecord(testCtx,
			business.ModuleCaller{PrincipalID: business.ModulePrincipalID("rows"), BoundOrg: org},
			org, path, "record", path, resource, id)
		require.NoError(t, err)
		return node
	}
	current := func(context.Context) error { return nil }

	orgA, ownerA, roleA := layeredFixture(t, resource, "read")
	orgB, ownerB, roleB := layeredFixture(t, resource, "read")
	// Each tenant grants on a branch the other does not, so resolving the wrong
	// tenant's placement changes the answer instead of merely the node id.
	for _, tenant := range []struct{ org, owner, role, branch string }{
		{orgA, ownerA, roleA, "allowed"},
		{orgB, ownerB, roleB, "private"},
	} {
		registerNode(t, tenant.org, "root", "root", "", "")
		registerNode(t, tenant.org, "root."+tenant.branch, "collection", "", "")
		require.NoError(t, testStore.WithOrgTx(testCtx, tenant.org, func(ctx context.Context) error {
			return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: tenant.org, SubjectId: tenant.owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root." + tenant.branch, RoleId: tenant.role})
		}))
	}
	nodeA := place(orgA, "root.allowed.record", collided)
	nodeB := place(orgB, "root.private.record", collided)
	require.NotEqual(t, nodeA, nodeB)
	place(orgB, "root.private.other", "b-only-record-id")

	ctxA := auth.WithVerifiedDatabaseIdentity(testCtx, ownerA, orgA)
	decision, err := svc.CheckDelegatedRecordAccess(ctxA, orgA, []string{ownerA}, resource, collided, "read", current)
	require.NoError(t, err)
	require.True(t, decision.GetAllowed())
	require.Equal(t, nodeA, decision.GetScopeNodeId())

	ctxB := auth.WithVerifiedDatabaseIdentity(testCtx, ownerB, orgB)
	decision, err = svc.CheckDelegatedRecordAccess(ctxB, orgB, []string{ownerB}, resource, collided, "read", current)
	require.NoError(t, err)
	require.True(t, decision.GetAllowed())
	require.Equal(t, nodeB, decision.GetScopeNodeId())

	// A record this tenant never placed, and an actor holding the grant only in
	// the other tenant, are both denied without disclosing any placement.
	for _, subjects := range [][]string{{ownerA}, {ownerA, ownerB}} {
		decision, err = svc.CheckDelegatedRecordAccess(ctxA, orgA, subjects, resource, "b-only-record-id", "read", current)
		require.NoError(t, err)
		require.False(t, decision.GetAllowed())
		require.Empty(t, decision.GetScopeNodeId())
	}
	decision, err = svc.CheckDelegatedRecordAccess(ctxA, orgA, []string{ownerA, ownerB}, resource, collided, "read", current)
	require.NoError(t, err)
	require.False(t, decision.GetAllowed())
	require.Empty(t, decision.GetScopeNodeId())
}
