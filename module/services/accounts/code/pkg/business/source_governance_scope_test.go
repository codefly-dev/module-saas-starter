package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

func governingScope(kind string, actions ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// A request names its source's governing scope, so the host must not let it
// name one its caller already holds through a lesser role: a viewer naming
// the read every viewer holds would make every viewer the source's manager.
func TestAGoverningScopeIsOneTheTargetsFullestRolesAloneHold(t *testing.T) {
	target := domain.Module{Name: "example-solution", Kind: domain.KindSolution, Roles: map[string][]*basev0.WorkScopeV1{
		"installer": {governingScope("example.page", "read")},
		"viewer":    {governingScope("example.page", "read")},
		"commenter": {governingScope("example.page", "read", "comment")},
		"editor":    {governingScope("example.page", "read", "comment", "write"), governingScope("example-records", "write")},
	}}
	require.NoError(t, governingScopeDeclared(target, "example.page", "write"), "the editors' write governs")
	require.NoError(t, governingScopeDeclared(target, "example-records", "write"), "so does a scope the editors alone reach through a binding")

	err := governingScopeDeclared(target, "example.page", "read")
	require.ErrorContains(t, err, "example.page:read without everything example-solution's roles grant", "a viewer may not name the read every viewer holds")
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, domain.Denied, code)
	require.ErrorContains(t, governingScopeDeclared(target, "example.page", "comment"), "role commenter is granted example.page:comment", "nor a commenter the comment a lesser role holds")
	require.ErrorContains(t, governingScopeDeclared(target, "example.page", "delete"), "no role of example-solution is granted example.page:delete")

	owned := domain.Module{Name: "example-page", Roles: map[string][]*basev0.WorkScopeV1{"reader": {governingScope("example.page", "read")}},
		OwnedRoles: map[string][]*basev0.WorkScopeV1{"owner": {governingScope("example.page", "read", "write")}}}
	require.NoError(t, governingScopeDeclared(owned, "example.page", "write"), "an owned role is a role")
	require.Error(t, governingScopeDeclared(owned, "example.page", "read"))

	partial := domain.Module{Name: "example-split", Roles: map[string][]*basev0.WorkScopeV1{
		"pages":   {governingScope("example.page", "write")},
		"records": {governingScope("example-records", "write")},
	}}
	require.Error(t, governingScopeDeclared(partial, "example.page", "write"), "roles that are all partial govern no source")
}

// A fullest role governs only beside a lesser one it strictly contains. A
// target with a single role, or with roles that are all equal, has none:
// every holder would count as its "fullest", the readers among them, so such
// a target governs no source and its sources stay the organization
// administrators'.
func TestATargetWithNoRoleFullerThanAnotherGovernsNoSource(t *testing.T) {
	single := domain.Module{Name: "example-single", Kind: domain.KindSolution, Roles: map[string][]*basev0.WorkScopeV1{
		"member": {governingScope("example.page", "read", "write")},
	}}
	for _, action := range []string{"read", "write"} {
		require.ErrorContains(t, governingScopeDeclared(single, "example.page", action), "no role of example-single is fuller than another", "a single role governs nothing: %s", action)
	}
	equal := domain.Module{Name: "example-equal", Kind: domain.KindSolution, Roles: map[string][]*basev0.WorkScopeV1{
		"viewer": {governingScope("example.page", "read")},
		"reader": {governingScope("example.page", "read")},
	}}
	require.ErrorContains(t, governingScopeDeclared(equal, "example.page", "read"), "no role of example-equal is fuller than another", "equal roles govern nothing")
	owned := domain.Module{Name: "example-owned-equal", Roles: map[string][]*basev0.WorkScopeV1{"reader": {governingScope("example.page", "read")}},
		OwnedRoles: map[string][]*basev0.WorkScopeV1{"owner": {governingScope("example.page", "read")}}}
	require.Error(t, governingScopeDeclared(owned, "example.page", "read"), "an owned role equal to a role is no fuller")
	roleless := domain.Module{Name: "example-roleless", Kind: domain.KindSolution}
	require.Error(t, governingScopeDeclared(roleless, "example.page", "read"), "a target declaring no roles governs nothing")
}

// Only an organization administrator's governed connect may read through the
// organization's own credentials; anyone else's brings a token of their own.
func TestAGovernedConnectBringsItsOwnCredentialUnlessAnAdministratorMakesIt(t *testing.T) {
	g := &SourceGovernance{InstallationID: "inst-1", Kind: "example.page", Action: "write"}
	require.NoError(t, governedCredential(nil, false, ""), "an organization's own source is decided by the organization rule")
	require.NoError(t, governedCredential(g, true, ""), "an administrator may attach the organization's credentials")
	require.NoError(t, governedCredential(g, false, "a-token"), "a member brings their own token")
	require.NoError(t, governedCredential(g, false, "  a-token  "))
	require.ErrorContains(t, governedCredential(g, false, ""), "with their own repository-scoped token")
	require.ErrorContains(t, governedCredential(g, false, "   "), "with their own repository-scoped token")
}
