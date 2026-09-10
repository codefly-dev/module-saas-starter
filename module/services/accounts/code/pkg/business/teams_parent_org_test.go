package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/adapters"
	authcore "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// teamMemberRoles covers every role a team write can request: the invariant is
// about who may hold a membership at all, so a privileged role must not be the
// only one that is refused.
var teamMemberRoles = map[string]gen.TeamRole{
	"member": gen.TeamRole_TEAM_ROLE_MEMBER,
	"admin":  gen.TeamRole_TEAM_ROLE_ADMIN,
	"owner":  gen.TeamRole_TEAM_ROLE_OWNER,
}

func teamMemberCount(t *testing.T, teamID, userID string) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.Pool().QueryRow(ctx,
			`SELECT COUNT(*) FROM team_members WHERE team_id = $1 AND user_id = $2`,
			teamID, userID).Scan(&count)
	}))
	return count
}

// TestAddTeamMemberRejectsOutsideOrganization is the audit reproducer: two
// independent organizations, and the second organization's user is offered to
// the first organization's team.
func TestAddTeamMemberRejectsOutsideOrganization(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	outsider, _ := mustUserAndOrg(t, testCtx, "outsider@example.com", "example-outsider", "ExampleCorp")
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)

	for name, role := range teamMemberRoles {
		t.Run(name, func(t *testing.T) {
			err := testService.AddTeamMember(testCtx, owner, &gen.AddTeamMemberRequest{
				TeamId: team.Team.Id, UserId: outsider, Role: role,
			})
			require.ErrorIs(t, err, business.ErrTeamMemberNotInParentOrganization)
			require.Zero(t, teamMemberCount(t, team.Team.Id, outsider))
		})
	}
}

// A user who does not exist at all must be indistinguishable from one who
// exists in another organization.
func TestAddTeamMemberRejectsUnknownUserWithTheSameError(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)

	err = testService.AddTeamMember(testCtx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id,
		UserId: "3f4b1d38-8f5a-4a2b-9d3e-2c1f0a5b6c7d",
		Role:   gen.TeamRole_TEAM_ROLE_MEMBER,
	})
	require.ErrorIs(t, err, business.ErrTeamMemberNotInParentOrganization)
}

// The invariant constrains who may be a member, not who may administer the
// team: a genuine parent member still joins, and a role change on that
// membership still applies.
func TestAddTeamMemberAcceptsParentOrganizationMemberAndRerole(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	colleague, _ := mustUserAndOrg(t, testCtx, "colleague@example.com", "example-colleague", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: colleague, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)

	require.NoError(t, testService.AddTeamMember(testCtx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id, UserId: colleague, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))
	require.NoError(t, testService.AddTeamMember(testCtx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id, UserId: colleague, Role: gen.TeamRole_TEAM_ROLE_ADMIN,
	}))

	members, err := testService.ListTeamMembers(testCtx, &gen.ListTeamMembersRequest{TeamId: team.Team.Id})
	require.NoError(t, err)
	require.Len(t, members.Members, 1)
	require.Equal(t, gen.TeamRole_TEAM_ROLE_ADMIN, members.Members[0].Role)
}

// Losing the parent membership retires the team membership with it, and
// rejoining the organization does not bring the old team role back.
func TestOrganizationRemovalRetiresTeamMembershipsAndDoesNotRestoreThem(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	colleague, _ := mustUserAndOrg(t, testCtx, "colleague@example.com", "example-colleague", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: colleague, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)
	require.NoError(t, testService.AddTeamMember(testCtx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id, UserId: colleague, Role: gen.TeamRole_TEAM_ROLE_ADMIN,
	}))

	// Delete the membership row alone, so the cascade — not the application's
	// own cleanup — is what has to remove the team row.
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.RemoveOrgMember(ctx, org, colleague)
	}))
	require.Zero(t, teamMemberCount(t, team.Team.Id, colleague))

	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: colleague, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	require.Zero(t, teamMemberCount(t, team.Team.Id, colleague),
		"rejoining the organization must not reactivate an obsolete team role")
}

