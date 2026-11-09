package adapters

import (
	"slices"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
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
		require.Empty(t, inst.PrincipalID, "its installation is composition's, holding no standing authority")
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

	t.Run("Admin → Solutions lists solutions; module pages appear only as set-up entries", func(t *testing.T) {
		offers, err := h.core.Offers(h.ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		for _, m := range offers {
			require.Equal(t, domain.KindSolution, m.Kind, "%s is offered for install", m.Name)
		}
		target := available(page)
		require.Equal(t, "module", target.Kind)
		_, err = h.svc.ReviewSolutionInstall(h.ctx, alex, fixtureOrganization, target.TargetID)
		require.Error(t, err, "a module page is never reviewed for install")
		code, _ := domain.CodeOf(err)
		require.Equal(t, domain.NotFound, code)
		const solutionAlias = "conformance-access-own"
		_, err = h.svc.SetUpModulePage(h.ctx, alex, fixtureOrganization, available(solutionAlias).TargetID, business.InstallAccess{})
		require.ErrorContains(t, err, "is not a module page", "a solution's access is assigned at its install")
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
		// The store, not the solution, changes: Purge now needs write, which
		// the viewer reaches through the solution's binding.
		deliverStore(2, "write")
		require.True(t, upgrade(), "the solution's reach widened, so an update is offered on it")
		offer, err := h.core.ReviewOffer(h.ctx, alex, fixtureOrganization, target)
		require.NoError(t, err)
		require.Contains(t, reachOperations(offer, "viewer", store), "/conformance.reach.Store/Purge", "the administrator reviews the wider reach")
		require.NotContains(t, approvedOperations(t, h, inst.Installation, "viewer", store), "/conformance.reach.Store/Purge", "holders keep the approved reach")
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
		require.Empty(t, granted[0].GetResourceOwnerId(), "a page's role selects no owner")
		require.Equal(t, store, child.Context.GetAudience())
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
