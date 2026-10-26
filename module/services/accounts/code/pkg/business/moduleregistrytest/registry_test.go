package moduleregistrytest

import (
	"accounts/pkg/business"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryFixtureSharedTenancyAndMalformedDedicatedTenant(t *testing.T) {
	shared, err := Parse(`{"example":{"tenant":"","workload":{"namespace":"acme-prod","service_account":"example","container":"app"}}}`)
	require.NoError(t, err)
	require.Empty(t, shared[business.ModulePrincipalID("example")].Tenant)
	require.False(t, shared[business.ModulePrincipalID("example")].CrossTenant)
	_, err = Parse(`{"example":{"tenant":"not-an-organization","workload":{"namespace":"acme-prod","service_account":"example","container":"app"}}}`)
	require.Error(t, err)
}

// A module is named by its name in every capability it exercises, beside the
// principal ids those capabilities carry: the two resolve to one module each
// and never to each other.
func TestRegistryResolvesAModuleByItsNameOrItsRegistryID(t *testing.T) {
	registry, err := Parse(`{` +
		`"tools":{"workload":{"namespace":"acme-prod","service_account":"tools","container":"app"}},` +
		`"documents":{"workload":{"namespace":"acme-prod","service_account":"documents","container":"app"}}}`)
	require.NoError(t, err)
	tools := business.ModulePrincipalID("tools")

	for principal, want := range map[string]string{"tools": tools, tools: tools, "documents": business.ModulePrincipalID("documents")} {
		got, ok := registry.RegistryIDOf(principal)
		require.True(t, ok, principal)
		require.Equal(t, want, got, principal)
	}
	for _, principal := range []string{"", "Tools", "tools ", "urn:uuid:" + tools, "00000000-0000-4000-8000-000000000001"} {
		_, ok := registry.RegistryIDOf(principal)
		require.False(t, ok, "%q resolved to a module", principal)
	}

	// An entry whose name is not the one it is keyed under answers for no name.
	stray := registry[tools]
	stray.Prefix = "documents"
	registry[tools] = stray
	_, ok := registry.RegistryIDOf("tools")
	require.False(t, ok)
}

// A name spelled as an id would collide with the principal ids capabilities
// carry, so no registry holds one.
func TestAModuleNamedAsAnIDIsRefused(t *testing.T) {
	_, err := Parse(`{"8d4a32d7-50a7-5009-a69d-71bf529b22a2":{"workload":{"namespace":"acme-prod","service_account":"x","container":"app"}}}`)
	require.ErrorContains(t, err, "named as an id")
}
