package cataloggen_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/cataloggen"
)

func TestConnectRegistrationIsDeterministicAndCurrent(t *testing.T) {
	catalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../adapters/connect_bindings.yaml")

	first, err := cataloggen.RenderConnectRegistration(catalog, bindings)
	require.NoError(t, err)
	second, err := cataloggen.RenderConnectRegistration(catalog, bindings)
	require.NoError(t, err)
	require.Equal(t, first, second)

	checkedIn := readFixture(t, "../adapters/connect_registration_catalog_gen.go")
	require.Equal(t, string(first), string(checkedIn), "run: go generate ./pkg/adapters")
	require.Equal(t, 25, strings.Count(string(first), "genconnect.New"))
	require.Equal(t, 25, strings.Count(string(first), ")(nil)"))
	require.Contains(t, string(first), "&apiKeyConnectHandler{inner: server.grpc.APIKey}")
	require.Contains(t, string(first), "&webhookConnectHandler{svc: server.service}")
	require.Contains(t, string(first), "&delegationConnectHandler{inner: DelegationSingleton()}")
}

func TestGRPCRegistrationIsDeterministicAndCurrent(t *testing.T) {
	catalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../adapters/connect_bindings.yaml")

	first, err := cataloggen.RenderGRPCRegistration(catalog, bindings)
	require.NoError(t, err)
	second, err := cataloggen.RenderGRPCRegistration(catalog, bindings)
	require.NoError(t, err)
	require.Equal(t, first, second)

	checkedIn := readFixture(t, "../adapters/grpc_registration_catalog_gen.go")
	require.Equal(t, string(first), string(checkedIn), "run: go generate ./pkg/adapters")
	require.Equal(t, 16, strings.Count(string(first), "gen.Register"))
	require.Contains(t, string(first), "gen.RegisterAPIKeyServiceServer(registrar, server.APIKey)")
	require.Contains(t, string(first), "gen.RegisterDelegationServiceServer(registrar, DelegationSingleton())")
	require.Contains(t, string(first), "gen.RegisterMFAServiceServer(registrar, server.MFA)")
	require.Contains(t, string(first), `"saas.accounts.v1.BillingService"`)
}

func TestConnectRegistrationRejectsUnsafeOrIncompleteBindings(t *testing.T) {
	catalog := readFixture(t, "../../../generated/service-catalog.json")

	_, err := cataloggen.RenderConnectRegistration(catalog, []byte(`
version: v1
services:
  APIKeyService:
    handler: apiKeyConnectHandler
    source:
      kind: grpc
      name: APIKey
`))
	require.ErrorContains(t, err, "missing catalog service")

	bindings := string(readFixture(t, "../adapters/connect_bindings.yaml"))
	unsafeExpression := strings.Replace(bindings, "name: APIKey", "name: APIKey()", 1)
	_, err = cataloggen.RenderConnectRegistration(catalog, []byte(unsafeExpression))
	require.ErrorContains(t, err, "invalid grpc source name")

	unknownField := strings.Replace(bindings, "handler: apiKeyConnectHandler", "handler: apiKeyConnectHandler\n    expression: arbitrary", 1)
	_, err = cataloggen.RenderConnectRegistration(catalog, []byte(unknownField))
	require.ErrorContains(t, err, "field expression not found")

	unsafeGRPCServer := strings.Replace(bindings, "grpc_server: MFA", "grpc_server: MFA()", 1)
	_, err = cataloggen.RenderGRPCRegistration(catalog, []byte(unsafeGRPCServer))
	require.ErrorContains(t, err, "invalid grpc_server")

	extraService := bindings + `
  UnknownService:
    handler: unknownConnectHandler
    source:
      kind: business
`
	_, err = cataloggen.RenderConnectRegistration(catalog, []byte(extraService))
	require.ErrorContains(t, err, "absent from the catalog")
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	document, err := os.ReadFile(path)
	require.NoError(t, err)
	return document
}
