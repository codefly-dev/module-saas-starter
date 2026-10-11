//go:build !pure

package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// exposureWorld is one organization with an installed solution, a team, a member
// of that team, a member outside it, and a role that permits using a solution;
// and a second organization with a team and an installation of its own.
type exposureWorld struct {
	admin, inTeam, outside  string
	org, team, useRole      string
	installation, target    string
	alias                   string
	otherAdmin, otherOrg    string
	otherTeam, otherInstall string
	productRole             string
}

func createRole(t *testing.T, orgID, name, resource, action string) string {
	t.Helper()
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleID, Name: name + " " + roleID, OrgId: orgID,
			Permissions: []*gen.Permission{{Resource: resource, Action: action}},
		})
	}))
	return roleID
}

func installForExposure(t *testing.T, adminID, orgID string) (installationID, targetID, alias string) {
	t.Helper()
	agentRole := createRole(t, orgID, "agent", "doc", "write")
	targetID, alias = declarePresence(t, "exposed-solution")
	installation, err := testService.InstallSolution(testCtx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/exposed:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "Exposed",
		RoleID:          agentRole,
	})
	require.NoError(t, err)
	return installation.GetId(), targetID, alias
}

func newExposureWorld(t *testing.T) *exposureWorld {
	t.Helper()
	clearData(t)
	adapters.WithService(testService)
	w := &exposureWorld{}
	w.admin, w.org = mustUserAndOrg(t, testCtx, "admin@exposure.test", "exposure-admin", "Acme Exposure")
	w.inTeam = mustMember(t, w.admin, w.org, "in@exposure.test", "exposure-in")
	w.outside = mustMember(t, w.admin, w.org, "out@exposure.test", "exposure-out")
	team, err := testService.CreateTeam(testCtx, w.admin, &gen.CreateTeamRequest{OrgId: w.org, Name: "finance"})
	require.NoError(t, err)
	w.team = team.Team.Id
	require.NoError(t, testService.AddTeamMember(testCtx, w.admin, &gen.AddTeamMemberRequest{
		TeamId: w.team, UserId: w.inTeam, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))
	w.useRole = createRole(t, w.org, "solution user", business.ResourceTypeSolution, business.ActionUseSolution)
	w.productRole = createRole(t, w.org, "doc reader", "doc", "read")
	w.installation, w.target, w.alias = installForExposure(t, w.admin, w.org)

	w.otherAdmin, w.otherOrg = mustUserAndOrg(t, testCtx, "admin@other.test", "other-admin", "Other Org")
	otherTeam, err := testService.CreateTeam(testCtx, w.otherAdmin, &gen.CreateTeamRequest{OrgId: w.otherOrg, Name: "intruders"})
	require.NoError(t, err)
	w.otherTeam = otherTeam.Team.Id
	w.otherInstall, _, _ = installForExposure(t, w.otherAdmin, w.otherOrg)
	return w
}

func entitledInstallations(t *testing.T, orgID, subjectID string) []string {
	t.Helper()
	resp, err := testService.ListSolutionEntitlements(testCtx, &gen.ListSolutionEntitlementsRequest{
		OrgId: orgID, SubjectId: subjectID,
	})
	require.NoError(t, err)
	ids := make([]string, 0, len(resp.GetEntitlements()))
	for _, entitlement := range resp.GetEntitlements() {
		ids = append(ids, entitlement.GetInstallationId())
	}
	return ids
}

// Installed is not exposed: nobody sees the solution until an administrator
// grants a team, and then exactly that team's members do. The grant is what
// ListSolutionEntitlements resolves through, so this is the capability check the
// menu and the solution proxy rely on, not a second reading of the same row.
func TestGrantInstallationToTeamExposesItToThatTeamOnly(t *testing.T) {
	w := newExposureWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)

	require.Empty(t, entitledInstallations(t, w.org, w.inTeam), "installed is not exposed")

	granted, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	})
	require.NoError(t, err)
	require.Equal(t, gen.SubjectKind_SUBJECT_KIND_TEAM, granted.GetGrant().GetSubjectKind())

	require.Equal(t, []string{w.installation}, entitledInstallations(t, w.org, w.inTeam))
	require.Empty(t, entitledInstallations(t, w.org, w.outside), "a member outside the team does not see it")

	// The grant is a scope grant like any other: same event, same audit row.
	events, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID: w.org, EventType: string(business.EventScopeGranted), PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, w.admin, events[0].ActorID)
	require.Equal(t, w.team, events[0].Payload["subject_id"])

	// And the organization's listing shows the installation, its target, its
	// authority root and the team granted there.
	listed, err := server.ListOrganizationInstallations(admin, &gen.ListOrganizationInstallationsRequest{OrgId: w.org})
	require.NoError(t, err)
	require.Len(t, listed.GetInstallations(), 1)
	row := listed.GetInstallations()[0]
	require.Equal(t, w.installation, row.GetInstallation().GetId())
	require.Equal(t, w.alias, row.GetTarget().GetRouteAlias())
	require.Equal(t, w.target, row.GetTarget().GetTargetId())
	require.True(t, row.GetTarget().GetInstalled())
	require.NotEmpty(t, row.GetRootScopePath())
	require.Len(t, row.GetGrantedTeams(), 1)
	require.Equal(t, w.team, row.GetGrantedTeams()[0].GetGrant().GetSubjectId())
	require.Equal(t, row.GetRootScopePath(), row.GetGrantedTeams()[0].GetGrant().GetScopePath())
	require.Empty(t, row.GetInheritedTeams())
}

