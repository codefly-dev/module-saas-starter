//go:build !pure

package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// Granting a team a solution CARRIES the module access the solution declares it
// consumes (#1167). The composition under test is the one a validation cell
// took: a solution whose applied manifest declares it invokes another module on
// the asking person's own session, and an organisation that installed both.

const (
	carryKind   = "evidence"
	carryAction = "read"
)

// carryWorld is one organisation that installed a reached module and a solution
// declaring it reaches it, a team, a member of that team, and roles.
type carryWorld struct {
	admin, member, org, team string
	// broadRole permits using a solution and four things on the reached module's
	// kinds, of which the solution declares exactly one. It is the role an
	// administrator picks, and the one option A1 — writing the chosen role at
	// each reached module's root — would have granted there.
	broadRole string
	// narrowRole permits using a solution and nothing the solution consumes, so
	// the intersection at the reached module is empty.
	narrowRole string
	// ownRole is what the team holds at the reached module in its own right, on
	// an action the solution does not declare, so the two grants cannot be
	// mistaken for one another.
	ownRole                 string
	solution, solutionAlias string
	reached, reachedAlias   string
	reachedRoot             string
}

// declareReachingPresence opens a live target whose APPLIED MANIFEST carries the
// authority `authority(alias)` returns, so the host's own reads —
// AppliedAuthorityRequests, the readiness facts, the reached-module resolution —
// see a real declaration rather than a fixture bolted beside them. Written
// through the same store methods the reconcile pass uses, so it cannot construct
// a state the reconciler could not.
func declareReachingPresence(
	t *testing.T, label string, authority func(alias string) *solutionhost.Authority,
) (targetID, alias string) {
	t.Helper()
	alias = uniqueAlias(label)
	bindingID := "acme.test." + alias
	now := time.Now().UTC()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: bindingID, Alias: alias,
		Kind: solutionhost.KindSolution, Generation: 1,
		Host:      solutionhost.HostTarget{Coordinate: "acme-test", Domain: "alpha"},
		Release:   solutionhost.Release{Publisher: "acme", Name: alias, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "api"}},
		Authority: authority(alias),
	}
	document, err := solutionhost.Marshal(manifest)
	require.NoError(t, err, "the fixture must be a manifest this host's own reader accepts")
	entry := business.SolutionHostBindingGeneration{
		Generation: 1, Digest: fixtureDigest(bindingID), Document: string(document), At: now,
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		target, err := testStore.OpenSolutionTarget(ctx, bindingID, alias, 1, now)
		if err != nil {
			return err
		}
		targetID = target.ID
		return testStore.SaveSolutionHostBinding(ctx, &business.SolutionHostBindingRecord{
			BindingID:      bindingID,
			HostCoordinate: "acme/local/host",
			Desired:        &entry,
			Applied: &business.SolutionHostBindingApplied{
				SolutionHostBindingGeneration: entry,
				SolutionID:                    alias,
				Routes:                        []string{alias},
				Domain:                        "alpha",
				Release:                       "acme/" + alias + "@1.0.0",
			},
			UpdatedAt: now,
		})
	}))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		revision, err := testStore.NextSolutionRegistryRevision(ctx)
		if err != nil {
			return err
		}
		return testStore.SaveSolutionRegistration(ctx, &business.SolutionRegistration{
			SolutionID: alias, Publisher: "solution:" + alias, Revision: revision, UpdatedAt: now,
			Declared: &business.SolutionDeclaredBinding{
				BindingID: bindingID, TargetID: targetID, Generation: 1,
				Release: "acme/" + alias + "@1.0.0", Kind: business.SolutionDeclaredKindSolution,
			},
		})
	}))
	return targetID, alias
}

func createRoleWith(t *testing.T, orgID, name string, permissions ...*gen.Permission) string {
	t.Helper()
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleID, Name: name + " " + roleID, OrgId: orgID, Permissions: permissions,
		})
	}))
	return roleID
}

