package moduleregistrytest

import (
	"accounts/pkg/business"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRegistryFixtureSharedTenancyAndMalformedDedicatedTenant(t *testing.T) {
	shared, err := Parse(`{"example":{"tenant":"","workload":{"namespace":"acme-prod","service_account":"example","container":"app"}}}`)
	require.NoError(t, err)
	require.Empty(t, shared[business.ModulePrincipalID("example")].Tenant)
	require.False(t, shared[business.ModulePrincipalID("example")].CrossTenant)
	_, err = Parse(`{"example":{"tenant":"not-an-organization","workload":{"namespace":"acme-prod","service_account":"example","container":"app"}}}`)
	require.Error(t, err)
}
