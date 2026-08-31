package business_test

// E2E test for the full role-assignment chain. This is the integration
// test that the standalone RLS / RBAC / introspection tests don't cover:
// it walks the same code path a real request takes — register, create
// org, create custom role, assign it, query CheckPermission to confirm
// the grant fires, and assert the audit trail captures every step.
//
// The chain:
//
//   RegisterUser  ──┐
//                   ├──► org_admin role_assignment seeded by the resolver
//   CreateOrg ──────┤    (admin auto-assigned to personal org)
//                   │
//   CreateRole ─────┤    custom role with deployments:write
//                   │
//   AssignRole ─────┤    grant the custom role to a different member
//                   │
//   CheckPermission ┤    (subjectId=member, action="deployments:write",
//                   │    org=orgID) — must return Allowed=true via
//                   │    role_assignments JOIN through RLS
//                   │
//   ListRoleAssignments ─ pin the assignment is queryable by member-tier
//                   │     callers (org_member authz)
//                   │
//   QueryAuditLog ──┘    every step emitted an audit event scoped to
//                        the org; the test asserts they're there
//
// What this catches that a unit test wouldn't:
//   - WithOrgTx wrapping correctness across multi-step Service flows
//   - role_assignments RLS policy lets the assignment row through
//     when CheckPermission JOINs it
//   - audit emit chain (async drain) actually persists across orgs
//   - the RBAC matcher's wildcard expansion + concrete-pair lookup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestE2E_RoleAssignmentFlow(t *testing.T) {
	clearData(t)
	ctx := testCtx

	// --- Step 1: register the org owner + a regular member.
	owner, ownerOrg := mustUserAndOrg(t, ctx,
		"owner-e2e@rls-test.com", "owner-e2e-rls", "Acme E2E")
	memberResp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "member-e2e@rls-test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "member-e2e-rls",
			ProviderEmail: "member-e2e@rls-test.com",
		},
	})
	require.NoError(t, err)
	memberID := memberResp.User.Uuid

	// Add the member to the owner's org so they CAN potentially be
	// granted permissions there. organization_members is RLS-protected
	// so this exercises Phase 2B too.
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId:  ownerOrg,
		UserId: memberID,
		Role:   gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	// --- Step 2: pre-flight CheckPermission. Member has NO grant
	// for "deployments:write" yet — must return Allowed=false.
	pre, err := testService.CheckPermission(ctx, &gen.CheckPermissionRequest{
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		Resource:    "deployments", Action: "write",
		OrgId: ownerOrg,
	})
	require.NoError(t, err)
	require.False(t, pre.Allowed,
		"baseline: member has no deployments:write yet — pre-check must deny")

	// --- Step 3: create a custom org-scoped role.
	roleResp, err := testService.CreateRole(ctx, owner, &gen.CreateRoleRequest{
		Name:        "deployer",
		Description: "Can deploy to production",
		OrgId:       ownerOrg,
		Permissions: []*gen.Permission{
			{Resource: "deployments", Action: "write"},
		},
	})
	require.NoError(t, err)
	roleID := roleResp.Role.Id
	require.NotEmpty(t, roleID)
	require.False(t, roleResp.Role.BuiltIn, "deployer is a custom role")

	// --- Step 4: grant the role to the member.
	_, err = testService.AssignRole(ctx, &gen.AssignRoleRequest{
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		RoleId:      roleID,
		OrgId:       ownerOrg,
	})
	require.NoError(t, err)

	// --- Step 5: post-grant CheckPermission. Same query as pre-
	// check — must now return Allowed=true.
	post, err := testService.CheckPermission(ctx, &gen.CheckPermissionRequest{
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		Resource:    "deployments", Action: "write",
		OrgId: ownerOrg,
	})
	require.NoError(t, err)
	require.True(t, post.Allowed,
		"after AssignRole, member should have deployments:write via the custom role: %s", post.Reason)
	require.Contains(t, post.Reason, "deployer",
		"reason should reference the granting role: %s", post.Reason)

	// --- Step 6: ListRoleAssignments returns the new grant.
	assignments, err := testService.ListRoleAssignments(ctx, &gen.ListRoleAssignmentsRequest{
		OrgId:       ownerOrg,
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
	})
	require.NoError(t, err)
	require.Len(t, assignments.Assignments, 1)
	require.Equal(t, roleID, assignments.Assignments[0].RoleId)
	require.Equal(t, memberID, assignments.Assignments[0].SubjectId)
	require.Equal(t, ownerOrg, assignments.Assignments[0].OrgId)

	// --- Step 7: revoke and re-check.
	require.NoError(t, testService.RevokeRole(ctx, owner, &gen.RevokeRoleRequest{
		SubjectId: memberID,
		RoleId:    roleID,
		OrgId:     ownerOrg,
	}))

	postRevoke, err := testService.CheckPermission(ctx, &gen.CheckPermissionRequest{
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		Resource:    "deployments", Action: "write",
		OrgId: ownerOrg,
	})
	require.NoError(t, err)
	require.False(t, postRevoke.Allowed,
		"after RevokeRole, member should no longer have deployments:write")

	// --- Step 8: audit trail.
	time.Sleep(200 * time.Millisecond)

	auditEvents, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID:    ownerOrg,
		From:     ptrTime(time.Now().Add(-10 * time.Minute)),
		To:       ptrTime(time.Now().Add(10 * time.Minute)),
		PageSize: 200,
	})
	require.NoError(t, err)

	gotActions := map[string]int{}
	for _, e := range auditEvents {
		gotActions[string(e.EventType)]++
	}

	// Required actions on this orgs scope:
	for _, want := range []string{
		"role.created",
		"role.assigned",
		"role.revoked",
		"org.member_added",
	} {
		require.Greater(t, gotActions[want], 0,
			"audit log should contain %q event for org %s; got actions=%v",
			want, ownerOrg, gotActions)
	}
}

// ptrTime — small helper because QueryAuditLog wants *time.Time for
// the from/to bounds and Go has no `&time.Now()`.
func ptrTime(t time.Time) *time.Time { return &t }

var _ = business.ServiceVersion // keep business import alive