func installTarget(t *testing.T, adminID, orgID, targetID, label string) string {
	t.Helper()
	agentRole := createRoleWith(t, orgID, "agent", &gen.Permission{Resource: "doc", Action: "write"})
	installation, err := testService.InstallSolution(testCtx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/" + label + ":1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  label,
		RoleID:          agentRole,
	})
	require.NoError(t, err)
	acknowledgeInstallation(t, installation)
	return installation.GetId()
}

func newCarryWorld(t *testing.T) *carryWorld {
	t.Helper()
	clearData(t)
	adapters.WithService(testService)
	w := &carryWorld{}
	w.admin, w.org = mustUserAndOrg(t, testCtx, "admin@carry.test", "carry-admin", "Acme Carry")
	w.member = mustMember(t, w.admin, w.org, "member@carry.test", "carry-member")
	team, err := testService.CreateTeam(testCtx, w.admin, &gen.CreateTeamRequest{OrgId: w.org, Name: "engineering"})
	require.NoError(t, err)
	w.team = team.Team.Id
	require.NoError(t, testService.AddTeamMember(testCtx, w.admin, &gen.AddTeamMemberRequest{
		TeamId: w.team, UserId: w.member, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))

	// The reached module. Its applied manifest declares the principal a
	// solution's binding names as its audience, which is how an audience
	// resolves to this organisation's installation of that module.
	reachedTarget, reachedAlias := declareReachingPresence(t, "reachedmod",
		func(alias string) *solutionhost.Authority { return &solutionhost.Authority{Principal: alias} })
	w.reachedAlias = reachedAlias

	// The solution: one binding, invoking the reached module on the asking
	// person's own session, declaring ONE scope of it.
	solutionTarget, solutionAlias := declareReachingPresence(t, "carrysol",
		func(alias string) *solutionhost.Authority {
			return &solutionhost.Authority{Principal: alias, Bindings: []solutionhost.AuthorityBinding{{
				ID: "reach", Audience: reachedAlias,
				Operations: map[string][]string{"invoke": {carryKind + ":" + carryAction}},
			}}}
		})
	w.solutionAlias = solutionAlias

	// The derived registry: what the module envelope allowed of that ask. The
	// grant carries THIS, not the manifest's ask, because a withheld scope is
	// not consumption.
	previous := testService.ModulePrincipals()
	registry := business.ModulePrincipalRegistry{}
	for id, grant := range previous {
		registry[id] = grant
	}
	registry[business.ModulePrincipalID(reachedAlias)] = business.ModulePrincipalGrant{
		Prefix: reachedAlias, PresenceBinding: "acme.test." + reachedAlias, CrossTenant: true,
	}
	registry[business.ModulePrincipalID(solutionAlias)] = business.ModulePrincipalGrant{
		Prefix: solutionAlias, PresenceBinding: "acme.test." + solutionAlias, CrossTenant: true,
		OperationAudiences: map[string]business.ModuleOperationAudience{
			"reach": {Audience: reachedAlias, InvokeScopes: []business.ModuleOperationScope{
				{ResourceKind: carryKind, Actions: []string{carryAction}},
			}},
		},
	}
	testService.SetModulePrincipals(registry)
	t.Cleanup(func() { testService.SetModulePrincipals(previous) })

	w.reached = installTarget(t, w.admin, w.org, reachedTarget, "reached")
	w.reachedRoot = rootScopePath(t, w.org, w.reached)
	w.solution = installTarget(t, w.admin, w.org, solutionTarget, "solution")

	w.broadRole = createRoleWith(t, w.org, "administrators role",
		&gen.Permission{Resource: business.ResourceTypeSolution, Action: business.ActionUseSolution},
		&gen.Permission{Resource: carryKind, Action: carryAction},
		&gen.Permission{Resource: carryKind, Action: "write"},
		&gen.Permission{Resource: carryKind, Action: "delete"},
		&gen.Permission{Resource: "secret", Action: "read"},
	)
	w.narrowRole = createRoleWith(t, w.org, "solution only",
		&gen.Permission{Resource: business.ResourceTypeSolution, Action: business.ActionUseSolution},
	)
	w.ownRole = createRoleWith(t, w.org, "its own reach",
		&gen.Permission{Resource: carryKind, Action: "write"},
	)
	return w
}

func rootScopePath(t *testing.T, orgID, installationID string) string {
	t.Helper()
	var path string
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		path, err = testStore.InstallationRootScopePath(ctx, orgID, installationID)
		return err
	}))
	return path
}

