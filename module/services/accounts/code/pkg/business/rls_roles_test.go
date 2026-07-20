package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_Roles_BuiltInsGloballyVisible — Phase 2E polymorphic policy.
// Built-in roles (org_id IS NULL) must be visible from inside any
// tenant's tx; tenant-specific roles must NOT cross-leak.
func TestRLS_Roles_BuiltInsGloballyVisible(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "alice-roles@rls-test.com", "alice-roles-rls", "Acme R A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-roles@rls-test.com", "bob-roles-rls", "Acme R B")

	// Each org creates a custom role.
	roleA, err := testService.CreateRole(ctx, ownerA, &gen.CreateRoleRequest{
		Name: "custom-A", Description: "tenant A role", OrgId: orgA,
		Permissions: []*gen.Permission{{Resource: "users", Action: "read"}},
	})
	require.NoError(t, err)
	require.NotNil(t, roleA.Role)

	roleB, err := testService.CreateRole(ctx, ownerA /* fixture, not real auth */, &gen.CreateRoleRequest{
		Name: "custom-B", Description: "tenant B role", OrgId: orgB,
		Permissions: []*gen.Permission{{Resource: "users", Action: "read"}},
	})
	require.NoError(t, err)
	require.NotNil(t, roleB.Role)

	// As A: see built-ins + custom-A, NOT custom-B.
	listA, err := testService.ListRoles(ctx, &gen.ListRolesRequest{OrgId: orgA})
	require.NoError(t, err)
	names := make(map[string]bool)
	for _, r := range listA.Roles {
		names[r.Name] = true
	}
	require.True(t, names["admin"], "built-in 'admin' must be visible to tenant")
	require.True(t, names["custom-A"], "tenant A's custom role visible to A")
	require.False(t, names["custom-B"], "tenant B's custom role MUST NOT be visible to A")

	// Probe: from A's tx, ask for B's roles via the Store.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListRoles(ctx, orgB)
		require.NoError(t, err)
		for _, r := range stolen {
			require.NotEqual(t, "custom-B", r.Name,
				"RLS must hide B's custom role from A's tx")
		}
		return nil
	}))

	// Built-in-only path (orgID==""). Service uses WithControlPlane —
	// returns built-ins (already globally visible).
	builtIns, err := testService.ListRoles(ctx, &gen.ListRolesRequest{OrgId: ""})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(builtIns.Roles), 3, "expected at least 3 built-ins (admin, editor, viewer)")
}

// TestRLS_RoleAssignments_CrossTenantBlocked — assignments with a
// concrete org_id must not be visible to other tenants. The test
// asserts CheckPermission sees the right grant from inside each
// tenant's tx.
//
// Note: RegisterUser auto-creates a personal org and assigns admin
// there. The explicit "Acme" org returned by mustUserAndOrg has the
// user as 'owner' in organization_members but NO role_assignments
// row. This test explicitly assigns the admin role to userA in orgA
// + userB in orgB so we have something concrete to probe.
func TestRLS_RoleAssignments_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, orgA := mustUserAndOrg(t, ctx, "alice-ra@rls-test.com", "alice-ra-rls", "Acme RA A")
	userB, orgB := mustUserAndOrg(t, ctx, "bob-ra@rls-test.com", "bob-ra-rls", "Acme RA B")

	// Pull the built-in admin role id (org_id IS NULL) — globally
	// readable under polymorphic RLS.
	adminRoleID := ""
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		rs, err := testStore.ListRoles(ctx, "")
		if err != nil {
			return err
		}
		for _, r := range rs {
			if r.Name == "admin" && r.BuiltIn {
				adminRoleID = r.Id
				return nil
			}
		}
		return nil
	}))
	require.NotEmpty(t, adminRoleID, "built-in admin role missing — migration 4 didn't seed?")

	// Assign admin to each user in their respective explicit org.
	_, err := testService.AssignRole(ctx, &gen.AssignRoleRequest{
		SubjectId:   userA,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		RoleId:      adminRoleID,
		OrgId:       orgA,
	})
	require.NoError(t, err)
	_, err = testService.AssignRole(ctx, &gen.AssignRoleRequest{
		SubjectId:   userB,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		RoleId:      adminRoleID,
		OrgId:       orgB,
	})
	require.NoError(t, err)

	// User A in org A → allowed.
	checkA, err := testService.CheckPermission(ctx, &gen.CheckPermissionRequest{
		SubjectId:   userA,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		Resource:    "users", Action: "write",
		OrgId: orgA,
	})
	require.NoError(t, err)
	require.True(t, checkA.Allowed, "user A should have admin on org A")

	// User A queried in org B's scope: assignment for A is in org A,
	// so under org B's WithOrgTx the role_assignment row is hidden
	// by RLS — CheckPermission must return false.
	checkAcrossB, err := testService.CheckPermission(ctx, &gen.CheckPermissionRequest{
		SubjectId:   userA,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		Resource:    "users", Action: "write",
		OrgId: orgB,
	})
	require.NoError(t, err)
	require.False(t, checkAcrossB.Allowed,
		"user A's assignment in org A must be invisible from org B's scope (RLS-protected)")

	// User B in org B → allowed (own org).
	checkB, err := testService.CheckPermission(ctx, &gen.CheckPermissionRequest{
		SubjectId:   userB,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		Resource:    "users", Action: "write",
		OrgId: orgB,
	})
	require.NoError(t, err)
	require.True(t, checkB.Allowed)
}