// Each organization's listing is its own: neither sees the other's
// installation, and the one it sees is the one it installed.
func TestListOrganizationInstallationsIsTenantScoped(t *testing.T) {
	w := newExposureWorld(t)
	server := &adapters.InstallationServer{}

	mine, err := server.ListOrganizationInstallations(storyCallerContext(testCtx, w.admin, w.org),
		&gen.ListOrganizationInstallationsRequest{OrgId: w.org})
	require.NoError(t, err)
	theirs, err := server.ListOrganizationInstallations(storyCallerContext(testCtx, w.otherAdmin, w.otherOrg),
		&gen.ListOrganizationInstallationsRequest{OrgId: w.otherOrg})
	require.NoError(t, err)
	require.Len(t, mine.GetInstallations(), 1)
	require.Len(t, theirs.GetInstallations(), 1)
	require.Equal(t, w.installation, mine.GetInstallations()[0].GetInstallation().GetId())
	require.Equal(t, w.otherInstall, theirs.GetInstallations()[0].GetInstallation().GetId())

	// Naming the other organization is refused before anything is read.
	_, err = server.ListOrganizationInstallations(storyCallerContext(testCtx, w.admin, w.org),
		&gen.ListOrganizationInstallationsRequest{OrgId: w.otherOrg})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// The attacks: another organization's installation is not found, a team of
// another organization is refused, a role that would expose nothing is refused,
// and a member who is not an administrator is denied. None of them writes a grant.
func TestGrantInstallationToTeamRefusals(t *testing.T) {
	w := newExposureWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)

	_, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.otherInstall, TeamId: w.team, RoleId: w.useRole,
	})
	require.Equal(t, codes.NotFound, status.Code(err), "another organization's installation does not exist here")

	_, err = server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.otherTeam, RoleId: w.useRole,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a team of another organization is refused")

	_, err = server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.productRole,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "a role that does not permit solution use exposes nothing")

	_, err = server.GrantInstallationToTeam(storyCallerContext(testCtx, w.inTeam, w.org), &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a member who is not an administrator is denied")

	_, err = server.RevokeInstallationFromTeam(admin, &gen.RevokeInstallationFromTeamRequest{
		OrgId: w.org, InstallationId: w.otherInstall, TeamId: w.team, RoleId: w.useRole,
	})
	require.Equal(t, codes.NotFound, status.Code(err), "nor can another organization's installation be narrowed")

	require.Empty(t, entitledInstallations(t, w.org, w.inTeam), "no refused request wrote a grant")
	require.Empty(t, entitledInstallations(t, w.otherOrg, w.otherAdmin))
}

