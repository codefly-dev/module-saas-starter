package adapters

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// conformanceAlice is another of Acme's administrators, who installs nothing.
const conformanceAlice = "00000000-0000-7000-8000-0000000000a2"

// testModulePagesAndReach holds the host to access by grant alone (an
// administrator holds nothing they were not granted), to module pages that
// are installed by composition and set up in one act, never installed by an
// administrator, and to an update offered when what a solution's roles reach
// widens in a module it reaches.
func testModulePagesAndReach(t *testing.T, h reviewedClosureHarness) {
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	entitled := func(person string) []string {
		t.Helper()
		out, err := h.svc.ListSolutionEntitlements(h.ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: person})
		require.NoError(t, err)
		var ids []string
		for _, e := range out.GetEntitlements() {
			ids = append(ids, e.GetInstallationId())
		}
		return ids
	}
	available := func(alias string) *business.AvailableSolutionTarget {
		t.Helper()
		all, _, err := h.svc.ListAvailableSolutions(h.ctx, fixtureOrganization, "", 500)
		require.NoError(t, err)
		i := slices.IndexFunc(all, func(a *business.AvailableSolutionTarget) bool { return a.RouteAlias == alias })
		require.GreaterOrEqual(t, i, 0, "%s is available", alias)
		return all[i]
	}
	install := func(target string, access business.InstallAccess) business.InstalledSolution {
		t.Helper()
		review, err := h.review(t, alex, target)
		require.NoError(t, err)
		inst, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, access)
		require.NoError(t, err)
		require.Equal(t, domain.Active, inst.Status)
		require.Empty(t, inst.AccessProblems)
		return inst
	}
	solution := func(alias string, bindings ...solutionhost.AuthorityBinding) {
		note := wireScope(alias+".note", "read")
		roles := map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}
		if len(bindings) > 0 {
			roles["viewer"] = append(roles["viewer"], wireScope("conformance-reach-host-store.item", "read", "write"))
		}
		deliverSolution(t, h.ctx, h.conn, h.signer, conformanceSolution{alias: alias, generation: 1, installationRole: domain.NoInstallationRole,
			ceilings: []*authorityv1.WorkScopeV1{note}, roles: roles, bindings: bindings})
	}

	t.Run("the installer can use the solution at once; another administrator cannot", func(t *testing.T) {
		const target = "conformance-access-own"
		solution(target)
		inst := install(target, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		require.Contains(t, entitled(conformanceAlex), inst.ID, "the installer took the viewer role and uses the solution")
		require.NotContains(t, entitled(conformanceAlice), inst.ID, "an administrator who took no role holds none")
	})

	t.Run("a member added to a role team can use it", func(t *testing.T) {
		const target = "conformance-access-member"
		solution(target)
		inst := install(target, business.InstallAccess{})
		require.NotContains(t, entitled(conformanceMaya), inst.ID)
		viewers := teamsNamed(t, h, target+"-viewer")
		require.Len(t, viewers, 1)
		require.NoError(t, h.svc.AddTeamMember(h.ctx, conformanceAlex, &gen.AddTeamMemberRequest{TeamId: viewers[0], UserId: conformanceMaya}))
		require.Contains(t, entitled(conformanceMaya), inst.ID, "the team's role is its new member's")
	})

	// A module page: a module declaring a page and roles, holding no
	// standing authority, installed by composition as every module is.
	const page = "conformance-set-up-page"
	entry := wireScope(page+".entry", "read")
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: domain.NoInstallationRole,
		page:  &solutionhost.Frontend{Title: "Set-up page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
		roles: map[string][]*authorityv1.WorkScopeV1{"installer": {entry}, "viewer": {entry}}})
	require.NoError(t, consentModule(h.ctx, h.svc, h.store, fixtureOrganization, conformanceAlex, page))
	composed := func() domain.Installation {
		t.Helper()
		all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == page && inst.Status == domain.Active })
		require.GreaterOrEqual(t, i, 0, "the module page is installed by composition")
		return all[i]
	}

	t.Run("a composed module page has no access until set up", func(t *testing.T) {
		inst := composed()
		require.Equal(t, authority.StablePrincipalID(fixtureOrganization, page), inst.PrincipalID, "its installation is composition's, owning its page's data (RBAC-1)")
		require.Empty(t, inst.InstalledBy, "holding no standing authority")
		grants, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, inst.ID)
		require.NoError(t, err)
		require.Empty(t, grants, "nobody holds a role on it")
		for _, person := range []string{conformanceAlex, conformanceAlice, conformanceMaya} {
			require.NotContains(t, entitled(person), inst.ID, "navigation lists it for nobody, administrators included")
		}
		require.Equal(t, []string{"installer", "viewer"}, available(page).Roles, "Admin lists it with its roles, to set up")
	})

	t.Run("setting up a module page is one act", func(t *testing.T) {
		inst := composed()
		set, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(page).TargetID, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		require.NoError(t, err)
		require.Empty(t, set.AccessProblems)
		require.Equal(t, inst.ID, set.ID, "set up on the composition installation; nothing is installed")
		installer, viewer := teamsNamed(t, h, page+"-installer"), teamsNamed(t, h, page+"-viewer")
		require.Len(t, installer, 1)
		require.Len(t, viewer, 1)
		grants, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, inst.ID)
		require.NoError(t, err)
		require.ElementsMatch(t, []domain.RoleGrant{{InstallationID: inst.ID, Role: "installer", TeamID: installer[0]}, {InstallationID: inst.ID, Role: "viewer", TeamID: viewer[0]}}, grants)
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]), "the administrator joins the team of the role they ticked")
		require.Empty(t, teamMembers(t, h, installer[0]), "and of no other")
		require.Contains(t, entitled(conformanceAlex), inst.ID, "navigation lists it for them")
		require.NotContains(t, entitled(conformanceAlice), inst.ID)
		again, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(page).TargetID, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		require.NoError(t, err)
		require.Empty(t, again.AccessProblems)
		require.Len(t, teamsNamed(t, h, page+"-viewer"), 1, "setting it up again creates nothing")
	})

	// A session cast onto a team (OpenTeamCast) is listed what the team holds
	// and nothing of its person's own: the listing is read under the session's
	// view, as every decision for that session is. Red before the session
	// reached the listing: a cast was answered as its person, so its dashboard
	// listed every page the administrator held and none of the team's.
	t.Run("a session cast onto a team lists the team's pages, not its person's", func(t *testing.T) {
		inst := composed()
		installer := teamsNamed(t, h, page+"-installer")
		require.Len(t, installer, 1)
		require.Empty(t, teamMembers(t, h, installer[0]), "the administrator is not in the installer team")
		own := entitled(conformanceAlex)
		require.Contains(t, own, inst.ID, "as themselves, through the viewer team they joined")
		require.Greater(t, len(own), 1, "and more of their own, which a cast must not list")
		parent, cast := uuid.NewString(), uuid.NewString()
		_, err := h.core.OpenTeamCast(h.ctx, authority.Person{ID: conformanceAlex, Organization: fixtureOrganization, SessionID: parent},
			authority.TeamCastRequest{Organization: fixtureOrganization, Team: installer[0], SessionID: cast})
		require.NoError(t, err, "an administrator casts onto a team they could grant")
		out, err := h.svc.ListSolutionEntitlements(h.ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: conformanceAlex, SessionId: cast})
		require.NoError(t, err)
		var listed []string
		for _, e := range out.GetEntitlements() {
			listed = append(listed, e.GetInstallationId())
		}
		require.Equal(t, []string{inst.ID}, listed, "the page the installer team holds, and nothing the person holds as themselves")
		asked, err := h.svc.ListSolutionEntitlements(h.ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: conformanceAlex, SessionId: parent})
		require.NoError(t, err)
		require.Len(t, asked.GetEntitlements(), len(own), "the session the cast was opened from lists the person's own, as before")
		_, err = h.svc.ListSolutionEntitlements(h.ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: conformanceAlice, SessionId: cast})
		require.Error(t, err, "another person asking under the cast is refused, never answered as the team")
	})

	t.Run("Admin → Solutions lists solutions; module pages appear only as set-up entries", func(t *testing.T) {
		offers, err := h.core.Offers(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		for _, m := range offers {
			require.Equal(t, domain.KindSolution, m.Kind, "%s is offered for install", m.Name)
		}
		target := available(page)
		require.Equal(t, "module", target.Kind)
		// A module page's installation, held by composition at its approved
		// reach, is reviewed for the upgrade it offers, as a solution's is;
		// the offer it already holds installs nothing again.
		require.NoError(t, h.core.AdoptLegacyReach(h.ctx))
		review, err := h.svc.ReviewSolutionInstall(h.ctx, alex, fixtureOrganization, target.TargetID)
		require.NoError(t, err, "a module page's installation is reviewed for its upgrade")
		require.Equal(t, page, review.Target)
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		code, _ := domain.CodeOf(err)
		require.Equal(t, domain.Denied, code, "%v", err)
		require.ErrorContains(t, err, "already installed")
		// An installed solution whose roles are all held is set up already:
		// Set up returns its installation and changes nothing.
		const solutionAlias = "conformance-access-own"
		all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		own := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == solutionAlias && inst.Status == domain.Active })
		require.GreaterOrEqual(t, own, 0)
		before, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, all[own].ID)
		require.NoError(t, err)
		require.NotEmpty(t, before)
		held, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(solutionAlias).TargetID, business.InstallAccess{})
		require.NoError(t, err)
		require.Empty(t, held.AccessProblems)
		after, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, held.ID)
		require.NoError(t, err)
		require.ElementsMatch(t, before, after)
	})

	// An installed solution a role of which nobody holds (an update approved
	// before an approval gave added roles their teams, or a holder removed)
	// is set up in one act, as a module page is: each such role gets its
	// conventional team, and a role someone holds is left as it is. Red
	// before: "is not a module page: a solution's access is assigned at its
	// install", so a hosted administrator had no way to give `user` a team
	// while a local fixture's install pass did.
	t.Run("an installed solution's role nobody holds is given its team by Set up", func(t *testing.T) {
		const target = "conformance-access-unheld"
		solution(target)
		created, err := h.svc.CreateTeam(h.ctx, conformanceAlex, &gen.CreateTeamRequest{OrgId: fixtureOrganization, Name: "conformance-unheld-readers"})
		require.NoError(t, err)
		readers := created.GetTeam().GetId()
		inst := install(target, business.InstallAccess{RoleTeams: map[string]string{"viewer": readers}})
		_, err = h.core.RevokeRole(h.ctx, alex, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: inst.ID, Role: "viewer", TeamID: readers})
		require.NoError(t, err)
		holders := func() []domain.RoleGrant {
			t.Helper()
			grants, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, inst.ID)
			require.NoError(t, err)
			return grants
		}
		installer := teamsNamed(t, h, target+"-installer")
		require.Len(t, installer, 1)
		require.Equal(t, []domain.RoleGrant{{InstallationID: inst.ID, Role: "installer", TeamID: installer[0]}}, holders(), "nobody holds viewer")
		require.Empty(t, teamsNamed(t, h, target+"-viewer"))

		set, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(target).TargetID, business.InstallAccess{InstallerRoles: []string{"installer", "viewer"}})
		require.NoError(t, err)
		require.Equal(t, inst.ID, set.ID, "set up on the solution's installation; nothing is installed")
		require.Empty(t, set.AccessProblems)
		viewer := teamsNamed(t, h, target+"-viewer")
		require.Len(t, viewer, 1, "the role nobody held gets its conventional team")
		require.ElementsMatch(t, []domain.RoleGrant{{InstallationID: inst.ID, Role: "installer", TeamID: installer[0]}, {InstallationID: inst.ID, Role: "viewer", TeamID: viewer[0]}}, holders())
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]), "the administrator takes the role they ticked")
		require.Empty(t, teamMembers(t, h, installer[0]), "and joins no team of a role someone held already")

		again, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(target).TargetID, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		require.NoError(t, err)
		require.Empty(t, again.AccessProblems)
		require.Len(t, teamsNamed(t, h, target+"-viewer"), 1, "setting it up again creates nothing")
		require.Len(t, holders(), 2)
	})

	t.Run("a widening anywhere in the reach shows update available", func(t *testing.T) {
		const store, target = "conformance-reach-host-store", "conformance-reach-host"
		item := "conformance-reach-host-store.item"
		deliverStore := func(generation uint64, purge string) {
			deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: store, generation: generation, installationRole: domain.NoInstallationRole,
				ceilings: []*authorityv1.WorkScopeV1{wireScope(item, "admin", "read", "write")},
				methods: []*compositionv1.MethodPolicyV1{
					{Method: "/conformance.reach.Store/Get", Requires: []*authorityv1.WorkScopeV1{wireScope(item, "read")}},
					{Method: "/conformance.reach.Store/Purge", Requires: []*authorityv1.WorkScopeV1{wireScope(item, purge)}},
				}})
			require.NoError(t, consentModule(h.ctx, h.svc, h.store, fixtureOrganization, conformanceAlex, store))
		}
		deliverStore(1, "admin")
		solution(target, solutionhost.AuthorityBinding{ID: "store", Audience: store, Operations: map[string][]string{"invoke": {item + ":read", item + ":write"}}})
		inst := install(target, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		upgrade := func() bool {
			t.Helper()
			read, _, err := h.svc.GetInstallation(h.ctx, fixtureOrganization, inst.ID)
			require.NoError(t, err)
			return read.GetUpgradeAvailable()
		}
		require.False(t, upgrade(), "a fresh installation offers no update")
		read, _, err := h.svc.GetInstallation(h.ctx, fixtureOrganization, inst.ID)
		require.NoError(t, err)
		require.NotNil(t, read.GetCreatedAt(), "the listing says when it was installed")
		require.WithinDuration(t, time.Now(), read.GetCreatedAt().AsTime(), 10*time.Minute)
		// The store, not the solution, changes: Purge now needs write, which
		// the viewer reaches through the solution's binding.
		deliverStore(2, "write")
		require.True(t, upgrade(), "the solution's reach widened, so an update is offered on it")
		offer, err := h.core.ReviewOffer(h.ctx, alex, fixtureOrganization, target)
		require.NoError(t, err)
		require.Contains(t, reachOperations(offer, "viewer", store), "/conformance.reach.Store/Purge", "the administrator reviews the wider reach")
		require.NotContains(t, approvedOperations(t, h, inst.Installation, "viewer", store), "/conformance.reach.Store/Purge", "holders keep the approved reach")
		// The update's review carries the approved reach beside it, so the
		// administrator is told what the update asks beyond it.
		review, err := h.svc.ReviewSolutionInstall(h.ctx, alex, fixtureOrganization, available(target).TargetID)
		require.NoError(t, err)
		var approvedAtStore []string
		for _, role := range review.GetApprovedRoles() {
			for _, row := range role.GetReach() {
				if role.GetName() == "viewer" && row.GetModule() == store {
					approvedAtStore = append(approvedAtStore, row.GetOperations()...)
				}
			}
		}
		require.Contains(t, approvedAtStore, "/conformance.reach.Store/Get", "the review names what the viewer was approved to reach")
		require.NotContains(t, approvedAtStore, "/conformance.reach.Store/Purge", "and not what the update asks beyond it")
	})

	// A module page whose upgrade an administrator approved is still
	// composition's installation: the core records the administrator's
	// operation as its receipt and leaves its installer as it was (none). Red
	// before Set up told composition's installation by its installer alone:
	// it asked for composition's receipt too, so after the first approved
	// upgrade the page was answered "not installed in this organization yet".
	t.Run("a module page set up after its approved upgrade is set up on the same installation", func(t *testing.T) {
		const store, upgraded = "conformance-upgraded-page-store", "conformance-upgraded-page"
		item, entry := store+".item", wireScope(upgraded+".entry", "read")
		deliverStore := func(generation uint64, purge string) {
			deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: store, generation: generation, installationRole: domain.NoInstallationRole,
				ceilings: []*authorityv1.WorkScopeV1{wireScope(item, "admin", "read", "write")},
				methods: []*compositionv1.MethodPolicyV1{
					{Method: "/conformance.upgraded.Store/Get", Requires: []*authorityv1.WorkScopeV1{wireScope(item, "read")}},
					{Method: "/conformance.upgraded.Store/Purge", Requires: []*authorityv1.WorkScopeV1{wireScope(item, purge)}},
				}})
			require.NoError(t, consentModule(h.ctx, h.svc, h.store, fixtureOrganization, conformanceAlex, store))
		}
		deliverStore(1, "admin")
		reach := []*authorityv1.WorkScopeV1{entry, wireScope(item, "read", "write")}
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: upgraded, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: domain.NoInstallationRole,
			page:     &solutionhost.Frontend{Title: "Upgraded page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
			bindings: []solutionhost.AuthorityBinding{{ID: "store", Audience: store, Operations: map[string][]string{"invoke": {item + ":read", item + ":write"}}}},
			roles:    map[string][]*authorityv1.WorkScopeV1{"installer": reach, "viewer": reach}})
		require.NoError(t, consentModule(h.ctx, h.svc, h.store, fixtureOrganization, conformanceAlex, upgraded))
		require.NoError(t, h.core.AdoptLegacyReach(h.ctx), "composition holds the page at its reach")
		installation := func() domain.Installation {
			t.Helper()
			all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
			require.NoError(t, err)
			i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == upgraded && inst.Status == domain.Active })
			require.GreaterOrEqual(t, i, 0, "the module page is installed by composition")
			return all[i]
		}
		held := installation()
		require.Equal(t, domain.ComposedConsent, held.Consent.OperationID, "held by composition")
		// The store widens Purge to write, which the page's roles reach
		// through its binding: an update is offered, and approved.
		deliverStore(2, "write")
		upgrade, err := h.core.UpgradeAvailable(h.ctx, installation())
		require.NoError(t, err)
		require.True(t, upgrade, "the page's reach widened, so an update is offered on it")
		review, err := h.svc.ReviewSolutionInstall(h.ctx, alex, fixtureOrganization, available(upgraded).TargetID)
		require.NoError(t, err)
		approved, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err, "an administrator approves the page's update")
		require.Equal(t, held.ID, approved.ID, "on composition's installation")
		require.Empty(t, approved.InstalledBy, "whose installer stays none")
		require.Equal(t, review.GetApproval().GetOperationId(), approved.Consent.OperationID, "its receipt now the administrator's")
		set, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(upgraded).TargetID, business.InstallAccess{InstallerRoles: []string{"viewer"}})
		require.NoError(t, err, "the upgraded page is set up")
		require.Empty(t, set.AccessProblems)
		require.Equal(t, held.ID, set.ID, "on the installation composition made")
		viewer := teamsNamed(t, h, upgraded+"-viewer")
		require.Len(t, viewer, 1)
		require.Equal(t, []string{conformanceAlex}, teamMembers(t, h, viewer[0]))
		require.Contains(t, entitled(conformanceAlex), held.ID, "navigation lists it for them")

		// The same review sent again, as a retry whose answer was lost, is
		// answered the installation, as the core replays it.
		again, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err, "an exact resend of the approved update replays it")
		require.Equal(t, approved.ID, again.ID)
		require.Equal(t, approved.Revision, again.Revision, "and records nothing again")
	})

	// Every role a page declares has its team, a role its update adds
	// included; and the holder of the page's installer role approves that
	// update, as the core admits them (its approver), while a member holding
	// another role, and an administrator of nothing, are refused. Red before:
	// the host gated the review and the approval on an organization
	// administrator, and an approved update assigned no team, so an added
	// role read "nobody".
	t.Run("a page's update adds a role with its team, approved by the page's installer", func(t *testing.T) {
		const page = "conformance-role-added-page"
		entry := wireScope(page+".entry", "read")
		roles := map[string][]*authorityv1.WorkScopeV1{"installer": {entry}, "viewer": {entry}}
		deliverPage := func(generation uint64) {
			deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, generation: generation, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: domain.NoInstallationRole,
				page: &solutionhost.Frontend{Title: "Role added page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload}, roles: roles})
		}
		deliverPage(1)
		require.NoError(t, consentModule(h.ctx, h.svc, h.store, fixtureOrganization, conformanceAlex, page))
		require.NoError(t, h.core.AdoptLegacyReach(h.ctx), "composition holds the page at what it approved")
		set, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(page).TargetID, business.InstallAccess{})
		require.NoError(t, err)
		require.Empty(t, set.AccessProblems)
		installer := teamsNamed(t, h, page+"-installer")
		require.Len(t, installer, 1)
		require.NoError(t, h.svc.AddTeamMember(h.ctx, conformanceAlex, &gen.AddTeamMemberRequest{TeamId: installer[0], UserId: conformanceMaya}))
		viewer := teamsNamed(t, h, page+"-viewer")
		require.Len(t, viewer, 1)
		require.NoError(t, h.svc.AddTeamMember(h.ctx, conformanceAlex, &gen.AddTeamMemberRequest{TeamId: viewer[0], UserId: conformanceIvy}))
		grantedRoles := func() []string {
			t.Helper()
			grants, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, set.ID)
			require.NoError(t, err)
			var out []string
			for _, grant := range grants {
				out = append(out, grant.Role)
			}
			slices.Sort(out)
			return out
		}
		require.Equal(t, []string{"installer", "viewer"}, grantedRoles())

		// The update adds `user`. Set up while it waits sets up what was
		// approved and asks the core for no role it would refuse.
		roles["user"] = []*authorityv1.WorkScopeV1{entry}
		deliverPage(2)
		pending, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(page).TargetID, business.InstallAccess{})
		require.NoError(t, err)
		require.Empty(t, pending.AccessProblems, "a role nobody approved yet is not set up")
		require.Empty(t, teamsNamed(t, h, page+"-user"))

		maya := authority.Person{ID: conformanceMaya, Organization: fixtureOrganization}
		ivy := authority.Person{ID: conformanceIvy, Organization: fixtureOrganization}
		_, err = h.svc.ReviewSolutionInstall(h.ctx, ivy, fixtureOrganization, available(page).TargetID)
		require.Error(t, err, "a viewer of the page does not review its update")
		// Nor can a member who approves nothing tell a delivered target id
		// from a missing one: both are refused alike, naming the id asked
		// for. The page's installer approves its page alone, so a missing id
		// is refused to her alike too; an administrator is told it is missing.
		missing := uuid.NewString()
		for _, member := range []authority.Person{ivy, maya} {
			_, unknown := h.svc.ReviewSolutionInstall(h.ctx, member, fixtureOrganization, missing)
			code, ok := domain.CodeOf(unknown)
			require.True(t, ok && code == domain.Denied, "%v", unknown)
			require.Equal(t, strings.ReplaceAll(err.Error(), available(page).TargetID, missing), unknown.Error())
		}
		_, unknown := h.svc.ReviewSolutionInstall(h.ctx, alex, fixtureOrganization, missing)
		require.ErrorContains(t, unknown, "not available")
		review, err := h.svc.ReviewSolutionInstall(h.ctx, maya, fixtureOrganization, available(page).TargetID)
		require.NoError(t, err, "the page's installer reviews its update")
		_, err = h.svc.InstallReviewedSolution(h.ctx, ivy, fixtureOrganization, review, business.InstallAccess{})
		require.Error(t, err, "nor approves another's review")
		approved, err := h.svc.InstallReviewedSolution(h.ctx, maya, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err, "the page's installer approves its update")
		require.Equal(t, set.ID, approved.ID)
		require.Equal(t, review.GetApproval().GetOperationId(), approved.Consent.OperationID)
		require.Len(t, approved.AccessProblems, 1, "the role the update adds waits for an administrator's team")
		require.Equal(t, "user", approved.AccessProblems[0].Role)
		require.Empty(t, teamsNamed(t, h, page+"-user"), "the installer creates no team of the organization")

		// An administrator's Set up gives it its team, now that it is approved.
		again, err := h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(page).TargetID, business.InstallAccess{})
		require.NoError(t, err)
		require.Empty(t, again.AccessProblems)
		require.Len(t, teamsNamed(t, h, page+"-user"), 1)
		require.Equal(t, []string{"installer", "user", "viewer"}, grantedRoles())

		// An administrator's approval of an update that adds a role gives it
		// its team at once.
		roles["author"] = []*authorityv1.WorkScopeV1{entry}
		deliverPage(3)
		review, err = h.svc.ReviewSolutionInstall(h.ctx, alex, fixtureOrganization, available(page).TargetID)
		require.NoError(t, err)
		// An update assigns no team it is asked for: the request is refused
		// by name, never dropped, and nothing of it is recorded.
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{RoleTeams: map[string]string{"author": viewer[0]}})
		require.ErrorContains(t, err, "role author to team "+viewer[0])
		_, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{InstallerRoles: []string{"author"}})
		require.ErrorContains(t, err, "the installer in role author")
		require.Empty(t, teamsNamed(t, h, page+"-author"))
		approved, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err)
		require.Empty(t, approved.AccessProblems)
		author := teamsNamed(t, h, page+"-author")
		require.Len(t, author, 1, "the added role's conventional team is created")
		require.Empty(t, teamMembers(t, h, author[0]), "with nobody in it: the update gives no person anything")
		require.Equal(t, []string{"author", "installer", "user", "viewer"}, grantedRoles())
	})
}

