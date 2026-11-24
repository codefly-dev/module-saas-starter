package adapters

import (
	"context"
	"fmt"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	solutionhost "github.com/codefly-dev/authority/libraries/solutionhost/go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// residueHarness is what the cases below need of the conformance harness.
type residueHarness struct {
	conn grpc.ClientConnInterface
	// admin is the administrator's session and user another person's; each
	// holds the organisation role the cases create over the relays' kind.
	admin, user     context.Context
	adminID, userID string
	credential      func(string) string
	// deliver delivers a module (at its generation) and installs it.
	deliver func(conformanceModule)
	page    *solutionhost.Frontend
	// seat grants person a role on a module page's installation and unseat
	// revokes it, as the core records both.
	seat, unseat func(target, role, person string)
	// agent registers an agent principal of the organization.
	agent func(identifier string) string
	// solution delivers a solution and installs it holding role as its agent
	// role; it answers the installation.
	solution func(s conformanceSolution, role string) string
	// grant writes the next envelope revision (installedActorHarness.grant).
	grant func(principals string)
	// reconsent installs a module again at what it declares now.
	reconsent func(module string)
}

// The residue page's composition: a page whose admin role is granted a
// relay's turn (run and read) addressed to that relay, a sibling relay
// serving the same kind, and one operation binding of the page to each.
const (
	residuePage    = "conformance-residue-page"
	residueRelay   = "conformance-residue-relay"
	residueSibling = "conformance-residue-other-relay"
	residueTurn    = residueRelay + ".turn"
)

func residueBindings() []solutionhost.AuthorityBinding {
	invoke := map[string][]string{"invoke": {residueTurn + ":run", residueTurn + ":read"}}
	return []solutionhost.AuthorityBinding{
		{ID: "relay", Audience: residueRelay, Operations: invoke},
		{ID: "other", Audience: residueSibling, Operations: invoke},
	}
}

// testAudienceGrantResidue: what testAudienceGrantOwnSession left open on
// accounts' own Work Context mints, each held to what the shared core issues.
// The core holds a role's grant on another module's kind at the module it is
// addressed to, through a binding of the unit: never on a capability
// addressed to the unit, never at a sibling serving the same kind.
//
// Two authorities bound these mints, and the cases name which decides. The
// role a person holds on a role-gated module's installation decides what
// their own session holds there and what the module's bindings carry for
// them. Organization RBAC alone decides a kind the audience does not serve
// named outright, and every mint an agent acts on (StartTask naming an
// actor): the two admissions pinned below as pending the owner's decision.
func testAudienceGrantResidue(t *testing.T, h residueHarness) {
	turn := wireScope(residueTurn, "read", "run")
	for _, alias := range []string{residueRelay, residueSibling} {
		h.deliver(conformanceModule{alias: alias, ceilings: []*authorityv1.WorkScopeV1{turn}})
	}
	note := wireScope(residuePage+".note", "read")
	h.deliver(conformanceModule{alias: residuePage, ceilings: []*authorityv1.WorkScopeV1{note}, page: h.page, installationRole: domain.NoInstallationRole,
		// The admin holds the relay's turn at the relay, and nowhere else;
		// the user holds the page's notes alone.
		roles:     map[string][]*authorityv1.WorkScopeV1{"admin": {note}, "user": {note}},
		audiences: map[string]map[string][]*authorityv1.WorkScopeV1{"admin": {residueRelay: {turn}}},
		bindings:  residueBindings()})
	h.seat(residuePage, domain.AdminRole, h.adminID)
	h.seat(residuePage, domain.UserRole, h.userID)

	// An organisation role names the relays' kind, held by both people and by
	// the agent that acts for them: organization RBAC refuses nothing below.
	agent := h.agent("conformance/residue-agent:0.1.0")
	residueOperators(t, h, h.adminID, h.userID, agent)

	contexts := gen.NewWorkContextServiceClient(h.conn)
	ask := []*gen.WorkContextScope{{ResourceKind: residueTurn, Actions: []string{"read", "run"}}}
	// start is StartTask at audience as who: actor "" is the person's own
	// session, and bindings project through the audience's operation bindings.
	start := func(who context.Context, audience, actor string, scopes []*gen.WorkContextScope, bindings ...string) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(who, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: audience, AuthorityScopes: scopes, OperationBindings: bindings, ActorPrincipalId: actor})
	}
	// child is StartChildSession: the administrator delegating parent to the agent.
	child := func(parent, audience string, scopes []*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
		return contexts.StartChildSession(h.admin, &gen.StartChildSessionWorkContextRequest{OrgId: fixtureOrganization, ParentWorkContextToken: parent,
			ActorPrincipalId: agent, GrantedScopes: scopes, Audience: audience})
	}
	// renew is RenewWorkContext as the agent, the current actor of parent.
	renew := func(parent string) (*gen.IssuedWorkContext, error) {
		ctx := stampVerifiedIdentity(context.Background(), agent, fixtureOrganization, accountsauth.Assurance{})
		return workContextSingleton.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{OrgId: fixtureOrganization, ParentWorkContextToken: parent})
	}
	// renewable is the parent check behind RenewWorkContext's door, as the
	// agent: on this store the door itself refuses a registered agent, which
	// is no member of the organization, so the check is asked directly.
	renewable := func(parent string) error {
		_, _, err := workContextSingleton.verifyActorParent(context.Background(), fixtureOrganization, agent, parent)
		return err
	}
	exchange := func(module, binding string, parent *gen.IssuedWorkContext) (*gen.IssuedWorkContext, error) {
		return moduleExchange(t, h.credential, module, binding, parent.GetToken())
	}
	denied := func(t *testing.T, err error, why string) {
		t.Helper()
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", why, err)
	}
	stale := func(t *testing.T, err error, why string) {
		t.Helper()
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%s: %v", why, err)
		require.Contains(t, status.Convert(err).Message(), "stale", why)
	}
	carries := func(t *testing.T, issued *gen.IssuedWorkContext, err error, why string) {
		t.Helper()
		require.NoError(t, err, why)
		require.Equal(t, []string{residueTurn}, carriedKinds(t, issued), why)
	}

	// PENDING THE OWNER'S DECISION, and pinned as it stands: the callee's kind
	// named outright on a StartTask at the role-gated page. The core never
	// signs a role's addressed grant onto a capability addressed to the unit;
	// this mint does, whenever an organisation role names the kind, whatever
	// role the person holds on the page (the user below holds none over it).
	// The scope is inert at the page, which serves no such kind, and what is
	// pinned with it is that the capability reaches nothing beyond the role:
	// it verifies at no other audience, the page's bindings carry it only to
	// the relay the role's grant is addressed to, and the owner's own
	// exchange, root session and child reach neither relay. The decision
	// pending is whether this mint should refuse a kind the audience does not
	// serve and the person's role holds only addressed elsewhere, as the core
	// does (the scope would then arrive at the page by projection through a
	// binding alone, operation_bindings), or keep carrying what organization
	// RBAC names. Until then nothing here changes that admission.
	t.Run("the callee's kind named outright is admitted on organization RBAC and reaches no sibling", func(t *testing.T) {
		outright, err := start(h.admin, residuePage, "", ask)
		carries(t, outright, err, "admitted: an organisation role names the kind, and it is inert at the page")
		_, err = authorityv1.NewAuthorityServiceClient(h.conn).Check(h.admin, &authorityv1.CheckRequest{Token: outright.GetToken(), Audience: residueSibling})
		require.Equal(t, codes.Unauthenticated, status.Code(err), "addressed to the page, it is no capability at the sibling: %v", err)
		require.Contains(t, status.Convert(err).Message(), "minted for audience")

		_, err = exchange(residuePage, "other", outright)
		denied(t, err, "the page's exchange through its binding to the sibling")
		through, err := exchange(residuePage, "relay", outright)
		carries(t, through, err, "the page's exchange through its binding to the relay the grant is addressed to")
		for _, audience := range []string{residueRelay, residueSibling} {
			_, err := contexts.ExchangeAudience(h.admin, &gen.ExchangeWorkContextAudienceRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: outright.GetToken(), Audience: audience, AttenuatedScopes: ask})
			refusedAtNoRoles(t, err, audience)
			_, err = contexts.StartRootSession(h.admin, &gen.StartRootSessionWorkContextRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: outright.GetToken(), SessionId: uuid.NewString(), Audience: audience})
			refusedAtNoRoles(t, err, audience)
			_, err = child(outright.GetToken(), audience, ask)
			denied(t, err, "a child keeps its parent's audience")
		}
		// What the page was exchanged for the relay is addressed to the relay:
		// the page exchanges it no further.
		_, err = exchange(residuePage, "other", through)
		denied(t, err, "the page's exchange of a capability addressed to the relay")

		// A person whose role on the page holds nothing at either relay is
		// admitted the same inert scope, and it is carried to neither.
		inert, err := start(h.user, residuePage, "", ask)
		carries(t, inert, err, "admitted on organization RBAC, whatever the role on the page")
		for _, binding := range []string{"relay", "other"} {
			_, err := exchange(residuePage, binding, inert)
			denied(t, err, "the page's exchange through binding "+binding+" for a role that holds nothing there")
		}
	})

	// A capability an agent acts on is minted on organization RBAC, the
	// owner's and the agent's, and is no own session: no hold of the own
	// session's applied to it, so the page's binding carried it to the
	// sibling, and to either relay for a person whose role holds nothing
	// there. A role-gated module's binding now carries a person's capability
	// by the person's role on its installation, whoever acts.
	t.Run("an agent acting for the person carries through the page's bindings what the person's role holds", func(t *testing.T) {
		chained, err := start(h.admin, residuePage, agent, ask)
		carries(t, chained, err, "minted on organization RBAC, the owner's and the agent's")
		kid, err := child(mustStart(t, start, h.admin, residuePage, ask).GetToken(), residuePage, ask)
		carries(t, kid, err, "a child of the person's own session, delegated to the agent")
		for name, parent := range map[string]*gen.IssuedWorkContext{"a mint naming the agent": chained, "a child delegated to the agent": kid} {
			_, err := exchange(residuePage, "other", parent)
			denied(t, err, name+", through the binding to the sibling")
			through, err := exchange(residuePage, "relay", parent)
			carries(t, through, err, name+", through the binding to the relay the grant is addressed to")
		}
		for _, binding := range []string{"relay", "other"} {
			inert, err := start(h.user, residuePage, agent, ask)
			carries(t, inert, err, "minted on organization RBAC")
			_, err = exchange(residuePage, binding, inert)
			denied(t, err, "an agent acting for a person whose role holds nothing through binding "+binding)
		}

		// A child never widens its parent, and keeps its audience.
		narrow, err := start(h.admin, residuePage, "", []*gen.WorkContextScope{{ResourceKind: residueTurn, Actions: []string{"read"}}})
		require.NoError(t, err)
		_, err = child(narrow.GetToken(), residuePage, ask)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "a child asking more than its parent carries: %v", err)

		// PENDING THE SAME DECISION as the kind named outright, and pinned as
		// it stands: a mint naming an agent is decided by organization RBAC
		// alone at every audience, so it is admitted at each relay directly,
		// modules that declare no roles and hold nothing for the person's own
		// session. No binding and no role's grant is involved in it.
		for _, audience := range []string{residueRelay, residueSibling} {
			_, err := start(h.admin, audience, "", ask)
			refusedAtNoRoles(t, err, audience)
			direct, err := start(h.admin, audience, agent, ask)
			carries(t, direct, err, "an agent's mint at "+audience+", on organization RBAC alone")
		}
	})

	// A capability minted on a role is refused once the role is revoked, as
	// the core's own is (its Check and IssueChild read the grant epoch a
	// revocation advances): the seal this host signs a person's capability
	// under now moves with the roles they hold on the installation. Before,
	// the revocation moved nothing this host's verifier read, and a child was
	// derived from a capability the revoked role had minted.
	t.Run("a role revoked ends what was minted on it: no child, exchange or renewal", func(t *testing.T) {
		const module = "conformance-residue-revoked"
		own := wireScope(module+".note", "read")
		h.deliver(conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{own}, page: h.page, installationRole: domain.NoInstallationRole,
			roles:     map[string][]*authorityv1.WorkScopeV1{"admin": {own}, "user": {own}},
			audiences: map[string]map[string][]*authorityv1.WorkScopeV1{"admin": {residueRelay: {turn}}},
			bindings:  residueBindings()})
		h.seat(module, domain.AdminRole, h.adminID)
		through, err := start(h.admin, module, "", nil, "relay")
		carries(t, through, err, "projected through the binding on the role")
		kid, err := child(through.GetToken(), module, ask)
		carries(t, kid, err, "a child while the role is held")
		chained, err := start(h.admin, module, agent, ask)
		require.NoError(t, err)

		h.unseat(module, domain.AdminRole, h.adminID)
		_, err = child(through.GetToken(), module, ask)
		stale(t, err, "a child of a capability minted before the role was revoked")
		for name, parent := range map[string]*gen.IssuedWorkContext{"the person's own": through, "the child's": kid, "the agent's": chained} {
			_, err := exchange(module, "relay", parent)
			stale(t, err, "the page's exchange of a capability minted before the revocation, "+name)
		}
		for name, parent := range map[string]*gen.IssuedWorkContext{"the child's": kid, "the agent's": chained} {
			stale(t, renewable(parent.GetToken()), "the renewal's parent check, "+name)
			_, err := renew(parent.GetToken())
			denied(t, err, "a renewal, "+name)
		}
		_, err = start(h.admin, module, "", nil, "relay")
		denied(t, err, "a projection once the role is revoked")

		// Granted again, the role mints again; what was minted before stays ended.
		h.seat(module, domain.AdminRole, h.adminID)
		again, err := start(h.admin, module, "", nil, "relay")
		carries(t, again, err, "a projection on the role granted again")
		_, err = child(through.GetToken(), module, ask)
		stale(t, err, "a capability minted before the revocation stays ended")
	})

	// A redelivery moving a role's literal grant to one addressed to a single
	// module narrows the role: it held the kind wherever a binding carried it,
	// and now holds it at one module. What was minted before is refused.
	t.Run("a redelivery narrowing a literal grant to an addressed one ends what was minted before it", func(t *testing.T) {
		const module = "conformance-residue-moving"
		own := wireScope(module+".note", "read")
		moving := conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{own}, page: h.page, installationRole: domain.NoInstallationRole,
			roles:    map[string][]*authorityv1.WorkScopeV1{"admin": {own, turn}, "user": {own}},
			bindings: residueBindings()}
		h.deliver(moving)
		h.seat(module, domain.AdminRole, h.adminID)
		literal, err := start(h.admin, module, "", nil, "other")
		carries(t, literal, err, "a literal grant is held wherever a binding carries it, the sibling included")
		kid, err := child(literal.GetToken(), module, ask)
		require.NoError(t, err)
		chained, err := start(h.admin, module, agent, ask)
		require.NoError(t, err)

		moving.generation = 2
		moving.roles = map[string][]*authorityv1.WorkScopeV1{"admin": {own}, "user": {own}}
		moving.audiences = map[string]map[string][]*authorityv1.WorkScopeV1{"admin": {residueRelay: {turn}}}
		h.deliver(moving)

		_, err = child(literal.GetToken(), module, ask)
		stale(t, err, "a child of a capability minted before the narrowing")
		for name, parent := range map[string]*gen.IssuedWorkContext{"the person's own": literal, "the child's": kid, "the agent's": chained} {
			_, err := exchange(module, "other", parent)
			stale(t, err, "the page's exchange to the sibling of a capability minted before the narrowing, "+name)
		}
		for name, parent := range map[string]*gen.IssuedWorkContext{"the child's": kid, "the agent's": chained} {
			stale(t, renewable(parent.GetToken()), "the renewal's parent check, "+name)
			_, err := renew(parent.GetToken())
			denied(t, err, "a renewal, "+name)
		}
		_, err = start(h.admin, module, "", nil, "other")
		denied(t, err, "a projection to the sibling once the grant is addressed to the relay")
		addressed, err := start(h.admin, module, "", nil, "relay")
		carries(t, addressed, err, "a projection to the relay the grant is now addressed to")
	})

	// The installation's agent. The core issues an installation acting for
	// itself what its agent role holds at the audience (RoleScopesAt): an
	// addressed grant at its callee, through the solution's binding. This
	// host's own headless mint (StartInstallationTask) reads another record:
	// an installation of this host's registry and the standing grant of an
	// organisation role to its agent. An installation of a solution that
	// declares roles is the core's record alone, so that mint issues nothing
	// for it at any audience, and no solution role, addressed or not, is read
	// by it anywhere.
	t.Run("the installation's agent holds an addressed grant at its callee, and only the core issues it", func(t *testing.T) {
		const solution = "conformance-residue-solution"
		own := wireScope(solution+".page", "read")
		installation := h.solution(conformanceSolution{alias: solution, generation: 1, ceilings: []*authorityv1.WorkScopeV1{own},
			roles:     map[string][]*authorityv1.WorkScopeV1{"admin": {own}, "user": {own}},
			audiences: map[string]map[string][]*authorityv1.WorkScopeV1{"user": {residueRelay: {turn}}},
			bindings:  residueBindings()}, domain.UserRole)

		// Everything the agent holds at audience is asked for: the agent's
		// scopes select the installation's own data, which no ask here names.
		issue := func(audience, binding string) ([]string, error) {
			issued, err := authorityv1.NewAuthorityServiceClient(h.conn).Issue(context.Background(), &authorityv1.IssueRequest{
				As:    &authorityv1.IssueRequest_Installation{Installation: &authorityv1.InstallationRef{InstallationId: installation}},
				Actor: &authorityv1.WorkloadCredential{Credential: h.credential(solution)}, OrganizationId: fixtureOrganization,
				Audience: audience, OperationBindingId: binding})
			var kinds []string
			for _, sc := range issued.GetContext().GetAuthorityScopes() {
				kinds = append(kinds, sc.GetResourceKind())
			}
			return kinds, err
		}
		atRelay, err := issue(residueRelay, "relay")
		require.NoError(t, err, "the core: the agent role's addressed grant, at its callee through the binding")
		require.Equal(t, []string{residueTurn}, atRelay)
		_, err = issue(residueSibling, "other")
		denied(t, err, "the core: the same grant at the sibling")
		require.Contains(t, status.Convert(err).Message(), "holds nothing at this audience")
		atUnit, err := issue(solution, "")
		require.NoError(t, err)
		require.Equal(t, []string{solution + ".page"}, atUnit, "the core: a capability addressed to the solution carries its own kind and never the addressed grant")

		SetInternalToken("residue-headless-mint")
		t.Cleanup(func() { SetInternalToken("") })
		internal := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "residue-headless-mint"))
		for _, audience := range []string{residueRelay, residueSibling, solution} {
			_, err := workContextSingleton.StartInstallationTask(internal, &gen.StartInstallationTaskRequest{OrgId: fixtureOrganization, InstallationId: installation,
				TaskId: uuid.NewString(), Audience: audience, AuthorityScopes: ask})
			require.Equal(t, codes.FailedPrecondition, status.Code(err), "this host's headless mint at %s: %v", audience, err)
			require.Contains(t, status.Convert(err).Message(), fmt.Sprintf("active installation %s not found", installation))
		}
	})
}