// Revoking is a narrowing under the policy log: with a witness it hides the
// solution from the team again; with none, the host refuses in its own words and
// the team keeps what it was granted.
func TestRevokeInstallationFromTeamIsAWitnessedNarrowing(t *testing.T) {
	w := newExposureWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)
	grant := &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	}
	revoke := &gen.RevokeInstallationFromTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	}
	_, err := server.GrantInstallationToTeam(admin, grant)
	require.NoError(t, err)

	testService.SetPolicyLog(nil, nil)
	t.Cleanup(func() { wirePolicyLogForTests(testService, testStore) })
	_, err = server.RevokeInstallationFromTeam(admin, revoke)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "this host has no policy log, so authority cannot be narrowed")
	require.Equal(t, []string{w.installation}, entitledInstallations(t, w.org, w.inTeam),
		"an unwitnessed revoke changed nothing")

	wirePolicyLogForTests(testService, testStore)
	_, err = server.RevokeInstallationFromTeam(admin, revoke)
	require.NoError(t, err)
	require.Empty(t, entitledInstallations(t, w.org, w.inTeam), "revoking hides it again")
}

// requireARowLockWaiter proves two transactions genuinely contend on a row
// rather than passing each other.
func requireARowLockWaiter(t *testing.T, msg string) {
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
	}, 10*time.Second, 20*time.Millisecond, msg)
}

// A grant and an uninstall of the same installation serialize on its row, in
// both orders, so a grant never lands on an installation already uninstalled.
func TestGrantInstallationToTeamSerializesWithUninstall(t *testing.T) {
	t.Run("uninstall commits first: the waiting grant finds nothing", func(t *testing.T) {
		w := newExposureWorld(t)
		granted := make(chan error, 1)
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, transitioned, err := testStore.UninstallSolution(ctx, w.org, w.installation)
			if err != nil {
				return err
			}
			require.True(t, transitioned)
			go func() {
				_, err := testService.GrantInstallationToTeam(testCtx, w.admin, w.org, w.installation, w.team, w.useRole)
				granted <- err
			}()
			requireARowLockWaiter(t, "the grant should wait on the row the uninstall holds")
			select {
			case err := <-granted:
				t.Fatalf("the grant finished while the uninstall held the row: %v", err)
			default:
			}
			return nil
		}))
		err := <-granted
		var se *business.StoreError
		require.True(t, errors.As(err, &se), "got %v", err)
		require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)

		// Nothing landed on the retained node: a reinstall of the same target
		// reuses it, and the team does not reach the reinstalled solution.
		agentRole := createRole(t, w.org, "agent again", "doc", "write")
		reinstalled, err := testService.InstallSolution(testCtx, w.admin, &business.InstallSolutionParams{
			OrgID: w.org, AgentIdentifier: "acme.example/exposed:1.0.1", TargetID: w.target,
			RootScopeLabel: "Exposed", RoleID: agentRole,
		})
		require.NoError(t, err)
		require.NotEqual(t, w.installation, reinstalled.GetId())
		require.Empty(t, entitledInstallations(t, w.org, w.inTeam))
	})

	// The two grant-first interleavings, each WRITING the team grant through the
	// real grant body, so the scope_grants trigger takes the organization's
	// revision row exactly as it does in production. The uninstall deletes the
	// agent's standing grant, whose trigger takes the same row; the installation
	// lock both take first is what keeps the two from deadlocking.
	grantFirst := func(t *testing.T, writeBeforeUninstallArrives bool) {
		w := newExposureWorld(t)
		uninstalled := make(chan error, 1)
		var written *gen.ScopeGrant
		require.NoError(t, testStore.WithOrgTx(testCtx, w.org, func(ctx context.Context) error {
			path, err := testStore.InstallationRootScopePath(ctx, w.org, w.installation)
			if err != nil {
				return err
			}
			write := func() error {
				written, err = testService.GrantScopeTxForTest(ctx, w.admin, &gen.GrantScopeRequest{
					OrgId: w.org, SubjectId: w.team, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
					ScopePath: path, RoleId: w.useRole,
				})
				return err
			}
			if writeBeforeUninstallArrives {
				if err := write(); err != nil {
					return err
				}
			}
			go func() { uninstalled <- testService.UninstallSolution(testCtx, w.admin, w.org, w.installation) }()
			requireARowLockWaiter(t, "the uninstall should wait on the installation row the grant holds")
			select {
			case err := <-uninstalled:
				t.Fatalf("the uninstall finished while the grant held the row: %v", err)
			default:
			}
			if !writeBeforeUninstallArrives {
				// The interleaving that deadlocked when uninstall touched grants
				// before the installation row: the uninstall is already waiting,
				// and the grant now writes.
				return write()
			}
			return nil
		}), "the grant transaction must commit, not be chosen as a deadlock victim")
		require.NoError(t, <-uninstalled, "the uninstall must complete after the grant, not deadlock")
		require.NotNil(t, written)
		require.Equal(t, w.team, written.GetSubjectId())
	}
	t.Run("the grant locks and writes first: the uninstall waits for it", func(t *testing.T) {
		grantFirst(t, true)
	})
	t.Run("the grant locks first, the uninstall arrives, then the grant writes", func(t *testing.T) {
		grantFirst(t, false)
	})
}