// reachOperations are the operations role reaches at module in offer.
func reachOperations(offer authority.ReviewedOffer, role, module string) []string {
	var out []string
	for _, row := range offer.Reach[role] {
		if row.Module == module {
			out = append(out, row.Operations...)
		}
	}
	return out
}

// approvedOperations are the operations role reaches at module in the reach
// inst's consent approved.
func approvedOperations(t *testing.T, h reviewedClosureHarness, inst domain.Installation, role, module string) []string {
	t.Helper()
	var read domain.Installation
	require.NoError(t, h.store.AuthorityCore().Read(h.ctx, func(tx authority.Tx) error {
		var err error
		read, err = tx.Installation(inst.ID)
		return err
	}))
	require.NotEmpty(t, read.Consent.Reach, "the consent records the reach it approved")
	rows, err := decide.DecodeReach(read.Consent.Reach)
	require.NoError(t, err)
	var out []string
	for _, row := range rows[role] {
		if row.Module == module {
			out = append(out, row.Operations...)
		}
	}
	return out
}

// testAPageDeliveredBeforeItsReachHoldsItOnceComposed holds the host's own
// delivery path to a module page delivered BEFORE the module it reaches
// through a binding, the order a fresh composition may take: the host's pass
// (KeepAuthorityCoreCurrent) installs the page by composition and holds it at
// the reach it has then, without the module; once the module is composed, the
// page's holders reach it through the binding at once, with no update offered
// for an administrator to approve, since composition growing approves nothing
// anyone declared. Red at authority 05e4e62f: the page's receipt froze the
// module's absence, so its viewer was refused "holds nothing at <store>
// through binding" and no update was offered either.
func testAPageDeliveredBeforeItsReachHoldsItOnceComposed(t *testing.T, h reviewedClosureHarness) {
	const page, store = "conformance-page-first", "conformance-page-first-store"
	entry, row := page+".entry", store+".row"
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, ceilings: []*authorityv1.WorkScopeV1{wireScope(entry, "read", "write")}, installationRole: domain.NoInstallationRole,
		page:     &solutionhost.Frontend{Title: "Page first", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
		bindings: []solutionhost.AuthorityBinding{{ID: "rows", Audience: store, Operations: map[string][]string{"invoke": {row + ":read", row + ":write"}}}},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope(entry, "read", "write"), wireScope(row, "read", "write")},
			"viewer":    {wireScope(entry, "read"), wireScope(row, "read")},
		}})
	pass := func() {
		t.Helper()
		// The host's pass. What the harness holds back elsewhere is reported
		// by it; nothing about this page or its store may be.
		if err := h.svc.KeepAuthorityCoreCurrent(h.ctx); err != nil {
			require.NotContains(t, err.Error(), page)
		}
	}
	pass()
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	installation := func() domain.Installation {
		t.Helper()
		all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == page && inst.Status == domain.Active })
		require.GreaterOrEqual(t, i, 0, "the module page is installed by composition")
		return all[i]
	}
	held := installation()
	require.Equal(t, domain.ComposedConsent, held.Consent.OperationID, "held by composition, at a reach without the store")
	_, err := h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: held.ID, Role: "viewer", PersonID: conformanceMaya})
	require.NoError(t, err)
	maya := authority.Person{ID: conformanceMaya, Organization: fixtureOrganization}
	through := func() (authority.Issued, error) {
		t.Helper()
		parent, err := h.core.IssueForPerson(h.ctx, maya, authority.IssueRequest{Audience: page})
		require.NoError(t, err, "the viewer is issued at the page")
		return h.core.IssueChild(h.ctx, parent.Token, authority.Workload{Module: page}, authority.IssueRequest{Binding: "rows"})
	}
	_, err = through()
	require.Error(t, err, "the store is not composed yet: the binding reaches nothing")

	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: store, ceilings: []*authorityv1.WorkScopeV1{wireScope(row, "read", "write")}})
	pass()
	upgrade, err := h.core.UpgradeAvailable(h.ctx, installation())
	require.NoError(t, err)
	require.False(t, upgrade, "the store composed for the first time widens nothing anyone declared: no update to approve")
	child, err := through()
	require.NoError(t, err, "the viewer reaches the store through the page's binding once it is composed")
	chain := child.Context.GetActorChain()
	require.NotEmpty(t, chain)
	granted := chain[len(chain)-1].GetGrantedScopes()
	require.Len(t, granted, 1)
	require.Equal(t, row, granted[0].GetResourceKind())
	require.Equal(t, []string{"read"}, granted[0].GetActions(), "the viewer's role, cut to the binding")
	require.Equal(t, store, child.Context.GetAudience())
}

