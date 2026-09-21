//go:build !pure

package business_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func roleByID(t *testing.T, orgID, roleID string) *gen.Role {
	t.Helper()
	list, err := testService.ListRoles(testCtx, &gen.ListRolesRequest{OrgId: orgID})
	require.NoError(t, err)
	for _, role := range list.Roles {
		if role.Id == roleID {
			return role
		}
	}
	return nil
}

func permissionSet(role *gen.Role) map[string]bool {
	set := make(map[string]bool, len(role.Permissions))
	for _, p := range role.Permissions {
		set[p.Resource+":"+p.Action] = true
	}
	return set
}

// Replacing a permission set has to shrink it, which is the half the tenant
// could not express before: role_permissions carried INSERT and SELECT
// policies only, so a DELETE under the tenant transaction matched no rows and
// reported success. Reading the role back — rather than trusting the response
// the store composed — is what makes this test see that.
func TestUpdateRole_ReplacesThePermissionSetAndKeepsAssignments(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@role-update.test", "owner-role-update", "Acme Role Update")

	created, err := testService.CreateRole(ctx, owner, &gen.CreateRoleRequest{
		Name: "analyst", Description: "reads everything", OrgId: orgID,
		Permissions: []*gen.Permission{
			{Resource: "users", Action: "read"},
			{Resource: "billing", Action: "read"},
		},
	})
	require.NoError(t, err)

	_, err = testService.AssignRole(ctx, &gen.AssignRoleRequest{
		SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		RoleId: created.Role.Id, OrgId: orgID,
	})
	require.NoError(t, err)

	updated, err := testService.UpdateRole(ctx, owner, &gen.UpdateRoleRequest{
		Id: created.Role.Id, OrgId: orgID, Description: "reads users only",
		Permissions: []*gen.Permission{{Resource: "users", Action: "read"}},
	})
	require.NoError(t, err)
	require.Equal(t, "analyst", updated.Role.Name)

	stored := roleByID(t, orgID, created.Role.Id)
	require.NotNil(t, stored)
	require.Equal(t, "reads users only", stored.Description)
	require.Equal(t, map[string]bool{"users:read": true}, permissionSet(stored))

	assignments, err := testService.ListRoleAssignments(ctx, &gen.ListRoleAssignmentsRequest{
		OrgId: orgID, SubjectId: owner,
	})
	require.NoError(t, err)
	require.Len(t, assignments.Assignments, 1,
		"editing a role must not cost it its assignments — that is the whole point of not deleting and recreating")
	require.Equal(t, created.Role.Id, assignments.Assignments[0].RoleId)
}

// An empty permission list is a grant set of nothing, not "leave it alone":
// the replace is wholesale, and an administrator revoking every permission
// has to be able to say so.
func TestUpdateRole_AnEmptyPermissionListClearsTheGrantSet(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@role-clear.test", "owner-role-clear", "Acme Role Clear")

	created, err := testService.CreateRole(ctx, owner, &gen.CreateRoleRequest{
		Name: "temp", OrgId: orgID,
		Permissions: []*gen.Permission{{Resource: "users", Action: "write"}},
	})
	require.NoError(t, err)

	_, err = testService.UpdateRole(ctx, owner, &gen.UpdateRoleRequest{
		Id: created.Role.Id, OrgId: orgID,
	})
	require.NoError(t, err)

	stored := roleByID(t, orgID, created.Role.Id)
	require.NotNil(t, stored)
	require.Empty(t, stored.Permissions)
}

// The scope the caller was authorized for is part of the row lookup, so a
// role id from another tenant is not found rather than found-and-refused —
// the handler authorizes org_id, and org_id alone must not reach elsewhere.
func TestUpdateRole_RefusesARoleInAnotherScope(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "a@role-scope.test", "a-role-scope", "Acme Scope A")
	_, orgB := mustUserAndOrg(t, ctx, "b@role-scope.test", "b-role-scope", "Acme Scope B")

	roleA, err := testService.CreateRole(ctx, ownerA, &gen.CreateRoleRequest{
		Name: "scoped", OrgId: orgA,
		Permissions: []*gen.Permission{{Resource: "users", Action: "read"}},
	})
	require.NoError(t, err)

	_, err = testService.UpdateRole(ctx, ownerA, &gen.UpdateRoleRequest{
		Id: roleA.Role.Id, OrgId: orgB, Description: "stolen",
		Permissions: []*gen.Permission{{Resource: "*", Action: "*"}},
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))

	stored := roleByID(t, orgA, roleA.Role.Id)
	require.NotNil(t, stored)
	require.Equal(t, map[string]bool{"users:read": true}, permissionSet(stored))
}

// Built-in roles are the platform's standard vocabulary, and a tenant naming
// a global role id must not reach it either. DeleteRole already draws this
// line; UpdateRole draws the same one.
func TestUpdateRole_RefusesBuiltInRoles(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@role-builtin.test", "owner-role-builtin", "Acme Role Builtin")

	globals, err := testService.ListRoles(ctx, &gen.ListRolesRequest{})
	require.NoError(t, err)
	var builtIn *gen.Role
	for _, role := range globals.Roles {
		if role.BuiltIn {
			builtIn = role
			break
		}
	}
	require.NotNil(t, builtIn, "the role catalog must seed at least one built-in role")

	_, err = testService.UpdateRole(ctx, owner, &gen.UpdateRoleRequest{
		Id: builtIn.Id, Description: "widened",
		Permissions: []*gen.Permission{{Resource: "*", Action: "*"}},
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	_, err = testService.UpdateRole(ctx, owner, &gen.UpdateRoleRequest{
		Id: builtIn.Id, OrgId: orgID, Description: "widened",
		Permissions: []*gen.Permission{{Resource: "*", Action: "*"}},
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"a tenant naming a global role must not even find it")

	stored := roleByID(t, orgID, builtIn.Id)
	require.NotNil(t, stored)
	require.NotEqual(t, "widened", stored.Description)
}

// ON CONFLICT DO NOTHING collapses a duplicate the caller sent, so echoing the
// request would claim a grant set the role does not hold.
func TestUpdateRole_ReturnsTheStoredSetNotTheRequestedOne(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@role-readback.test", "owner-role-readback", "Acme Role Readback")

	created, err := testService.CreateRole(ctx, owner, &gen.CreateRoleRequest{
		Name: "dedupe", OrgId: orgID,
	})
	require.NoError(t, err)

	updated, err := testService.UpdateRole(ctx, owner, &gen.UpdateRoleRequest{
		Id: created.Role.Id, OrgId: orgID,
		Permissions: []*gen.Permission{
			{Resource: "users", Action: "read"},
			{Resource: "users", Action: "read"},
		},
	})
	require.NoError(t, err)
	require.Len(t, updated.Role.Permissions, 1,
		"the response must describe the role as stored, not the request as sent")
	require.Equal(t, map[string]bool{"users:read": true}, permissionSet(updated.Role))
}
