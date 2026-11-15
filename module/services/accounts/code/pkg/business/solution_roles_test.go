package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

func scope(kind string, actions ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// An example solution declaration: its own page kind under a ceiling, and the
// collection kind it consumes through its documents binding.
func exampleSolutionAuthority() *SolutionAuthority {
	return &SolutionAuthority{
		Principal: "example", Kinds: []string{"example.page"}, Ceilings: map[string][]string{"example.page": {"read", "write", "publish"}},
		Consumed: []*basev0.WorkScopeV1{scope("documents.collection", "read")},
		Roles: map[string][]*basev0.WorkScopeV1{
			domain.AdminRole: {scope("example.page", "read", "write", "publish", "configure"), scope("documents.collection", "read", "admin")},
			domain.UserRole:  {scope("example.page", "read"), scope("documents.collection", "read")},
		},
	}
}

// A role grants within what the solution may grant: the ceiling on its own
// kinds and its bindings on the kinds it consumes; a kind it neither serves
// nor consumes is granted nowhere.
func TestASolutionRoleIsBoundedByItsCeilingAndItsBindings(t *testing.T) {
	a := exampleSolutionAuthority()
	a.Roles["stray"] = []*basev0.WorkScopeV1{scope("other.tasks", "execute")}
	require.ElementsMatch(t, []*basev0.WorkScopeV1{scope("example.page", "read", "write", "publish"), scope("documents.collection", "read")}, a.Grants(domain.AdminRole))
	require.Empty(t, a.Grants("stray"))
	require.Empty(t, a.Grants("undeclared"))
	require.Equal(t, []*basev0.WorkScopeV1{scope("example.page", "read")}, a.Own(a.Grants(domain.UserRole)))
}

// The owner holds admin; a member of an exposed team holds the installation's
// role; anyone else holds nothing.
func TestAPersonHoldsASolutionRoleAtItsInstallation(t *testing.T) {
	a := exampleSolutionAuthority()
	at := SolutionRoleHolders{Owner: "alex", Role: domain.UserRole, Teams: []string{"finance", "engineering"}}
	require.Equal(t, a.Grants(domain.AdminRole), a.HeldBy("alex", nil, at))
	require.Equal(t, a.Grants(domain.UserRole), a.HeldBy("maya", []string{"finance"}, at))
	require.Empty(t, a.HeldBy("omar", []string{"ops"}, at))
	require.Equal(t, domain.UserRole, SolutionRoleOf(solutionRoleNamePrefix+"org=org;solution=target;role="+domain.UserRole))
	require.Equal(t, "Module viewer", SolutionRoleOf("Module viewer"))
}

// STOPGAP (one release, with the core's domain.LegacyInstallerRole): a
// delivery stored before the rename declares the admin role as "installer",
// and its owner holds it still.
func TestTheOwnerHoldsALegacyInstallerDeclarationAsAdmin(t *testing.T) {
	a := exampleSolutionAuthority()
	a.Roles[domain.LegacyInstallerRole] = a.Roles[domain.AdminRole]
	delete(a.Roles, domain.AdminRole)
	require.Equal(t, a.Grants(domain.LegacyInstallerRole), a.HeldBy("alex", nil, SolutionRoleHolders{Owner: "alex"}))
	require.NotEmpty(t, a.HeldBy("alex", nil, SolutionRoleHolders{Owner: "alex"}))
}