// testModulePageRoleReachesThroughItsBinding holds the host to a module
// page's roles reaching through the page's own bindings as a solution's do:
// a person holding only the page's viewer role is issued, through the page's
// binding, a child narrowed to the role intersected with the binding, and
// nothing beyond it, without the role, or on their own session at the module
// the page reaches.
func testModulePageRoleReachesThroughItsBinding(t *testing.T, h reviewedClosureHarness) {
	const page, store = "conformance-reach-page", "conformance-reach-page-store"
	entry, row := page+".entry", store+".row"
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: store, ceilings: []*authorityv1.WorkScopeV1{wireScope(row, "read", "write")}})
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, ceilings: []*authorityv1.WorkScopeV1{wireScope(entry, "read", "write")}, installationRole: domain.NoInstallationRole,
		page:     &solutionhost.Frontend{Title: "Reaching page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
		bindings: []solutionhost.AuthorityBinding{{ID: "rows", Audience: store, Operations: map[string][]string{"invoke": {row + ":read", row + ":write"}}}},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope(entry, "read", "write"), wireScope(row, "read", "write")},
			"viewer":    {wireScope(entry, "read"), wireScope(row, "read")},
		}})
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
	require.NoError(t, err)
	i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == page && inst.Status == domain.Active })
	require.GreaterOrEqual(t, i, 0, "the module page is installed by composition")
	_, err = h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: all[i].ID, Role: "viewer", PersonID: conformanceMaya})
	require.NoError(t, err)

	maya := authority.Person{ID: conformanceMaya, Organization: fixtureOrganization}
	through := func(who authority.Person, scopes ...*basev0.WorkScopeV1) (authority.Issued, error) {
		t.Helper()
		parent, err := h.core.IssueForPerson(h.ctx, who, authority.IssueRequest{Audience: page})
		if err != nil {
			return parent, err
		}
		return h.core.IssueChild(h.ctx, parent.Token, authority.Workload{Module: page}, authority.IssueRequest{Binding: "rows", Scopes: scopes})
	}

	t.Run("a module page's viewer is issued, through its binding, the role cut to the binding", func(t *testing.T) {
		child, err := through(maya)
		require.NoError(t, err)
		chain := child.Context.GetActorChain()
		require.NotEmpty(t, chain)
		granted := chain[len(chain)-1].GetGrantedScopes()
		require.Len(t, granted, 1)
		require.Equal(t, row, granted[0].GetResourceKind())
		require.Equal(t, []string{"read"}, granted[0].GetActions())
		// The store checks owners: the page's role reaches it as the data the
		// page's composition installation owns, as a solution's role does
		// (RBAC-1), never every owner's.
		require.Equal(t, authority.StablePrincipalID(fixtureOrganization, page), all[i].PrincipalID, "a module's composition installation holds the principal the core gives it")
		require.Equal(t, all[i].PrincipalID, granted[0].GetResourceOwnerId(), "a page's role is its installation's data at a module checking owners")
		require.Equal(t, store, child.Context.GetAudience())
	})

	t.Run("and never another installation's data (RBAC-1)", func(t *testing.T) {
		_, err := through(maya, &basev0.WorkScopeV1{ResourceKind: row, Actions: []string{"read"}, ResourceOwnerId: authority.StablePrincipalID(globexOrganization, page)})
		require.ErrorContains(t, err, `widens what its owner holds through binding "rows"`)
		_, err = through(maya, &basev0.WorkScopeV1{ResourceKind: row, Actions: []string{"read"}})
		require.ErrorContains(t, err, `widens what its owner holds through binding "rows"`, "the whole kind, every owner's, is refused")
	})

	t.Run("and nothing beyond it", func(t *testing.T) {
		_, err := through(maya, &basev0.WorkScopeV1{ResourceKind: row, Actions: []string{"write"}})
		require.ErrorContains(t, err, `widens what its owner holds through binding "rows"`, "the binding carries write; the viewer role does not")
		_, err = through(authority.Person{ID: conformanceAlice, Organization: fixtureOrganization})
		require.ErrorContains(t, err, "holds no role on "+page, "an administrator holding no role is refused like any member")
		_, err = h.core.IssueForPerson(h.ctx, maya, authority.IssueRequest{Audience: store})
		require.ErrorContains(t, err, "holds nothing at this audience", "on her own session, her own grants at the store decide")
	})

	// The host's module edge names the page a call comes from, and the
	// authority interface issues through the page's target's binding.
	t.Run("a page's call on the module edge goes through its target's binding; an unbound module on the person's own access", func(t *testing.T) {
		const other = "conformance-reach-page-unbound"
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: other, ceilings: []*authorityv1.WorkScopeV1{wireScope(other+".row", "read")}})
		sess, err := sessionv1.NewSessionServiceClient(h.conn).SignIn(h.ctx, &sessionv1.SignInRequest{FixtureToken: "fixture-maya"})
		require.NoError(t, err)
		asMaya := metadata.AppendToOutgoingContext(h.ctx, "authorization", "Bearer "+sess.GetBearer())
		issueAs := func(actor, audience string, scopes ...*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
			req := &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: audience, PageTarget: page, Scopes: scopes}
			if actor != "" {
				req.Actor = &authorityv1.WorkloadCredential{Credential: actor}
			}
			return authorityv1.NewAuthorityServiceClient(h.conn).Issue(asMaya, req)
		}
		issue := func(audience string, scopes ...*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
			return issueAs(h.edgeCredential, audience, scopes...)
		}
		got, err := issue(store)
		require.NoError(t, err)
		chain := got.GetContext().GetActorChain()
		require.NotEmpty(t, chain, "issued as the page's target's hop")
		require.Equal(t, page, chain[len(chain)-1].GetPrincipalId())
		require.Equal(t, "page", chain[len(chain)-1].GetPrincipalKind(), "the person present on the page")
		granted := chain[len(chain)-1].GetGrantedScopes()
		require.Len(t, granted, 1)
		require.Equal(t, row, granted[0].GetResourceKind())
		require.Equal(t, []string{"read"}, granted[0].GetActions(), "the viewer's role cut to the binding")
		_, err = issue(store, wireScope(row, "write"))
		require.ErrorContains(t, err, `widens what its owner holds through binding "rows"`, "refused what the role lacks")
		_, err = issue(other)
		require.ErrorContains(t, err, "holds nothing at this audience", "a module the target declares no binding to is on the person's own access")
		// Only the module edge names a page: a solution backend holding her
		// bearer, or a module, cannot mint the page hop.
		for _, actor := range []string{"", h.moduleCredential} {
			_, err := issueAs(actor, store)
			require.ErrorContains(t, err, "module edge alone")
		}
	})
}