// reaches is the one resolution: ListAccessibleScopes, the same grant + share
// union CheckAccess resolves. A derived role is read by it exactly as any other
// role is, which is why a derived grant can never be a second answer to "what
// does this viewer reach".
func reaches(t *testing.T, orgID, subjectID string, kind gen.SubjectKind, resource, action string) []string {
	t.Helper()
	resp, err := testService.ListAccessibleScopes(testCtx, &gen.ListAccessibleScopesRequest{
		OrgId: orgID, SubjectId: subjectID, SubjectKind: kind, ResourceType: resource, Action: action,
	})
	require.NoError(t, err)
	paths := make([]string, 0, len(resp.GetScopes()))
	for _, scope := range resp.GetScopes() {
		paths = append(paths, scope.GetScopePath())
	}
	return paths
}

func derivedGrantsOf(t *testing.T, orgID, causeGrantID string) []*gen.ScopeGrant {
	t.Helper()
	var grants []*gen.ScopeGrant
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		grants, err = testStore.DerivedScopeGrants(ctx, orgID, causeGrantID, "")
		return err
	}))
	return grants
}

// rolePermits reads one role's permission through the same match every
// authority decision uses, so what a derived role permits is asserted in the
// terms the host itself resolves rather than from its row.
func rolePermits(t *testing.T, orgID, roleID, resource, action string) bool {
	t.Helper()
	var permits bool
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		permits, err = testStore.RolePermits(ctx, roleID, resource, action)
		return err
	}))
	return permits
}

// The property the design was chosen for: the team's DIRECT reach into the
// reached module is exactly what the solution declared, and NOT the role the
// administrator chose. Writing that role at the reached module's root — the
// cheaper option — would have granted write, delete and the rest there.
func TestAGrantOfASolutionCarriesOnlyWhatItDeclaresItConsumes(t *testing.T) {
	w := newCarryWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)

	require.Empty(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, carryAction),
		"a team reaches nothing on the module before the solution is granted")

	granted, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.broadRole,
	})
	require.NoError(t, err)
	require.Len(t, granted.GetCarried(), 1, "the one module the solution declares it reaches")
	carried := granted.GetCarried()[0]
	require.Equal(t, w.reachedAlias, carried.GetAudience())
	require.Equal(t, w.reached, carried.GetInstallationId())
	require.Equal(t, w.reachedRoot, carried.GetScopePath())
	require.Equal(t, []string{carryKind + ":" + carryAction}, carried.GetScopes())
	require.Empty(t, granted.GetUnmet())

	// The derived grant names its cause, so a surface showing it can say why it
	// is there instead of offering a revoke that only breaks the solution.
	derived := derivedGrantsOf(t, w.org, granted.GetGrant().GetId())
	require.Len(t, derived, 1)
	require.Equal(t, w.reachedRoot, derived[0].GetScopePath())
	require.Equal(t, granted.GetGrant().GetId(), derived[0].GetDerivedFromGrantId())

	// It resolves through the SAME union as any other grant, for the team and —
	// through the team — for a member who holds no grant of their own.
	require.Contains(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, carryAction),
		w.reachedRoot)
	require.Contains(t, reaches(t, w.org, w.member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, carryKind, carryAction),
		w.reachedRoot)

	// NOT the chosen role. This is the whole argument for a derived role.
	for _, widened := range []struct{ resource, action string }{
		{carryKind, "write"}, {carryKind, "delete"}, {"secret", "read"},
	} {
		require.NotContains(t,
			reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, widened.resource, widened.action),
			w.reachedRoot,
			"the administrator's role permits %s:%s and the solution does not declare it",
			widened.resource, widened.action)
	}

	// The derived role is in no picker, so nobody is ever offered it; the role
	// the administrator created is.
	roles, err := testService.ListRoles(testCtx, &gen.ListRolesRequest{OrgId: w.org})
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, role := range roles.GetRoles() {
		listed[role.GetId()] = true
	}
	require.False(t, listed[carried.GetRoleId()], "a derived role is never listed")
	require.True(t, listed[w.broadRole])

	// The derived role itself permits the intersection and nothing else, read
	// through the match every authority decision resolves a role with.
	require.True(t, rolePermits(t, w.org, carried.GetRoleId(), carryKind, carryAction))
	require.False(t, rolePermits(t, w.org, carried.GetRoleId(), carryKind, "write"))
	require.False(t, rolePermits(t, w.org, carried.GetRoleId(),
		business.ResourceTypeSolution, business.ActionUseSolution),
		"a derived role is not a solution role: it confers reach into one module and nothing else")
}

