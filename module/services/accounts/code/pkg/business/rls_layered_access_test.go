package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_LayeredAccess_CrossTenantAndFailClosed proves the tenant floor holds
// for the three new relations: scope grants/nodes and record shares created in
// one org are invisible to another org's transaction, and every read fails
// closed when run without an org-scoped transaction. It also exercises the full
// service → store → audit wiring for the management RPCs.
func TestRLS_LayeredAccess_CrossTenantAndFailClosed(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "alice-la@rls-test.com", "alice-la-rls", "Layered A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-la@rls-test.com", "bob-la-rls", "Layered B")

	roleA := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleA, Name: "reader " + roleA, Description: "doc:read", OrgId: orgA,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
		})
	}))

	// orgA builds a scope tree, places a record, grants at the root, and shares
	// a second record — all through the service RPCs.
	_, err := testService.RegisterScopeNode(ctx, ownerA, &gen.RegisterScopeNodeRequest{
		OrgId: orgA, ScopePath: "space", Kind: "space", Label: "Space",
	})
	require.NoError(t, err)
	_, err = testService.RegisterScopeNode(ctx, ownerA, &gen.RegisterScopeNodeRequest{
		OrgId: orgA, ScopePath: "space.doc_1", Kind: "record", Label: "Doc 1",
		ResourceType: "doc", ResourceId: "doc-a",
	})
	require.NoError(t, err)
	_, err = testService.GrantScope(ctx, ownerA, &gen.GrantScopeRequest{
		OrgId: orgA, SubjectId: ownerA, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath: "space", RoleId: roleA,
	})
	require.NoError(t, err)
	_, err = testService.ShareRecord(ctx, ownerA, &gen.ShareRecordRequest{
		OrgId: orgA, ResourceType: "doc", ResourceId: "shared-a",
		SubjectId: ownerA, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: roleA,
	})
	require.NoError(t, err)

	access := func(orgScope string, unwrapped bool) bool {
		t.Helper()
		var allowed bool
		run := func(ctx context.Context) error {
			var e error
			allowed, _, e = testStore.CheckAccess(ctx, ownerA, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "doc-a", "read")
			return e
		}
		if unwrapped {
			require.NoError(t, run(context.Background()))
		} else {
			require.NoError(t, testStore.WithOrgTx(ctx, orgScope, run))
		}
		return allowed
	}

	// Own org: the scope grant + node resolve, so access is allowed.
	require.True(t, access(orgA, false), "orgA must see its own scope grant")
	shares, err := testService.ListShares(ctx, &gen.ListSharesRequest{OrgId: orgA, ResourceType: "doc", ResourceId: "shared-a"})
	require.NoError(t, err)
	require.Len(t, shares.GetShares(), 1)

	// Cross-tenant: orgB's transaction cannot see orgA's node/grant, so the same
	// check denies; and orgB sees none of orgA's shares.
	require.False(t, access(orgB, false), "RLS must hide orgA's scope grant + node from orgB")
	crossShares, err := testService.ListShares(ctx, &gen.ListSharesRequest{OrgId: orgB, ResourceType: "doc", ResourceId: "shared-a"})
	require.NoError(t, err)
	require.Empty(t, crossShares.GetShares(), "RLS must hide orgA's share from orgB")

	// Fail-closed: an un-wrapped connection (no app.current_org_id) sees nothing.
	require.False(t, access("", true), "un-wrapped CheckAccess must fail closed")
	noWrapShares, err := testStore.ListShares(context.Background(), orgA, "doc", "shared-a")
	require.NoError(t, err)
	require.Empty(t, noWrapShares, "un-wrapped ListShares must return nothing (RLS fail-closed)")
}