// testADeliveryTheCoreRefusesIsNeverApplied replays Blue, 2026-11-11, on the
// real path: accounts recorded a directory page's generation 8 as applied
// and current while the authority core refused it (its roles named a task
// kind and the document kinds through an import binding, which the
// composition did not reach) and stayed at 7; generation 9, identical to 8,
// then measured as a widening of 7 and ended three of the page's role
// grants. A delivery the core refuses is refused with the core's reason and
// recorded nowhere: accounts'
// applied generation and the core's composed one agree after every step,
// the refused generation delivered again at its own or a later generation is
// still refused, and no grant on the page ends. A later generation the core
// admits is applied and composed together.
func testADeliveryTheCoreRefusesIsNeverApplied(t *testing.T, h reviewedClosureHarness) {
	const page, store, legacy = "conformance-pair-page", "conformance-pair-store", "conformance-pair-legacy"
	entry, row, item := page+".entry", store+".row", legacy+".item"
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: store, ceilings: []*authorityv1.WorkScopeV1{wireScope(row, "read", "write")}})
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: legacy, ceilings: []*authorityv1.WorkScopeV1{wireScope(item, "read")}})
	// pageAt is the page at generation, binding the store's rows for its
	// roles; beyondReach adds a binding carrying the legacy item's read and
	// names its write through it for the viewer, which the composition does
	// not reach: refused by name (decide.Unreached).
	pageAt := func(generation uint64, beyondReach bool) conformanceModule {
		m := conformanceModule{alias: page, generation: generation, ceilings: []*authorityv1.WorkScopeV1{wireScope(entry, "read", "write")}, installationRole: domain.NoInstallationRole,
			page:     &solutionhost.Frontend{Title: "Pair page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
			bindings: []solutionhost.AuthorityBinding{{ID: "rows", Audience: store, Operations: map[string][]string{"invoke": {row + ":read", row + ":write"}}}},
			roles: map[string][]*authorityv1.WorkScopeV1{
				"installer": {wireScope(entry, "read", "write"), wireScope(row, "read", "write")},
				"viewer":    {wireScope(entry, "read"), wireScope(row, "read")},
			}}
		if beyondReach {
			m.bindings = append(m.bindings, solutionhost.AuthorityBinding{ID: "legacy", Audience: legacy, Operations: map[string][]string{"invoke": {item + ":read"}}})
			m.through = map[string]map[string][]*authorityv1.WorkScopeV1{"viewer": {"legacy": {wireScope(item, "write")}}}
		}
		return m
	}
	deliverModule(t, h.ctx, h.conn, h.signer, pageAt(7, false))
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	installation, ok := h.coreInstallation(t, alex, page)
	require.True(t, ok, "the module page is installed by composition")
	_, err := h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installation.ID, Role: "viewer", PersonID: conformanceMaya})
	require.NoError(t, err)
	_, err = h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installation.ID, Role: "installer", PersonID: conformanceAlex})
	require.NoError(t, err)
	grants, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, installation.ID)
	require.NoError(t, err)
	require.Len(t, grants, 2)
	installation, _ = h.coreInstallation(t, alex, page)

	// agree holds both records to generation, and the page's installation to
	// its grants and to revision: its own while the core refuses every
	// delivery, one above it once a delivery asking nothing new re-converges
	// the installation on the new generation, as a solution's does.
	agree := func(generation, revision uint64) {
		t.Helper()
		composed, err := h.core.Composed(h.ctx, page)
		require.NoError(t, err)
		require.Equal(t, generation, composed.Generation, "the core composes generation %d", generation)
		records, err := h.svc.ListSolutionHostBindings(h.ctx)
		require.NoError(t, err)
		i := slices.IndexFunc(records, func(r *business.SolutionHostBindingRecord) bool { return r.BindingID == page })
		require.GreaterOrEqual(t, i, 0)
		require.NotNil(t, records[i].Applied)
		require.Equal(t, generation, records[i].Applied.Generation, "accounts applied generation %d", generation)
		now, ok := h.coreInstallation(t, alex, page)
		require.True(t, ok)
		require.Equal(t, revision, now.Revision, "the installation's revision")
		kept, err := h.core.ListRoleGrants(h.ctx, alex, fixtureOrganization, installation.ID)
		require.NoError(t, err)
		require.ElementsMatch(t, grants, kept, "every grant is kept")
	}
	agree(7, installation.Revision)

	for _, generation := range []uint64{8, 8, 9} {
		err := tryDeliverModule(t, h.ctx, h.conn, h.signer, pageAt(generation, true))
		require.ErrorContains(t, err, `role "viewer" of `+page+` names `+item+`:write through binding "legacy", which reaches it nowhere in the composition`, "generation %d", generation)
		agree(7, installation.Revision)
	}

	deliverModule(t, h.ctx, h.conn, h.signer, pageAt(10, false))
	agree(10, installation.Revision+1)
	now, _ := h.coreInstallation(t, alex, page)
	require.Equal(t, uint64(10), now.Consent.Generation, "the installation re-converged on 10, held by composition")
	require.Equal(t, domain.ComposedConsent, now.Consent.OperationID)
}

