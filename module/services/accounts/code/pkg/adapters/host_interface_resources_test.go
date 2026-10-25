package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"accounts/pkg/auth"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// scopedAuthority is holdingAuthority whose owner also holds grants limited to
// named resources, by kind:action.
type scopedAuthority struct {
	*holdingAuthority
	scoped map[string][]string
}

func (f scopedAuthority) WorkContextScopedResourceIDs(_ context.Context, _, _, kind, action string) ([]string, error) {
	return f.scoped[kind+":"+action], nil
}

// resourceFixture is the hop fixture's issuer over an owner holding held
// organisation-wide and scoped resource by resource.
func resourceFixture(t *testing.T, scoped map[string][]string, held ...string) hopFixture {
	t.Helper()
	f := newHopFixture(t, held...)
	f.issuer.authority = scopedAuthority{holdingAuthority: f.holding, scoped: scoped}
	return f
}

func (f hopFixture) resources(t *testing.T, token, kind, action string) ([]*authorityv1.ResourceGrant, error) {
	t.Helper()
	out, err := f.server.ListResources(context.Background(), &authorityv1.ListResourcesRequest{Token: token, Audience: "documents", ResourceKind: kind, Action: action})
	return out.GetGrants(), err
}

func grantIDs(grants []*authorityv1.ResourceGrant) []string {
	var out []string
	for _, g := range grants {
		if g.GetAll() {
			out = append(out, "*")
		} else {
			out = append(out, g.GetId())
		}
	}
	return out
}

// A person's capability on a kind is answered from their grants now: every
// resource for an organisation-wide grant, the ids of resource-limited ones
// otherwise; a capability naming ids covers only those the grants cover; an
// action the capability does not hold is refused. Asking spends nothing.
func TestListResourcesAnswersFromTheOwnersGrants(t *testing.T) {
	read := []string{"read"}
	wide := resourceFixture(t, nil, "example-records:read")
	token, _ := wide.parent(t, "documents", hopScope("example-records", read))
	grants, err := wide.resources(t, token, "example-records", "read")
	require.NoError(t, err)
	require.Equal(t, []string{"*"}, grantIDs(grants))

	limited := resourceFixture(t, map[string][]string{"example-records:read": {"a", "b"}})
	token, _ = mintTestCapability(t, limited.issuer, testCapability{Audience: "documents", Tenant: moduleWorkContextTenant, Owner: hopOwner,
		Replay: workcontext.ReplaySingleUse, Scopes: []*basev0.WorkScopeV1{hopScope("example-records", read)}})
	grants, err = limited.resources(t, token, "example-records", "read")
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, grantIDs(grants))
	_, err = limited.issuer.verifyFor(context.Background(), token, "documents")
	require.NoError(t, err, "a question does not spend a single-use capability")

	named, _ := limited.parent(t, "documents", hopScope("example-records", read, "a", "c"))
	grants, err = limited.resources(t, named, "example-records", "read")
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, grantIDs(grants))

	_, err = limited.resources(t, named, "example-records", "write")
	requireRefused(t, err, codes.PermissionDenied, "the capability holds no example-records:write")
}

// Where a solution's roles decide, the answer is what those roles grant.
func TestListResourcesAnswersASolutionRolesGrants(t *testing.T) {
	f := resourceFixture(t, nil, "example-records:read")
	f.server.roles = fixedSolutionRoles{"documents": {kinds: []string{"example-records"}, held: []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"}, "x")}}}
	token, _ := f.parent(t, "documents", hopScope("example-records", []string{"read"}))
	grants, err := f.resources(t, token, "example-records", "read")
	require.NoError(t, err)
	require.Equal(t, []string{"x"}, grantIDs(grants))
}

// An empty ask carries a kind the person holds only on named resources, on
// exactly those resources; a person holding nothing there is refused by name.
func TestAnEmptyAskCarriesResourceLimitedGrantsOnTheirResources(t *testing.T) {
	f := resourceFixture(t, map[string][]string{"example-records:read": {"a", "b"}})
	withHopMember(t)
	caller := stampVerifiedIdentity(context.Background(), hopOwner, moduleWorkContextTenant, auth.Assurance{})
	held, err := f.issuer.personHoldings(caller, moduleWorkContextTenant, "documents")
	require.NoError(t, err)
	require.Len(t, held, 1)
	require.Equal(t, "example-records", held[0].GetResourceKind())
	require.Equal(t, []string{"read"}, held[0].GetActions())
	require.Equal(t, []string{"a", "b"}, held[0].GetResourceIds())

	none := resourceFixture(t, nil)
	withHopMember(t)
	_, err = none.issuer.personHoldings(caller, moduleWorkContextTenant, "documents")
	requireRefused(t, err, codes.PermissionDenied, "the owner holds nothing at this audience")
}
