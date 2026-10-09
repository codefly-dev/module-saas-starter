package cataloggen_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/codefly-dev/core/composition"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"accounts/pkg/business"
	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

func TestAPIContractSurfacesAreDeterministicCurrentAndListenerScoped(t *testing.T) {
	catalogDocument := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../adapters/connect_bindings.yaml")
	first, err := cataloggen.RenderAPIContractSurfaces(catalogDocument, bindings)
	require.NoError(t, err)
	second, err := cataloggen.RenderAPIContractSurfaces(catalogDocument, bindings)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, string(first), string(readFixture(t, "../../../generated/api-contract-surfaces.json")), "run: go generate ./pkg/adapters")

	var surfaces map[string][]composition.APIContractService
	require.NoError(t, json.Unmarshal(first, &surfaces))
	require.Len(t, surfaces, 2)
	catalog, err := business.BuildServiceCatalog()
	require.NoError(t, err)
	var expectedConnect []composition.APIContractService
	var connectProcedures []string
	for _, service := range catalog.GetServices() {
		connectProcedures = append(connectProcedures, service.GetProcedures()...)
		expectedConnect = append(expectedConnect, composition.APIContractService{
			Name: service.GetName(), FullName: service.GetFullName(), Procedures: service.GetProcedures(),
		})
	}
	require.Equal(t, expectedConnect, surfaces["connect"], "include every registered procedure, including internal methods omitted by the gateway catalog")
	var authority []string
	for _, service := range surfaces[business.ModuleAuthorityEndpoint] {
		require.NotEmpty(t, service.Procedures)
		for _, procedure := range service.Procedures {
			require.True(t, strings.HasPrefix(procedure, "/"+service.FullName+"/"))
			authority = append(authority, procedure)
		}
	}
	require.Equal(t, business.ModuleAuthorityProcedures(), authority)
	for _, method := range []string{"InvokeSourceOperation", "LookupInvokeSourceOperation", "PruneSourceOperationReceipts", "LookupPruneSourceOperationReceipts"} {
		procedure := "/saas.accounts.v1.DatasourceService/" + method
		require.NotContains(t, authority, procedure)
		require.Contains(t, connectProcedures, procedure)
	}
}

func TestAPIContractSurfacesRejectIncompleteRegistration(t *testing.T) {
	catalogDocument := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../adapters/connect_bindings.yaml")
	_, err := cataloggen.RenderAPIContractSurfaces(catalogDocument, []byte(strings.Replace(string(bindings), "name: APIKey", "name: APIKey()", 1)))
	require.ErrorContains(t, err, "invalid grpc source name")
	_, err = cataloggen.RenderAPIContractSurfaces(catalogDocument, []byte("version: v1\nservices: {}\n"))
	require.ErrorContains(t, err, "no services in Connect bindings")

	// A valid but incomplete catalog must not silently shrink authority's
	// published surface while the listener still serves the missing procedure.
	catalog := new(catalogv1.ServiceCatalog)
	require.NoError(t, protojson.Unmarshal(catalogDocument, catalog))
	missing := business.ModuleAuthorityProcedures()[0]
	catalog.Methods = slices.DeleteFunc(catalog.Methods, func(method *catalogv1.Method) bool {
		return method.GetProcedure() == missing
	})
	for _, service := range catalog.Services {
		service.Procedures = slices.DeleteFunc(service.Procedures, func(procedure string) bool { return procedure == missing })
	}
	require.NoError(t, business.ValidateServiceCatalog(catalog))
	incomplete, err := protojson.Marshal(catalog)
	require.NoError(t, err)
	_, err = cataloggen.RenderAPIContractSurfaces(incomplete, bindings)
	require.ErrorContains(t, err, "module authority procedure "+missing+" is absent from the Connect registration catalog")
}