// testPagesReachThroughTheAgent holds the host to what a solution's page
// reaches through an agent: the solution's roles name,
// through their binding to the agent, what the agent's own bindings reach
// (the model's shared profiles, the documents), delivered as the CLI
// delivers them and read back from this store, and a person present on the
// page is issued, at each of the agent's hops, their role cut to its binding,
// owned by the solution's installation — the model's profiles unselected,
// since the model declares them shared. An agent redelivered without what a
// solution names through it is accepted: the solution's reach is reported
// unreached and its hop there refused by name, and it re-converges by itself
// once the agent reaches it again.
func testPagesReachThroughTheAgent(t *testing.T, h reviewedClosureHarness) {
	const model, docs, agent, notes, studio = "reach-model", "reach-docs", "reach-agent", "reach-notes", "reach-studio"
	profiles, doc, intents, tasks := model+".profiles", docs+".doc", agent+".intents", agent+".tasks"
	invoke := func(scopes ...string) map[string][]string { return map[string][]string{"invoke": scopes} }
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: model, ceilings: []*authorityv1.WorkScopeV1{wireScope(profiles, "invoke", "read")},
		ownerUnaware: true, sharedKinds: []string{profiles}})
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: docs, ceilings: []*authorityv1.WorkScopeV1{wireScope(doc, "read", "write")}})
	agentModule := func(generation uint64, withModel bool) conformanceModule {
		bindings := []solutionhost.AuthorityBinding{
			{ID: "evidence", Audience: docs, Operations: invoke(doc + ":read")},
			{ID: "configuration-write", Audience: docs, Operations: invoke(doc + ":write")},
		}
		if withModel {
			bindings = append(bindings, solutionhost.AuthorityBinding{ID: "model", Audience: model, Operations: invoke(profiles+":invoke", profiles+":read")})
		}
		return conformanceModule{alias: agent, generation: generation,
			ceilings: []*authorityv1.WorkScopeV1{wireScope(intents, "read", "configure", "activate", "run"), wireScope(tasks, "execute")}, bindings: bindings}
	}
	deliverModule(t, h.ctx, h.conn, h.signer, agentModule(1, true))
	turn := []*authorityv1.WorkScopeV1{wireScope(profiles, "invoke", "read"), wireScope(doc, "read")}
	author := []*authorityv1.WorkScopeV1{wireScope(profiles, "invoke", "read"), wireScope(doc, "read", "write")}
	solution := func(alias string, binding []string, roles map[string][]*authorityv1.WorkScopeV1, through map[string][]*authorityv1.WorkScopeV1) {
		note := wireScope(alias+".note", "read")
		for name := range roles {
			roles[name] = append([]*authorityv1.WorkScopeV1{note}, roles[name]...)
		}
		roles["installer"] = []*authorityv1.WorkScopeV1{note}
		reach := map[string]map[string][]*authorityv1.WorkScopeV1{}
		for name, scopes := range through {
			reach[name] = map[string][]*authorityv1.WorkScopeV1{"agent": scopes}
		}
		deliverSolution(t, h.ctx, h.conn, h.signer, conformanceSolution{alias: alias, generation: 1, installationRole: domain.NoInstallationRole,
			ceilings: []*authorityv1.WorkScopeV1{note}, roles: roles, through: reach,
			bindings: []solutionhost.AuthorityBinding{{ID: "agent", Audience: agent, Operations: invoke(binding...)}}})
	}
	solution(notes, []string{intents + ":read", intents + ":run", tasks + ":execute"},
		map[string][]*authorityv1.WorkScopeV1{"viewer": {wireScope(intents, "read", "run"), wireScope(tasks, "execute")}},
		map[string][]*authorityv1.WorkScopeV1{"viewer": turn})
	solution(studio, []string{intents + ":read", intents + ":configure", intents + ":activate", intents + ":run", tasks + ":execute"},
		map[string][]*authorityv1.WorkScopeV1{
			"editor": {wireScope(intents, "read", "configure", "activate", "run"), wireScope(tasks, "execute")},
			"viewer": {wireScope(intents, "read", "run"), wireScope(tasks, "execute")}},
		map[string][]*authorityv1.WorkScopeV1{"editor": author, "viewer": turn})
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	installed := map[string]string{}
	for _, target := range []string{notes, studio} {
		review, err := h.review(t, alex, target)
		require.NoError(t, err)
		inst, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
		require.NoError(t, err)
		installed[target] = inst.ID
	}
	grant := func(target, role, person string) {
		_, err := h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installed[target], Role: role, PersonID: person})
		require.NoError(t, err)
	}
	grant(notes, "viewer", conformanceMaya)
	grant(studio, "editor", conformanceIvy)
	grant(studio, "viewer", conformanceAlice)
	person := func(id string) authority.Person { return authority.Person{ID: id, Organization: fixtureOrganization} }
	atAgent := func(who, page string) authority.Issued {
		out, err := h.core.IssueForPage(h.ctx, person(who), page, authority.IssueRequest{Audience: agent})
		require.NoError(t, err)
		return out
	}
	onward := func(parent authority.Issued, binding string) (authority.Issued, error) {
		return h.core.IssueChild(h.ctx, parent.Token, authority.Workload{Module: agent}, authority.IssueRequest{Binding: binding})
	}
	granted := func(t *testing.T, i authority.Issued) []*basev0.WorkScopeV1 {
		chain := i.Context.GetActorChain()
		require.NotEmpty(t, chain)
		return chain[len(chain)-1].GetGrantedScopes()
	}
	principal := func(target string) string {
		inst, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		i := slices.IndexFunc(inst, func(i domain.Installation) bool { return i.Target == target && i.Status == domain.Active })
		require.GreaterOrEqual(t, i, 0)
		return inst[i].PrincipalID
	}

	t.Run("a non-admin notes viewer's turn reaches the model and reads the evidence, and writes nothing", func(t *testing.T) {
		at := atAgent(conformanceMaya, notes)
		toModel, err := onward(at, "model")
		require.NoError(t, err, "the model's profiles are shared")
		for _, sc := range granted(t, toModel) {
			require.Empty(t, sc.GetResourceOwnerId(), "a shared kind is unselected")
		}
		evidence, err := onward(at, "evidence")
		require.NoError(t, err)
		require.Equal(t, principal(notes), granted(t, evidence)[0].GetResourceOwnerId(), "the evidence read is the notes's installation's")
		_, err = onward(at, "configuration-write")
		require.ErrorContains(t, err, "holds nothing at "+docs)
	})
	t.Run("a non-admin studio editor publishes, activates and runs", func(t *testing.T) {
		at := atAgent(conformanceIvy, studio)
		write, err := onward(at, "configuration-write")
		require.NoError(t, err)
		require.Equal(t, principal(studio), granted(t, write)[0].GetResourceOwnerId())
		_, err = onward(at, "model")
		require.NoError(t, err)
		all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		i := slices.IndexFunc(all, func(i domain.Installation) bool { return i.Target == agent && i.Status == domain.Active })
		require.GreaterOrEqual(t, i, 0)
		for _, action := range []string{decide.ArtifactActivate, decide.ArtifactRun} {
			_, err := h.core.DecideArtifact(h.ctx, at.Token, authority.Workload{Module: agent}, all[i].ID, intents, action,
				&decide.ArtifactIdentity{Schema: "s/v1", Source: fixtureOrganization + "." + studio, Subject: []byte("x")})
			require.NoError(t, err, action)
		}
	})
	t.Run("a studio viewer runs and neither writes a configuration nor activates", func(t *testing.T) {
		at := atAgent(conformanceAlice, studio)
		_, err := onward(at, "configuration-write")
		require.ErrorContains(t, err, "holds nothing at "+docs)
		_, err = onward(at, "model")
		require.NoError(t, err)
	})
	t.Run("an agent redelivered without the model binding leaves the notes's reach unreached; it re-converges", func(t *testing.T) {
		installation := func() domain.Installation {
			all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
			require.NoError(t, err)
			i := slices.IndexFunc(all, func(i domain.Installation) bool { return i.Target == notes && i.Status == domain.Active })
			require.GreaterOrEqual(t, i, 0)
			return all[i]
		}
		deliverModule(t, h.ctx, h.conn, h.signer, agentModule(2, false))
		gaps, err := h.core.Unreached(h.ctx, installation())
		require.NoError(t, err)
		require.Equal(t, []string{"role viewer reaches nothing of " + profiles + ":invoke, " + profiles + ":read through agent: no module agent reaches consumes it for a person"}, gaps)
		at := atAgent(conformanceMaya, notes)
		_, err = onward(at, "model")
		require.ErrorContains(t, err, `operation binding "model" is not one `+agent+` declares`)
		_, err = onward(at, "evidence")
		require.NoError(t, err, "what the agent still reaches is issued")

		deliverModule(t, h.ctx, h.conn, h.signer, agentModule(3, true))
		gaps, err = h.core.Unreached(h.ctx, installation())
		require.NoError(t, err)
		require.Empty(t, gaps)
		upgrade, err := h.core.UpgradeAvailable(h.ctx, installation())
		require.NoError(t, err)
		require.False(t, upgrade, "a declared reach reached again asks the administrator nothing new")
		_, err = onward(atAgent(conformanceMaya, notes), "model")
		require.NoError(t, err)
	})
}

