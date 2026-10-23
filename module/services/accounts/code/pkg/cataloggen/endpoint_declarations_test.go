package cataloggen_test

import (
	"testing"

	"accounts/pkg/cataloggen"

	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

func TestDeploymentTopologyAcceptsConsumerDerivedInternalReach(t *testing.T) {
	documents := readDeploymentDocuments(t)
	artifacts, err := cataloggen.BuildDeploymentArtifacts(readFixture(t, "../../../generated/service-catalog.json"), documents)
	require.NoError(t, err)
	// The reach category does not grant callers. The policies are still the
	// exact dependency edges, and internal RPCs still require caller identity.
	require.Equal(t, string(readFixture(t, "testdata/network-policy.golden.yaml")), string(artifacts.NetworkPolicy))
	require.Equal(t, string(readFixture(t, "testdata/mesh-policy.golden.yaml")), string(artifacts.MeshPolicy))
	// Core defaults an interface export to internal, while a service endpoint
	// still defaults to private. Omitting the interface reach keeps its policy.
	defaultExport := withModule(t, documents, "          visibility: internal\n", "")
	defaults, err := cataloggen.BuildDeploymentArtifacts(readFixture(t, "../../../generated/service-catalog.json"), defaultExport)
	require.NoError(t, err)
	require.Equal(t, string(artifacts.CatalogJSON), string(defaults.CatalogJSON))
}

func TestDeploymentTopologyRefusesAuthoredConsumersByKeyPresence(t *testing.T) {
	documents := readDeploymentDocuments(t)
	catalog := readFixture(t, "../../../generated/service-catalog.json")
	for _, key := range []string{"allow-modules", "allow_modules", "allowModules", "Allow Modules"} {
		for _, value := range []string{"[\"*\"]", "[example]", "[]", "null"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				_, err := cataloggen.BuildDeploymentArtifacts(catalog, withModule(t, documents,
					"          visibility: internal\n", "          visibility: internal\n          "+key+": "+value+"\n"))
				require.ErrorIs(t, err, resources.ErrInvalidEndpointDeclaration)
				_, err = cataloggen.BuildDeploymentArtifacts(catalog, withService(t, documents, "accounts",
					"      visibility: internal\n", "      visibility: internal\n      "+key+": "+value+"\n"))
				require.ErrorIs(t, err, resources.ErrInvalidEndpointDeclaration)
			})
		}
	}
}
