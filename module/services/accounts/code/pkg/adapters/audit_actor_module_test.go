package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// A module acting on its own is recorded under its principal, which no
// directory row names; the audit read names it by the prefix the registry
// declares it under, and names no other actor.
func TestAuditActorModuleNamesOnlyADeclaredModulePrincipal(t *testing.T) {
	modules := business.ModulePrincipalRegistry{
		business.ModulePrincipalID("example-chat"): {Prefix: "example-chat"},
	}
	require.Equal(t, "example-chat", auditActorModule(modules, business.ModulePrincipalID("example-chat")))
	// A person, or a module this host no longer declares, is not a module here.
	require.Empty(t, auditActorModule(modules, "11111111-1111-4111-8111-111111111111"))
	require.Empty(t, auditActorModule(modules, business.ModulePrincipalID("example-withdrawn")))
	require.Empty(t, auditActorModule(modules, ""))
}
