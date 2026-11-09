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
