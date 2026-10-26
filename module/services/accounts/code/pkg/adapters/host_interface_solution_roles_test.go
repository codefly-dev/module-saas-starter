package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// fixedSolutionRoles answers, for each solution principal it names, the
// solution's own kinds and what the owner holds through its roles; any other
// principal is not a solution whose roles decide it.
type fixedSolutionRoles map[string]struct {
	kinds []string
	held  []*basev0.WorkScopeV1
}

func (f fixedSolutionRoles) PersonSolutionRoles(_ context.Context, _, _, principal string) (*business.SolutionAuthority, []*basev0.WorkScopeV1, error) {
	solution, ok := f[principal]
	if !ok {
		return nil, nil, nil
	}
	return &business.SolutionAuthority{Principal: principal, Kinds: solution.kinds}, solution.held, nil
}

// The live refusal: a solution acting for a person through its binding to a
// module carries what the person's role at the solution grants there, although
// the person holds nothing at that module through organisation roles.
func TestASolutionsHopCarriesWhatThePersonsSolutionRoleGrants(t *testing.T) {
	f := newHopFixture(t) // organisation roles grant nothing
	f.server.roles = fixedSolutionRoles{"tools": {kinds: []string{"tool.items"}, held: []*basev0.WorkScopeV1{
		hopScope("tool.items", []string{"read"}), hopScope("example-records", []string{"read"}),
	}}}
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	out, err := f.hop(parent, f.tool, "records", "documents", ask("example-records", []string{"read"}))
	require.NoError(t, err)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}, claims(t, out).GetActorChain()[0].GetGrantedScopes())

	// The binding still bounds it: invoke is in the binding, not in the role.
	_, err = f.hop(parent, f.tool, "records", "documents", ask("example-records", []string{"invoke"}))
	requireRefused(t, err, codes.PermissionDenied, "widens what its owner holds")
}

// Where a solution's roles decide, organisation roles do not add to them.
func TestASolutionsHopIsNotWidenedByOrganisationRoles(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	f.server.roles = fixedSolutionRoles{"tools": {kinds: []string{"tool.items"}, held: []*basev0.WorkScopeV1{hopScope("tool.items", []string{"read"})}}}
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	_, err := f.hop(parent, f.tool, "records", "documents")
	requireRefused(t, err, codes.PermissionDenied, "holds nothing at documents through operation binding \"records\"")
}

// A parent issued at a solution stands only while its roles still grant what
// it carries.
func TestASolutionParentCarryingMoreThanItsRolesNowGrantIsRefused(t *testing.T) {
	f := newHopFixture(t)
	f.server.roles = fixedSolutionRoles{"tools": {kinds: []string{"tool.items"}, held: []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}}}
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	_, err := f.hop(parent, f.tool, "records", "documents")
	requireRefused(t, err, codes.PermissionDenied, "now grant")
}

// A person is issued at a solution what their roles there grant on its own
// kinds, and refused what they do not.
func TestAPersonAtASolutionHoldsTheirSolutionRolesGrants(t *testing.T) {
	_, server, _ := hostChildFixture(t)
	server.roles = fixedSolutionRoles{"documents": {kinds: []string{"documents"}, held: []*basev0.WorkScopeV1{
		hopScope("documents", []string{"read"}), hopScope("example-records", []string{"read"}),
	}}}
	caller := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, auth.Assurance{})
	issue := func(scopes ...*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
		return server.Issue(caller, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: "documents", Scopes: scopes})
	}
	out, err := issue()
	require.NoError(t, err)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("documents", []string{"read"})}, claims(t, out).GetAuthorityScopes())
	_, err = issue(ask("documents", []string{"write"}))
	requireRefused(t, err, codes.PermissionDenied, "do not grant documents:write")

	server.roles = fixedSolutionRoles{"documents": {kinds: []string{"documents"}}}
	_, err = issue()
	requireRefused(t, err, codes.PermissionDenied, "the person holds nothing at documents")
}

// An audience the organisation never installed is a failed precondition, named,
// for a person whether a solution's roles or the organisation's would decide
// what they hold there: never that they hold nothing.
func TestAPersonAtAnUninstalledAudienceIsAFailedPrecondition(t *testing.T) {
	issuer, server, _ := hostChildFixture(t)
	issuer.seals.(*sealAnswersFake).uninstalled["documents"] = true
	caller := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, auth.Assurance{})
	for name, roles := range map[string]solutionRoleReader{
		// What PersonSolutionRoles answers for a solution the organisation
		// never installed: its declaration, and nothing held through it.
		"a solution's roles": fixedSolutionRoles{"documents": {kinds: []string{"documents"}}},
		"organisation roles": fixedSolutionRoles{},
	} {
		t.Run(name, func(t *testing.T) {
			server.roles = roles
			_, err := server.Issue(caller, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: "documents"})
			requireRefused(t, err, codes.FailedPrecondition, `audience "documents" is not installed in this organization, so there is no installation to seal the capability to`)
		})
	}
}

// A person issued at a solution through one of its bindings carries what the
// solution's roles grant them within that binding: an ask within it is issued
// as asked, an ask beyond it is refused by name, and a person whose roles
// grant nothing through it is refused for that.
func TestAPersonAtASolutionThroughItsBindingCarriesTheirRolesWithinIt(t *testing.T) {
	f := newHopFixture(t)
	withHopMember(t)
	caller := stampVerifiedIdentity(context.Background(), hopOwner, moduleWorkContextTenant, auth.Assurance{})
	issue := func(scopes ...*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
		return f.server.Issue(caller, &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: "tools", OperationBindingId: "records", Scopes: scopes,
		})
	}
	f.server.roles = fixedSolutionRoles{"tools": {kinds: []string{"tool.items"}, held: []*basev0.WorkScopeV1{
		hopScope("tool.items", []string{"read"}), hopScope("example-records", []string{"read", "write"}),
	}}}
	out, err := issue(ask("example-records", []string{"read"}))
	require.NoError(t, err)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}, claims(t, out).GetAuthorityScopes())

	out, err = issue()
	require.NoError(t, err)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("tool.items", []string{"read"}), hopScope("example-records", []string{"read"})}, claims(t, out).GetAuthorityScopes())

	_, err = issue(ask("example-records", []string{"write"}))
	requireRefused(t, err, codes.PermissionDenied, "beyond what the person's roles at tools grant")

	f.server.roles = fixedSolutionRoles{"tools": {kinds: []string{"tool.items"}, held: []*basev0.WorkScopeV1{hopScope("tool.items", []string{"read"})}}}
	_, err = issue()
	requireRefused(t, err, codes.PermissionDenied, "holds nothing at documents through operation binding \"records\"")
}

// withHopMember recomposes the hop fixture's principals over a store in which
// the person is a member of the pinned organisation holding no organisation
// role, as an issue through the interface reads them.
func withHopMember(t *testing.T) {
	t.Helper()
	svc, err := business.NewService(hopMemberStore{recordingModuleStore: &recordingModuleStore{tenantExists: true}})
	require.NoError(t, err)
	registry, err := moduleregistrytest.Parse(hopPrincipals)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	bindModuleExecution(t, svc, registry)
	WithService(svc)
}

// hopMemberStore is the hop fixture's store with the person a member of the
// pinned organisation and no organisation role defined.
type hopMemberStore struct{ *recordingModuleStore }

func (hopMemberStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (hopMemberStore) ListRoles(context.Context, string) ([]*gen.Role, error) { return nil, nil }

func (hopMemberStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if orgID == moduleWorkContextTenant && userID == hopOwner {
		return &gen.OrgMembership{UserId: userID, Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
	}
	return nil, nil
}