// residueOperators assigns subjects the organisation role over the relays'
// kind for the length of the calling case, creating it the first time.
func residueOperators(t *testing.T, h residueHarness, subjects ...string) {
	t.Helper()
	const name = "Residue turn operator"
	permissions := gen.NewPermissionServiceClient(h.conn)
	roles, err := permissions.ListRoles(h.admin, &gen.ListRolesRequest{OrgId: fixtureOrganization})
	require.NoError(t, err)
	var operator string
	for _, role := range roles.GetRoles() {
		if role.GetName() == name {
			operator = role.GetId()
		}
	}
	if operator == "" {
		created, err := permissions.CreateRole(h.admin, &gen.CreateRoleRequest{OrgId: fixtureOrganization, Name: name,
			Permissions: []*gen.Permission{{Resource: residueTurn, Action: "run"}, {Resource: residueTurn, Action: "read"}}})
		require.NoError(t, err)
		operator = created.GetRole().GetId()
	}
	for _, subject := range subjects {
		_, err := permissions.AssignRole(h.admin, &gen.AssignRoleRequest{SubjectId: subject, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: operator, OrgId: fixtureOrganization})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = permissions.RevokeRole(h.admin, &gen.RevokeRoleRequest{SubjectId: subject, RoleId: operator, OrgId: fixtureOrganization})
		})
	}
}