// testAnEditorSavesAndIsOfferedTheRunnablesTheirRoleAllows holds the host,
// on its own store and delivery path, to a solution editor's first save and
// tool offer through an agent (W19-3, W19-4):
//
//   - the agent's configuration-write binding carries the collection create
//     its call declares (as the CLI derives it from the store's CreateDocument
//     carries), so an editor whose role names it through the agent is issued
//     it, and the store attaches the collection the first write creates to
//     the solution's installation; a viewer is issued no write at all;
//   - each runnable operation's prepared binding, the agent's own operations
//     among them (addressed to the agent itself), issues the operation's scopes
//     asked for the solution's installation (the owner the agent's admitted
//     capability is confined to) within the role cut to the binding: an
//     editor is offered the document, record and agent tools their role
//     names, a viewer only the document read; an owner-blind ask is a
//     widening.
func testAnEditorSavesAndIsOfferedTheRunnablesTheirRoleAllows(t *testing.T, h reviewedClosureHarness) {
	const docs, records, agent, studio = "tools-docs", "tools-records", "tools-agent", "tools-studio"
	doc, collection, row, intents, tasks := docs+".doc", docs+".collection", records+".row", agent+".intents", agent+".tasks"
	invoke := func(scopes ...string) map[string][]string { return map[string][]string{"invoke": scopes} }
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: docs, ceilings: []*authorityv1.WorkScopeV1{wireScope(doc, "read", "write"), wireScope(collection, "read", "create")}})
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: records, ceilings: []*authorityv1.WorkScopeV1{wireScope(row, "read")}})
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: agent,
		ceilings: []*authorityv1.WorkScopeV1{wireScope(intents, "read", "configure", "activate", "run"), wireScope(tasks, "execute")},
		bindings: []solutionhost.AuthorityBinding{
			{ID: "configuration-write", Audience: docs, Operations: invoke(doc+":write", collection+":create")},
			{ID: "DOCS__SEARCH", Audience: docs, Operations: invoke(doc+":read", collection+":read")},
			{ID: "RECORDS__LIST", Audience: records, Operations: invoke(row + ":read")},
			{ID: "AGENT__READ_INTENT", Audience: agent, Operations: invoke(intents + ":read")},
		}})
	note := wireScope(studio+".note", "read")
	deliverSolution(t, h.ctx, h.conn, h.signer, conformanceSolution{alias: studio, generation: 1, installationRole: domain.NoInstallationRole,
		ceilings: []*authorityv1.WorkScopeV1{note},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {note},
			"editor":    {note, wireScope(intents, "read", "configure", "activate", "run"), wireScope(tasks, "execute")},
			"viewer":    {note, wireScope(intents, "read", "run"), wireScope(tasks, "execute")},
		},
		through: map[string]map[string][]*authorityv1.WorkScopeV1{
			"editor": {"agent": {wireScope(doc, "read", "write"), wireScope(collection, "read", "create"), wireScope(row, "read")}},
			"viewer": {"agent": {wireScope(doc, "read"), wireScope(collection, "read")}},
		},
		bindings: []solutionhost.AuthorityBinding{{ID: "agent", Audience: agent, Operations: invoke(intents+":read", intents+":configure", intents+":activate", intents+":run", tasks+":execute")}}})
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	review, err := h.review(t, alex, studio)
	require.NoError(t, err)
	inst, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review, business.InstallAccess{})
	require.NoError(t, err)
	for role, person := range map[string]string{"editor": conformanceIvy, "viewer": conformanceAlice} {
		_, err := h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: inst.ID, Role: role, PersonID: person})
		require.NoError(t, err)
	}
	all, err := h.core.Installations(h.ctx, alex, fixtureOrganization)
	require.NoError(t, err)
	i := slices.IndexFunc(all, func(i domain.Installation) bool { return i.ID == inst.ID })
	require.GreaterOrEqual(t, i, 0)
	owner := all[i].PrincipalID
	atAgent := func(who string) authority.Issued {
		out, err := h.core.IssueForPage(h.ctx, authority.Person{ID: who, Organization: fixtureOrganization}, studio, authority.IssueRequest{Audience: agent})
		require.NoError(t, err)
		return out
	}
	onward := func(parent authority.Issued, binding string, asked ...*basev0.WorkScopeV1) (authority.Issued, error) {
		return h.core.IssueChild(h.ctx, parent.Token, authority.Workload{Module: agent}, authority.IssueRequest{Binding: binding, Scopes: asked})
	}
	owned := func(kind string, actions ...string) *basev0.WorkScopeV1 {
		return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions, ResourceOwnerId: owner}
	}

	t.Run("a non-admin editor's first save creates the installation's collection", func(t *testing.T) {
		write, err := onward(atAgent(conformanceIvy), "configuration-write")
		require.NoError(t, err)
		attachedTo, err := h.core.AttachResource(h.ctx, authority.AttachmentRequest{Token: write.Token, Kind: collection, ResourceID: "assistant-configuration", Store: authority.Workload{Module: docs}})
		require.NoError(t, err, "the write carries the collection create the role names")
		require.Equal(t, inst.ID, attachedTo)
		_, err = onward(atAgent(conformanceAlice), "configuration-write")
		require.ErrorContains(t, err, "holds nothing at "+docs)
	})
	t.Run("an editor is offered the document, record and agent tools their role names", func(t *testing.T) {
		at := atAgent(conformanceIvy)
		for binding, asked := range map[string][]*basev0.WorkScopeV1{
			"DOCS__SEARCH":       {owned(doc, "read"), owned(collection, "read")},
			"RECORDS__LIST":      {owned(row, "read")},
			"AGENT__READ_INTENT": {owned(intents, "read")},
		} {
			_, err := onward(at, binding, asked...)
			require.NoError(t, err, binding)
		}
		_, err := onward(at, "DOCS__SEARCH", wireCore(doc, "read"), wireCore(collection, "read"))
		require.ErrorContains(t, err, "a child widens what its owner holds", "owner-blind, the ask is the whole kind")
	})
	t.Run("a viewer is offered the document read alone", func(t *testing.T) {
		at := atAgent(conformanceAlice)
		_, err := onward(at, "DOCS__SEARCH", owned(doc, "read"), owned(collection, "read"))
		require.NoError(t, err)
		_, err = onward(at, "RECORDS__LIST", owned(row, "read"))
		require.ErrorContains(t, err, "holds nothing at "+records)
	})
}

func wireCore(kind string, actions ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions}
}
