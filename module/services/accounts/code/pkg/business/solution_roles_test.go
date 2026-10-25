package business

import (
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

func scope(kind string, actions ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// A wiki-shaped declaration: its own page kind under a ceiling, and the
// collection kind it consumes through its documents binding.
func wikiAuthority() *SolutionAuthority {
	return &SolutionAuthority{
		Principal: "wiki", Kinds: []string{"wiki.page"}, Ceilings: map[string][]string{"wiki.page": {"read", "write", "publish"}},
		Consumed: []*basev0.WorkScopeV1{scope("documents.collection", "read")},
		Roles: map[string][]*basev0.WorkScopeV1{
			InstallerRole: {scope("wiki.page", "read", "write", "publish", "configure"), scope("documents.collection", "read", "admin")},
			"editor":      {scope("wiki.page", "read", "write"), scope("documents.collection", "read")},
			"viewer":      {scope("wiki.page", "read"), scope("documents.collection", "read")},
		},
	}
}

// A role grants within what the solution may grant: the ceiling on its own
// kinds and its bindings on the kinds it consumes; a kind it neither serves
// nor consumes is granted nowhere.
func TestASolutionRoleIsBoundedByItsCeilingAndItsBindings(t *testing.T) {
	a := wikiAuthority()
	a.Roles["stray"] = []*basev0.WorkScopeV1{scope("robin.tasks", "execute")}
	require.ElementsMatch(t, []*basev0.WorkScopeV1{scope("wiki.page", "read", "write", "publish"), scope("documents.collection", "read")}, a.Grants(InstallerRole))
	require.Empty(t, a.Grants("stray"))
	require.Empty(t, a.Grants("undeclared"))
	require.Equal(t, []*basev0.WorkScopeV1{scope("wiki.page", "read")}, a.Own(a.Grants("viewer")))
}

// The owner holds installer; a member of an exposed team holds the
// installation's role; anyone else holds nothing.
func TestAPersonHoldsASolutionRoleAtItsInstallation(t *testing.T) {
	a := wikiAuthority()
	at := SolutionRoleHolders{Owner: "alex", Role: "viewer", Teams: []string{"finance", "engineering"}}
	require.Equal(t, a.Grants(InstallerRole), a.HeldBy("alex", nil, at))
	require.Equal(t, a.Grants("viewer"), a.HeldBy("maya", []string{"finance"}, at))
	require.Empty(t, a.HeldBy("omar", []string{"ops"}, at))
	require.Equal(t, "viewer", SolutionRoleOf(solutionRoleNamePrefix+solutionRoleProvenance("org", "target", "viewer")))
	require.Equal(t, "Module viewer", SolutionRoleOf("Module viewer"))
}