// mustStart is a person's own session at audience carrying scopes.
func mustStart(t *testing.T, start func(context.Context, string, string, []*gen.WorkContextScope, ...string) (*gen.IssuedWorkContext, error), who context.Context, audience string, scopes []*gen.WorkContextScope) *gen.IssuedWorkContext {
	t.Helper()
	issued, err := start(who, audience, "", scopes)
	require.NoError(t, err)
	return issued
}

// moduleReadExchange is module exchanging parent through its read binding,
// presenting the module identity its workload credential is minted.
func moduleReadExchange(t *testing.T, credential func(string) string, module, binding, parent string) (*gen.IssuedWorkContext, error) {
	t.Helper()
	identity, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(), &gen.ModuleMintWorkContextRequest{
		Prefix: module, ServiceAccountToken: credential(module), OrganizationId: fixtureOrganization})
	require.NoError(t, err)
	return ModuleCapabilitiesSingleton().ExchangeDelegatedReadAudience(presentedAs(t, identity.GetToken()),
		&gen.ModuleExchangeDelegatedReadAudienceRequest{BindingId: binding, ParentWorkContextToken: parent})
}

// testReadBindingHeldToTheRole: a read binding is deployment policy (an
// envelope's read_audiences), exchanged with no operation binding named, and
// the core declares none: the page's read binding to a relay carried there
// whatever the parent carried, so a read the admin role holds at one relay
// reached its sibling, and a person whose role holds nothing at either
// reached both on an organisation role. A role-gated module's read binding
// now carries what the core issues the person at that module through the
// page's operation bindings addressed to it.
//
// It runs after testAudienceGrantResidue, on the page that delivered, and
// among the harness's last cases: the envelope it writes becomes every
// module's ceiling.
func testReadBindingHeldToTheRole(t *testing.T, h residueHarness) {
	scopes := fmt.Sprintf(`[{"resource_kind":%q,"actions":["read","run"]}]`, residueTurn)
	read := func(audience string) string {
		return fmt.Sprintf(`{"audience":%q,"scopes":[{"resource_kind":%q}]}`, audience, residueTurn)
	}
	h.grant(fmt.Sprintf(`%q:{"grant":{"tenant":%q,"resources":[%q],`+
		`"operation_audiences":{"relay":{"audience":%q,"invoke_scopes":%s},"other":{"audience":%q,"invoke_scopes":%s}},`+
		`"read_audiences":{"peek-relay":%s,"peek-other":%s}}},`+
		`%q:{"grant":{"tenant":%q,"resources":[%q]}},%q:{"grant":{"tenant":%q,"resources":[%q]}}`,
		residuePage, fixtureOrganization, residuePage+".note", residueRelay, scopes, residueSibling, scopes, read(residueRelay), read(residueSibling),
		residueRelay, fixtureOrganization, residueTurn, residueSibling, fixtureOrganization, residueTurn))
	// The installation consents to the read bindings the envelope adds.
	h.reconsent(residuePage)
	residueOperators(t, h, h.adminID, h.userID)

	contexts := gen.NewWorkContextServiceClient(h.conn)
	outright := func(who context.Context) *gen.IssuedWorkContext {
		issued, err := contexts.StartTask(who, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: residuePage,
			AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: residueTurn, Actions: []string{"read"}}}})
		require.NoError(t, err, "inert at the page, bounded by organization RBAC")
		return issued
	}
	denied := func(err error, why string) {
		t.Helper()
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", why, err)
		require.Contains(t, status.Convert(err).Message(), "than a read binding carries there", why)
	}

	// The admin's role holds the relay's turn at the relay alone.
	admin := outright(h.admin)
	held, err := moduleReadExchange(t, h.credential, residuePage, "peek-relay", admin.GetToken())
	require.NoError(t, err, "the read binding to the relay the role's grant is addressed to")
	require.Equal(t, []string{residueTurn}, carriedKinds(t, held))
	_, err = moduleReadExchange(t, h.credential, residuePage, "peek-other", admin.GetToken())
	denied(err, "the read binding to the sibling")

	// The user's role holds nothing at either, whatever organisation role names the kind.
	user := outright(h.user)
	for _, binding := range []string{"peek-relay", "peek-other"} {
		_, err := moduleReadExchange(t, h.credential, residuePage, binding, user.GetToken())
		denied(err, "read binding "+binding+" for a role that holds nothing there")
	}
}
