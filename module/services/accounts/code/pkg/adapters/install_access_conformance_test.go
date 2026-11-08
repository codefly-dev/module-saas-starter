package adapters

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/stretchr/testify/require"
)

// testInstallAssignsAccess holds an administrator's reviewed install to the
// access it assigns: each declared role to a team, the conventional
// `<target>-<role>` one unless the request names another, created when absent
// and granted once the installation is active; the installer joins exactly
// the teams of the roles they took; and the same access is applied, once,
// when a pending attempt resumes.
func testInstallAssignsAccess(t *testing.T, h reviewedClosureHarness) {
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	solution := func(alias string, edges ...string) {
		note := wireScope(alias+".note", "read")
		s := conformanceSolution{alias: alias, generation: 1, installationRole: domain.NoInstallationRole, ceilings: []*authorityv1.WorkScopeV1{note},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}}
		for _, edge := range edges {
			s.edges = append(s.edges, modulecontract.EdgeDependency{Module: edge, Presence: "required"})
		}
		deliverSolution(t, h.ctx, h.conn, h.signer, s)
	}
	createTeam := func(name string, members ...string) string {
		created, err := h.svc.CreateTeam(h.ctx, conformanceAlex, &gen.CreateTeamRequest{OrgId: fixtureOrganization, Name: name})
		require.NoError(t, err)
		for _, member := range members {
			require.NoError(t, h.svc.AddTeamMember(h.ctx, conformanceAlex, &gen.AddTeamMemberRequest{TeamId: created.GetTeam().GetId(), UserId: member}))
		}
		return created.GetTeam().GetId()
	}
	grantsOf := func(inst domain.Installation) map[string][]string {
		grants, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, inst.ID)
		require.NoError(t, err)
		out := map[string][]string{}
		for _, grant := range grants {
			if grant.TeamID != "" {
				out[grant.Role] = append(out[grant.Role], grant.TeamID)
			}
		}
		return out
	}

	everyRole := []string{"installer", "viewer"}
	installed := func(target string, access business.InstallAccess) business.InstalledSolution {
		t.Helper()
		review, err := h.review(t, alex, target)
		require.NoError(t, err)
		inst, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, access)
		require.NoError(t, err)
		require.Equal(t, domain.Active, inst.Status)
		return inst
	}
	install := func(target string, access business.InstallAccess) domain.Installation {
		t.Helper()
		inst := installed(target, access)
		require.Empty(t, inst.AccessProblems, "every role's access is applied")
		return inst.Installation
	}
	// pendingAttempt is a first attempt at target that stops part-way, its
	// supporting module failing once: the installation is pending.
	pendingAttempt := func(target string, access business.InstallAccess) (*gen.ReviewedSolutionInstall, domain.Installation) {
		t.Helper()
		support := target + "-support"
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: support, installationRole: domain.NoInstallationRole})
		solution(target, support)
		review, err := h.review(t, alex, target)
		require.NoError(t, err)
		resolved, err := h.svc.ResolveInstallationPrincipal(h.ctx, fixtureOrganization, support)
		require.NoError(t, err)
		lost := errors.New("the store lost the connection")
		h.faults.failOnce(resolved.TargetID, lost)
		pending, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, access)
		require.ErrorContains(t, err, lost.Error())
		require.Equal(t, domain.Pending, pending.Status)
		return review, pending.Installation
	}

	t.Run("installing a solution needs no choices", func(t *testing.T) {
		const target = "conformance-access-default"
		solution(target)
		// What the dialog sends untouched: no team named, every role ticked.
		inst := install(target, business.InstallAccess{InstallerRoles: everyRole})
		installer, viewer := teamsNamed(t, h, target+"-installer"), teamsNamed(t, h, target+"-viewer")
		require.Len(t, installer, 1, "the installer role's conventional team is created")
		require.Len(t, viewer, 1, "the viewer role's conventional team is created")
		require.Equal(t, map[string][]string{"installer": installer, "viewer": viewer}, grantsOf(inst), "each team holds its role once setup is active")
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, installer[0]), "the installer joins the team of each role they took, and nobody else does")
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]))
	})

	t.Run("an existing team of the conventional name is reused", func(t *testing.T) {
		const target = "conformance-access-reused"
		solution(target)
		// Spelled in another case, with its own member.
		existing := createTeam("Conformance-Access-Reused-Viewer", conformanceMaya)
		inst := install(target, business.InstallAccess{InstallerRoles: everyRole})
		require.Equal(t, []string{existing}, teamsNamed(t, h, target+"-viewer"), "no second team of the name")
		require.Equal(t, []string{existing}, grantsOf(inst)["viewer"])
		require.ElementsMatch(t, []string{conformanceAlex, conformanceMaya}, teamMembers(t, h, existing), "its members stay; the installer joins it for the role they took")
	})

	t.Run("a team holding the conventional slug under another name is reused", func(t *testing.T) {
		const target = "conformance-access-slug"
		solution(target)
		// Teams are unique by path, and a root team's path is the slug of its
		// name: "Conformance Access Slug Viewer" holds the path a team named
		// conformance-access-slug-viewer would take.
		existing := createTeam("Conformance Access Slug Viewer", conformanceMaya)
		inst := install(target, business.InstallAccess{InstallerRoles: everyRole})
		require.Equal(t, []string{existing}, grantsOf(inst)["viewer"], "the team of the slug takes the role")
		require.Empty(t, teamsNamed(t, h, target+"-viewer"), "no second team is created beside it")
		require.ElementsMatch(t, []string{conformanceAlex, conformanceMaya}, teamMembers(t, h, existing))
	})

	t.Run("an explicit role map is honoured", func(t *testing.T) {
		const target = "conformance-access-chosen"
		solution(target)
		chosen := createTeam("conformance access chosen readers", conformanceMaya)
		inst := install(target, business.InstallAccess{RoleTeams: map[string]string{"viewer": chosen}})
		installer := teamsNamed(t, h, target+"-installer")
		require.Len(t, installer, 1, "a role the map does not name gets its conventional team")
		require.Equal(t, map[string][]string{"installer": installer, "viewer": {chosen}}, grantsOf(inst))
		require.Empty(t, teamsNamed(t, h, target+"-viewer"), "no conventional team is created for a role given another")
		require.Equal(t, []string{conformanceMaya}, teamMembers(t, h, chosen), "a named team keeps its members")
	})

	t.Run("unticking a role leaves the installer out of that team", func(t *testing.T) {
		const target = "conformance-access-unticked"
		solution(target)
		inst := install(target, business.InstallAccess{InstallerRoles: []string{"installer"}})
		installer, viewer := teamsNamed(t, h, target+"-installer"), teamsNamed(t, h, target+"-viewer")
		require.Equal(t, map[string][]string{"installer": installer, "viewer": viewer}, grantsOf(inst), "every role still goes to its team")
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, installer[0]))
		require.Empty(t, teamMembers(t, h, viewer[0]), "the installer did not take the viewer role")
	})

	t.Run("a refused install creates no team", func(t *testing.T) {
		const target = "conformance-access-refused"
		_, pending := pendingAttempt(target, business.InstallAccess{InstallerRoles: everyRole})
		require.Equal(t, domain.Pending, pending.Status)
		require.Empty(t, teamsNamed(t, h, target+"-installer"), "nothing is created before the installation is active")
		require.Empty(t, teamsNamed(t, h, target+"-viewer"))
	})

	t.Run("a resumed install applies the same assignment once", func(t *testing.T) {
		const target = "conformance-access-resumed"
		other := createTeam("conformance access resumed elsewhere")
		journal := func(event string) int {
			return h.journalled(t, event, slices.Values([]string{fixtureOrganization}))
		}
		joins := func() (count int) {
			require.NoError(t, h.store.WithControlPlane(h.ctx, func(ctx context.Context) error {
				return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM public.audit_events WHERE event_type = 'saas.team.member_added' AND org_id = $1::uuid`,
					fixtureOrganization).Scan(&count)
			}))
			return count
		}
		granted, joined := journal("saas.authority.role_granted"), joins()
		// The first attempt stops part-way: its access is checked and
		// recorded, and nothing is created, granted or joined before it is
		// active.
		review, pending := pendingAttempt(target, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		require.Equal(t, granted, journal("saas.authority.role_granted"), "nothing is granted while setup is pending")
		require.Empty(t, teamsNamed(t, h, target+"-viewer"), "no team is created while setup is pending")
		plan, err := h.store.SolutionInstallationPlan(h.ctx, fixtureOrganization, pending.ID)
		require.NoError(t, err)
		require.Equal(t, &business.InstallationAccess{Conventional: map[string]string{"installer": target + "-installer", "viewer": target + "-viewer"},
			Installer: conformanceAlex, InstallerRoles: []string{"viewer"}}, plan.Access, "the access is recorded with the installation")

		// The replay resumes the attempt and applies what it recorded, not
		// what the replay names; asked again, it applies nothing.
		active, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{RoleTeams: map[string]string{"viewer": other}, InstallerRoles: everyRole})
		require.NoError(t, err)
		require.Equal(t, domain.Active, active.Status)
		require.Equal(t, pending.ID, active.ID)
		require.Empty(t, active.AccessProblems)
		installer, viewer := teamsNamed(t, h, target+"-installer"), teamsNamed(t, h, target+"-viewer")
		require.Len(t, installer, 1)
		require.Len(t, viewer, 1)
		require.Equal(t, map[string][]string{"installer": installer, "viewer": viewer}, grantsOf(active.Installation))
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]))
		require.Empty(t, teamMembers(t, h, installer[0]), "the replay's ticks play no part")
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err)
		_, err = h.svc.RetryInstallation(h.ctx, conformanceAlex, fixtureOrganization, active.ID)
		require.NoError(t, err)
		require.Equal(t, granted+2, journal("saas.authority.role_granted"), "each role is granted once across the attempt and its retries")
		require.Equal(t, joined+1, joins(), "the installer joins once")
		require.Len(t, teamsNamed(t, h, target+"-installer"), 1, "a resumed install creates no team twice")
	})

	t.Run("a recorded team deleted before activation falls back to the conventional team", func(t *testing.T) {
		const target = "conformance-access-deleted"
		chosen := createTeam("conformance access deleted readers")
		review, _ := pendingAttempt(target, business.InstallAccess{RoleTeams: map[string]string{"viewer": chosen}, InstallerRoles: everyRole})
		require.NoError(t, h.svc.DeleteTeam(h.ctx, conformanceAlex, &gen.DeleteTeamRequest{TeamId: chosen}))
		active, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err, "the installation is active; its access problems are an answer")
		require.Equal(t, domain.Active, active.Status)
		viewer := teamsNamed(t, h, target+"-viewer")
		require.Len(t, viewer, 1, "the viewer role's conventional team takes it")
		require.Equal(t, viewer, grantsOf(active.Installation)["viewer"])
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]))
		require.Len(t, active.AccessProblems, 1)
		require.Equal(t, "viewer", active.AccessProblems[0].Role)
		require.Contains(t, active.AccessProblems[0].Problem, "no longer exists; granted to "+target+"-viewer instead")
	})

	t.Run("an access step failing after activation reports each role and applies the rest", func(t *testing.T) {
		const target = "conformance-access-failing"
		solution(target)
		h.faults.failNextMember(errors.New("the membership store refused"))
		inst := installed(target, business.InstallAccess{InstallerRoles: everyRole})
		installer, viewer := teamsNamed(t, h, target+"-installer"), teamsNamed(t, h, target+"-viewer")
		require.Equal(t, map[string][]string{"installer": installer, "viewer": viewer}, grantsOf(inst.Installation), "every role is granted before anyone joins")
		require.Len(t, inst.AccessProblems, 1, "the one membership refused is reported")
		require.Equal(t, "installer", inst.AccessProblems[0].Role)
		require.Contains(t, inst.AccessProblems[0].Problem, "the membership store refused")
		require.Empty(t, teamMembers(t, h, installer[0]))
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]), "the next role's membership is applied all the same")
	})

	t.Run("an undeclared role is refused, ticked or mapped", func(t *testing.T) {
		const target = "conformance-access-undeclared"
		solution(target)
		review, err := h.review(t, alex, target)
		require.NoError(t, err)
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{InstallerRoles: []string{"editor"}})
		require.ErrorContains(t, err, `declares no role "editor"`)
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{RoleTeams: map[string]string{"editor": createTeam("conformance access undeclared")}})
		require.ErrorContains(t, err, `declares no role "editor"`)
		_, held := h.coreInstallation(t, alex, target)
		require.False(t, held, "a refused assignment records no installation")
	})

	t.Run("uninstalling a solution removes its grants, keeps the teams", func(t *testing.T) {
		const target = "conformance-access-uninstalled"
		solution(target)
		inst := install(target, business.InstallAccess{InstallerRoles: everyRole})
		require.Len(t, grantsOf(inst), 2)
		require.NoError(t, h.svc.UninstallSolution(h.ctx, conformanceAlex, fixtureOrganization, inst.ID))
		var kept []domain.RoleGrant
		require.NoError(t, h.store.AuthorityCore().Read(h.ctx, func(tx authority.Tx) error {
			var err error
			kept, err = tx.RoleGrants(inst.ID)
			return err
		}))
		require.Empty(t, kept, "the installation's grants are gone")
		for _, role := range everyRole {
			teams := teamsNamed(t, h, target+"-"+role)
			require.Len(t, teams, 1, "the teams are the organization's and remain")
			require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, teams[0]), "with their members")
		}
	})

	t.Run("an install refuses a team that is not the organization's", func(t *testing.T) {
		const target = "conformance-access-foreign"
		solution(target)
		review, err := h.review(t, alex, target)
		require.NoError(t, err)
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{RoleTeams: map[string]string{"viewer": business.NewIDString()}})
		require.ErrorContains(t, err, "is not a team of this organization")
		_, held := h.coreInstallation(t, alex, target)
		require.False(t, held, "a refused assignment records no installation")
		require.Empty(t, teamsNamed(t, h, target+"-installer"), "a refused assignment creates no team")
	})
}

// teamsNamed are the ids of the harness organization's teams named name,
// whatever its case.
func teamsNamed(t *testing.T, h reviewedClosureHarness, name string) []string {
	t.Helper()
	var ids []string
	for _, team := range listTeams(t, h, "") {
		if strings.EqualFold(team.GetName(), name) {
			ids = append(ids, team.GetId())
		}
	}
	return ids
}

// teamMembers are the people of the harness organization in team, sorted.
func teamMembers(t *testing.T, h reviewedClosureHarness, team string) []string {
	t.Helper()
	var members []string
	for _, person := range []string{conformanceAlex, conformanceMaya, conformanceIvy, conformanceZoe} {
		if slices.ContainsFunc(listTeams(t, h, person), func(t *gen.Team) bool { return t.GetId() == team }) {
			members = append(members, person)
		}
	}
	slices.Sort(members)
	return members
}

func listTeams(t *testing.T, h reviewedClosureHarness, member string) []*gen.Team {
	t.Helper()
	var teams []*gen.Team
	require.NoError(t, h.svc.Store().WithControlPlane(h.ctx, func(ctx context.Context) error {
		var err error
		teams, err = h.svc.Store().ListTeams(ctx, fixtureOrganization, member)
		return err
	}))
	return teams
}