// A role that permits none of what the solution consumes there carries nothing,
// and says so in the same response — rather than writing a grant that reveals
// nothing, which is what this repository already refuses a role for.
func TestAnEmptyIntersectionCarriesNothingAndSaysSo(t *testing.T) {
	w := newCarryWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)

	granted, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.narrowRole,
	})
	require.NoError(t, err, "the solution is still exposed; what is empty is the carry")
	require.Empty(t, granted.GetCarried())
	require.Len(t, granted.GetUnmet(), 1)
	require.Equal(t, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_CARRIED_MODULE_ACCESS_NOT_DERIVED,
		granted.GetUnmet()[0].GetKind())
	require.Contains(t, granted.GetUnmet()[0].GetDetail(), business.ErrRoleDoesNotExposeSolution.Error())
	require.Contains(t, granted.GetUnmet()[0].GetDetail(), carryKind+":"+carryAction)
	require.Empty(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, carryAction))
}

// Re-granting the same cause is idempotent on (cause, node, role).
func TestReGrantingIsIdempotentOnTheCauseNodeAndRole(t *testing.T) {
	w := newCarryWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)
	request := &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.broadRole,
	}
	first, err := server.GrantInstallationToTeam(admin, request)
	require.NoError(t, err)
	second, err := server.GrantInstallationToTeam(admin, request)
	require.NoError(t, err)
	require.Equal(t, first.GetCarried()[0].GetRoleId(), second.GetCarried()[0].GetRoleId(),
		"the same triple derives the one role")
	require.Len(t, derivedGrantsOf(t, w.org, first.GetGrant().GetId()), 1, "and the one grant")
}

// A derived grant goes with its cause, and a grant the team holds in its own
// right at the same node does NOT: a derived grant adds, it never replaces.
func TestRevokingTheCauseRevokesWhatItCarriedAndNothingElse(t *testing.T) {
	w := newCarryWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)

	granted, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.broadRole,
	})
	require.NoError(t, err)
	require.Len(t, granted.GetCarried(), 1)

	// The team's own grant at the reached module, written the ordinary way, with
	// no cause.
	_, err = testService.GrantScope(testCtx, w.admin, &gen.GrantScopeRequest{
		OrgId: w.org, SubjectId: w.team, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
		ScopePath: w.reachedRoot, RoleId: w.ownRole,
	})
	require.NoError(t, err)

	_, err = server.RevokeInstallationFromTeam(admin, &gen.RevokeInstallationFromTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.broadRole,
	})
	require.NoError(t, err)

	require.Empty(t, derivedGrantsOf(t, w.org, granted.GetGrant().GetId()),
		"no carried grant outlives the grant that caused it")
	require.Empty(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, carryAction),
		"what the solution carried is gone")
	require.Contains(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, "write"),
		w.reachedRoot, "the team's own direct grant is untouched")

	// Each carried revocation is a scope revocation like any other, so a reader
	// of the record learns why the reach went away.
	events, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID: w.org, EventType: string(business.EventScopeRevoked), PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, events, 2, "the carried grant and its cause, each recorded")
}

