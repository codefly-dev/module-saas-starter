package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A following scope holds the grant as it moves; a written scope narrows as
// before, and an axis it leaves out is none of that axis.
func TestAFollowingScopeHoldsTheGrantAsItMoves(t *testing.T) {
	grant := ModulePrincipalGrant{
		Prefix: "m", Queues: []string{"q"}, Namespaces: []string{"n"}, Resources: []string{"r"},
		OperationAudiences: map[string]ModuleOperationAudience{"b": {Audience: "other", InvokeScopes: []ModuleOperationScope{{ResourceKind: "other.things", Actions: []string{"read"}}}}},
	}
	followed := intersectInstallationScope(grant, InstallationScope{Capabilities: true, Follows: true})
	require.Equal(t, grant, followed)

	frozen := intersectInstallationScope(grant, InstallationScope{Capabilities: true, Queues: []string{"q"}})
	require.Equal(t, []string{"q"}, frozen.Queues)
	require.Empty(t, frozen.OperationAudiences, "a written scope that names no binding consents to none")

	full := FullInstallationScope(grant)
	require.True(t, InstallationScopeWithin(InstallationScope{Capabilities: true, Follows: true}, full))
	require.True(t, InstallationScopeWithin(InstallationScope{Capabilities: true, Queues: []string{"q"}}, ResolveInstallationScope(InstallationScope{Capabilities: true, Follows: true}, full)),
		"a narrowing is checked against what a following installation holds today")
}
