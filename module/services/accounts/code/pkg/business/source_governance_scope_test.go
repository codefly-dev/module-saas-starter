package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev1 "github.com/codefly-dev/core/generated/go/codefly/base/v1"
	"github.com/stretchr/testify/require"
)

func governingScope(kind string, actions ...string) *basev1.WorkScopeV1 {
	return &basev1.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// The request never picks the governing scope: the host admits only one the
// target's approved declaration names. A user naming the read every user
// holds, or an admin naming a scope the author did not declare, is refused.
func TestAGoverningScopeIsOneTheApprovedDeclarationNames(t *testing.T) {
	target := domain.Module{Name: "example-solution", Kind: domain.KindSolution, Roles: map[string][]*basev1.WorkScopeV1{
		domain.AdminRole: {governingScope("example.page", "read", "write", "publish")},
		domain.UserRole:  {governingScope("example.page", "read")},
	}, SourceGovernance: []*basev1.WorkScopeV1{governingScope("example.page", "write")}}
	asked := func(kind, action, binding string) SourceGovernance {
		return SourceGovernance{InstallationID: "inst-1", Kind: kind, Action: action, Binding: binding}
	}
	require.NoError(t, governingScopeDeclared(target, asked("example.page", "write", "")), "the declared write governs")

	err := governingScopeDeclared(target, asked("example.page", "read", ""))
	require.ErrorContains(t, err, "does not declare example.page:read as governing its sources; it declares example.page:write", "a user may not name the read every user holds")
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, domain.Denied, code)
	require.ErrorContains(t, governingScopeDeclared(target, asked("example.page", "publish", "")), "does not declare example.page:publish", "nor the admin a scope no declaration names")
	require.ErrorContains(t, governingScopeDeclared(target, asked("documents", "write", "")), "does not declare documents:write")
	require.ErrorContains(t, governingScopeDeclared(target, asked("example.page", "write", "documents")), "never through binding documents",
		"a declared scope is one of the target's own kinds, so a request naming a binding governs by nothing declared")
}

// A target that declares no governing scope governs no source, whatever its
// roles look like: its sources stay the organization administrators'.
func TestATargetDeclaringNoGoverningScopeGovernsNoSource(t *testing.T) {
	for name, target := range map[string]domain.Module{
		"a solution with a fuller admin": {Name: "example-undeclared", Kind: domain.KindSolution, Roles: map[string][]*basev1.WorkScopeV1{
			domain.AdminRole: {governingScope("example.page", "read", "write")},
			domain.UserRole:  {governingScope("example.page", "read")},
		}},
		"a single role": {Name: "example-single", Kind: domain.KindSolution, Roles: map[string][]*basev1.WorkScopeV1{
			domain.AdminRole: {governingScope("example.page", "read", "write")},
		}},
	} {
		for _, action := range []string{"read", "write"} {
			require.ErrorContains(t, governingScopeDeclared(target, SourceGovernance{InstallationID: "inst-1", Kind: "example.page", Action: action}),
				"declares no scope that governs its sources, so they stay the organization administrators'", "%s: %s", name, action)
		}
	}
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