// A platform super administrator passes the organization-admin check for every
// organization (requireOrgAdmin, shared by every organization-admin method), so
// these doors admit one for an organization it is not a member of. Pinned so the
// inherited reach is a decision someone has to change on purpose. A lesser
// platform role is not admitted.
func TestInstallationExposureAdmitsPlatformSuperAdmin(t *testing.T) {
	w := newExposureWorld(t)
	server := &adapters.InstallationServer{}
	superAdmin, ownOrg := mustUserAndOrg(t, testCtx, "super@platform.test", "platform-super", "Platform Home")
	grantPlatformRole(t, superAdmin, "super_admin", superAdmin)
	support, supportOrg := mustUserAndOrg(t, testCtx, "support@platform.test", "platform-support", "Support Home")
	grantPlatformRole(t, support, "support", support)

	listed, err := server.ListOrganizationInstallations(storyCallerContext(testCtx, superAdmin, ownOrg),
		&gen.ListOrganizationInstallationsRequest{OrgId: w.org})
	require.NoError(t, err)
	require.Len(t, listed.GetInstallations(), 1)
	require.Equal(t, w.installation, listed.GetInstallations()[0].GetInstallation().GetId())
	_, err = server.GrantInstallationToTeam(storyCallerContext(testCtx, superAdmin, ownOrg), &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	})
	require.NoError(t, err)

	_, err = server.RevokeInstallationFromTeam(storyCallerContext(testCtx, superAdmin, ownOrg), &gen.RevokeInstallationFromTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	})
	require.NoError(t, err)

	supportCaller := storyCallerContext(testCtx, support, supportOrg)
	_, err = server.ListOrganizationInstallations(supportCaller, &gen.ListOrganizationInstallationsRequest{OrgId: w.org})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = server.GrantInstallationToTeam(supportCaller, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = server.RevokeInstallationFromTeam(supportCaller, &gen.RevokeInstallationFromTeamRequest{
		OrgId: w.org, InstallationId: w.installation, TeamId: w.team, RoleId: w.useRole,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
