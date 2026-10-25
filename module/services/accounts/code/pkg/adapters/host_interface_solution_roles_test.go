package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
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