// Uninstalling the solution removes the reach its declaration was the only
// justification for, wherever it was granted.
func TestUninstallingTheSolutionRevokesWhatItsGrantsCarried(t *testing.T) {
	w := newCarryWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)

	granted, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.broadRole,
	})
	require.NoError(t, err)
	require.Len(t, granted.GetCarried(), 1)

	require.NoError(t, testService.UninstallSolution(testCtx, w.admin, w.org, w.solution))

	require.Empty(t, derivedGrantsOf(t, w.org, granted.GetGrant().GetId()))
	// The team's grant at the UNINSTALLED solution's own root is left as the
	// scope node is, so a reinstall re-exposes it to the same teams. What must
	// not survive is reach into ANOTHER module that only this installation's
	// declaration justified, so the assertion is about the reached module's node.
	require.NotContains(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, carryAction),
		w.reachedRoot, "the reach the uninstalled solution's declaration justified is gone")
}

// Every way of conferring a role refuses a derived one, and each is its own
// hole: a hand-written scope grant at any node, an ORGANISATION-WIDE assignment
// that does not go through the scope tree at all, and a per-record share.
func TestADerivedRoleIsNeverConferredOrEditedByHand(t *testing.T) {
	w := newCarryWorld(t)
	server := &adapters.InstallationServer{}
	admin := storyCallerContext(testCtx, w.admin, w.org)
	granted, err := server.GrantInstallationToTeam(admin, &gen.GrantInstallationToTeamRequest{
		OrgId: w.org, InstallationId: w.solution, TeamId: w.team, RoleId: w.broadRole,
	})
	require.NoError(t, err)
	derivedRole := granted.GetCarried()[0].GetRoleId()

	_, err = testService.GrantScope(testCtx, w.admin, &gen.GrantScopeRequest{
		OrgId: w.org, SubjectId: w.team, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
		ScopePath: w.reachedRoot, RoleId: derivedRole,
	})
	require.ErrorContains(t, err, "derived by this host", "a hand-written grant of a derived role is refused")
	// And the refusal names the provenance the role records: the installation it
	// was derived from, the module audience, and the role it was intersected
	// with — so an operator reading it learns why the role exists.
	require.ErrorContains(t, err, "installation="+w.solution)
	require.ErrorContains(t, err, "audience="+w.reachedAlias)
	require.ErrorContains(t, err, "role="+w.broadRole)

	// The org-role path. An assignment here would hand every member the module
	// reach with no installation grant at all, so it is guarded as hard.
	_, err = testService.AssignRole(testCtx, &gen.AssignRoleRequest{
		OrgId: w.org, SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		RoleId: derivedRole,
	})
	require.ErrorContains(t, err, "derived by this host", "an organisation-wide assignment is refused")

	_, err = testService.ShareRecord(testCtx, w.admin, &gen.ShareRecordRequest{
		OrgId: w.org, ResourceType: carryKind, ResourceId: business.NewIDString(),
		SubjectId: w.team, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM, RoleId: derivedRole,
	})
	require.ErrorContains(t, err, "derived by this host", "a per-record share is refused")

	// Nor edited, nor deleted: one would silently widen a live carried grant,
	// the other would cascade it away with no revocation to explain it.
	_, err = testService.UpdateRole(testCtx, w.admin, &gen.UpdateRoleRequest{
		OrgId: w.org, Id: derivedRole, Description: "mine now",
		Permissions: []*gen.Permission{{Resource: "secret", Action: "read"}},
	})
	require.ErrorContains(t, err, "derived by this host")
	require.ErrorContains(t,
		testService.DeleteRole(testCtx, w.admin, &gen.DeleteRoleRequest{Id: derivedRole}),
		"derived by this host")

	// The carried grant still permits exactly what it did.
	require.Contains(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, carryKind, carryAction),
		w.reachedRoot)
	require.NotContains(t, reaches(t, w.org, w.team, gen.SubjectKind_SUBJECT_KIND_TEAM, "secret", "read"),
		w.reachedRoot)
}

var _ = context.Background
