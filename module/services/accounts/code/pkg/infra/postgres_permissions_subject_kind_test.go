package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func TestRoleAssignmentSubjectKindsCannotCrossMatchOnUUIDCollision(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))

	teamRoleID := business.NewIDString()
	principalRoleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		// Principal and Team identifiers live in separate namespaces and may
		// legally contain the same UUID. Authorization must always include the
		// subject kind in its direct-match predicate.
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id:    principalID,
			OrgId: orgID,
			Name:  "Collision Team",
			Slug:  "collision-" + principalID,
			Path:  "collision-" + principalID,
		}); err != nil {
			return err
		}
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          teamRoleID,
			Name:        "team collision " + teamRoleID,
			Description: "must only authorize the Team subject",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{
				Resource: "collision-team",
				Action:   "read",
			}},
		}); err != nil {
			return err
		}
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          principalRoleID,
			Name:        "principal collision " + principalRoleID,
			Description: "must only authorize the Principal subject",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{
				Resource: "collision-principal",
				Action:   "read",
			}},
		}); err != nil {
			return err
		}
		if err := testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   principalID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
			RoleId:      teamRoleID,
			OrgId:       orgID,
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   principalID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      principalRoleID,
			OrgId:       orgID,
		})
	}))

	check := func(kind gen.SubjectKind, resource string) bool {
		t.Helper()
		var allowed bool
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			var err error
			allowed, _, err = testStore.CheckPermission(
				ctx, principalID, kind, resource, "read", orgID, "",
			)
			return err
		}))
		return allowed
	}

	require.True(t, check(gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "collision-principal"))
	require.False(t, check(gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "collision-team"))
	require.True(t, check(gen.SubjectKind_SUBJECT_KIND_TEAM, "collision-team"))
	require.False(t, check(gen.SubjectKind_SUBJECT_KIND_TEAM, "collision-principal"))
}

func TestRoleAssignmentRejectsUnspecifiedSubjectKind(t *testing.T) {
	err := testStore.AssignRole(testCtx, &gen.RoleAssignment{
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_UNSPECIFIED,
	})
	require.ErrorContains(t, err, "unsupported subject kind")

	_, _, err = testStore.CheckPermission(
		testCtx,
		business.NewIDString(),
		gen.SubjectKind_SUBJECT_KIND_UNSPECIFIED,
		"resource",
		"read",
		"",
		"",
	)
	require.ErrorContains(t, err, "unsupported subject kind")
}