// The invariant has to survive a writer that never goes through the service:
// both supported runtime roles are held to it, with and without a truthful
// org_id on the row.
func TestTeamMembershipInvariantHoldsForDirectDatabaseWrites(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	outsider, outsiderOrg := mustUserAndOrg(t, testCtx, "outsider@example.com", "example-outsider", "ExampleCorp")
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)

	insert := func(ctx context.Context, rowOrg string) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx,
			`INSERT INTO team_members (team_id, org_id, user_id, role) VALUES ($1, $2, $3, 'admin')`,
			team.Team.Id, rowOrg, outsider)
		return err
	}

	for name, run := range map[string]func(context.Context, func(context.Context) error) error{
		"app_tenant": func(ctx context.Context, fn func(context.Context) error) error {
			return testStore.WithOrgTx(ctx, org, fn)
		},
		"app_control_plane": testStore.WithControlPlane,
	} {
		t.Run(name+"/truthful org_id", func(t *testing.T) {
			require.Error(t, run(testCtx, func(ctx context.Context) error { return insert(ctx, org) }))
			require.Zero(t, teamMemberCount(t, team.Team.Id, outsider))
		})
		t.Run(name+"/borrowed org_id", func(t *testing.T) {
			require.Error(t, run(testCtx, func(ctx context.Context) error { return insert(ctx, outsiderOrg) }))
			require.Zero(t, teamMemberCount(t, team.Team.Id, outsider))
		})
	}
}

// Neither commit order may leave a team membership whose parent is gone. The
// insert's referential check locks the organization_members row, so the second
// transaction observes the first rather than racing past it.
func TestAddTeamMemberRacingOrganizationRemovalLeavesNoOrphan(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	colleague, _ := mustUserAndOrg(t, testCtx, "colleague@example.com", "example-colleague", "ExampleCorp")
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)

	rejoin := func(t *testing.T) {
		t.Helper()
		require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: colleague, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		}))
	}

	t.Run("insert commits first", func(t *testing.T) {
		rejoin(t)
		inserted := make(chan struct{})
		removed := make(chan error, 1)
		go func() {
			<-inserted
			removed <- testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
				return testStore.RemoveOrgMember(ctx, org, colleague)
			})
		}()
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			if err := testStore.AddTeamMember(ctx, team.Team.Id, colleague, "admin"); err != nil {
				return err
			}
			close(inserted)
			requireRemovalIsWaiting(t)
			return nil
		}))
		require.NoError(t, <-removed)
		require.Zero(t, teamMemberCount(t, team.Team.Id, colleague))
	})

	t.Run("removal commits first", func(t *testing.T) {
		rejoin(t)
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			return testStore.RemoveOrgMember(ctx, org, colleague)
		}))
		err := testService.AddTeamMember(testCtx, owner, &gen.AddTeamMemberRequest{
			TeamId: team.Team.Id, UserId: colleague, Role: gen.TeamRole_TEAM_ROLE_ADMIN,
		})
		require.ErrorIs(t, err, business.ErrTeamMemberNotInParentOrganization)
		require.Zero(t, teamMemberCount(t, team.Team.Id, colleague))
	})
}

// requireRemovalIsWaiting proves the two transactions genuinely contend rather
// than passing each other: the removal cannot proceed while the insert holds
// the membership row.
func requireRemovalIsWaiting(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		if err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.Pool().QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted)`).Scan(&waiting)
		}); err != nil {
			return false
		}
		return waiting
	}, 10*time.Second, 20*time.Millisecond,
		"the organization-member removal should be blocked by the team insert")
}

// The exposed handler refuses the same write, and reports ineligibility rather
// than an internal fault or a hint about the target.
func TestTeamServerAddMemberRejectsOutsideOrganization(t *testing.T) {
	clearData(t)

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	outsider, _ := mustUserAndOrg(t, testCtx, "outsider@example.com", "example-outsider", "ExampleCorp")
	team, err := testService.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "Example Team"})
	require.NoError(t, err)

	adapters.WithService(testService)

	_, err = (&adapters.TeamServer{}).AddMember(
		callerContext(owner, org),
		&gen.AddTeamMemberRequest{TeamId: team.Team.Id, UserId: outsider, Role: gen.TeamRole_TEAM_ROLE_ADMIN},
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, business.ErrTeamMemberNotInParentOrganization.Error(), status.Convert(err).Message())
	require.Zero(t, teamMemberCount(t, team.Team.Id, outsider))

	// The caller's own authority is still answered separately, and a verified
	// session naming this organization does not stand in for membership of it.
	_, err = (&adapters.TeamServer{}).AddMember(
		callerContext(outsider, org),
		&gen.AddTeamMemberRequest{TeamId: team.Team.Id, UserId: owner, Role: gen.TeamRole_TEAM_ROLE_ADMIN},
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// callerContext reproduces what the authentication interceptor installs before
// a handler runs: the caller's id for requireAuth, and the verified
// tenant/user pair the membership lookups refuse to work without.
func callerContext(userID, orgID string) context.Context {
	return authcore.WithVerifiedDatabaseIdentity(
		context.WithValue(testCtx, wool.UserIDKey, userID), userID, orgID)
}
