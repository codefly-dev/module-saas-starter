package cataloggen_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

func TestRESTSurfaceCompilationAndExposure(t *testing.T) {
	surface, err := cataloggen.BuildRESTSurfaceCatalog(readFixture(t, "../../../generated/gateway-routes.json"))
	require.NoError(t, err)
	require.NoError(t, cataloggen.ValidateRESTSurfaceCatalog(surface))
	require.Equal(t, "saas.rest.surface.v1", surface.GetSchemaVersion())
	require.Equal(t, "accounts", surface.GetOwner().GetService())
	require.Len(t, surface.GetRoutes(), 115)

	publicCount := 0
	services := make(map[string]struct{})
	routes := make(map[string]*catalogv1.GatewayRoute, len(surface.GetRoutes()))
	for _, route := range surface.GetRoutes() {
		routes[route.GetMethod()+" "+route.GetPath()] = route
		serviceName := strings.Split(strings.TrimPrefix(route.GetProcedure(), "/saas.accounts.v1."), "/")[0]
		services[serviceName] = struct{}{}
		if route.GetExposure() == policyv1.Exposure_EXPOSURE_PUBLIC {
			publicCount++
		}
	}
	require.Equal(t, 11, publicCount)
	require.Len(t, services, 23)
	require.Nil(t, routes["POST /v1/permissions:check"])
	require.Nil(t, routes["POST /v1/api-keys:validate"])
	require.Equal(t, "/saas.accounts.v1.UserService/RegisterUser", routes["POST /v1/users"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.AuditExportService/SaveConfig", routes["POST /v1/audit-export"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.PlatformAdminService/GetJobOperations", routes["GET /v1/platform/jobs/operations"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.PlatformAdminService/ReplayJob", routes["POST /v1/platform/jobs/{source_job_id}:replay"].GetProcedure())
}

func TestRESTSurfaceArtifactsAreDeterministicAndCurrent(t *testing.T) {
	serviceDocument := readFixture(t, "../../../generated/service-catalog.json")
	gatewayDocument := readFixture(t, "../../../generated/gateway-routes.json")
	bindingDocument := readFixture(t, "../adapters/rest_bindings.yaml")
	rawOpenAPI := readFixture(t, "../../../generated/openapi-raw/api.swagger.json")
	surface, err := cataloggen.BuildRESTSurfaceCatalog(gatewayDocument)
	require.NoError(t, err)

	first, err := cataloggen.RenderRESTSurfaceCatalogJSON(surface)
	require.NoError(t, err)
	second, err := cataloggen.RenderRESTSurfaceCatalogJSON(surface)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, string(readFixture(t, "../../../generated/rest-surface.json")), string(first), "run: go generate ./pkg/cataloggen")

	parsed := &catalogv1.RESTSurfaceCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(first, parsed))
	require.True(t, proto.Equal(surface, parsed))

	service := &catalogv1.ServiceCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, service))
	accountsRuntime, err := cataloggen.RenderAccountsRESTRuntime(surface, service, bindingDocument)
	require.NoError(t, err)
	require.Equal(t, string(readFixture(t, "../adapters/rest_registration_catalog_gen.go")), string(accountsRuntime), "run: go generate ./pkg/cataloggen")
	require.Equal(t, 21, strings.Count(string(accountsRuntime), "gen.Register"))
	require.Contains(t, string(accountsRuntime), `case "permissions":`)

	sidecarRuntime, err := cataloggen.RenderAuthSidecarRESTRoutes(surface)
	require.NoError(t, err)
	require.Equal(t, string(readFixture(t, "../../../../auth-sidecar/code/routing_rest_catalog_gen.go")), string(sidecarRuntime), "run: go generate ./pkg/cataloggen")
	require.Equal(t, 115, strings.Count(string(sidecarRuntime), `{Service: "accounts"`))

	publicOpenAPI, err := cataloggen.RenderPublicOpenAPI(rawOpenAPI, surface, service)
	require.NoError(t, err)
	require.Equal(t, string(readFixture(t, "../../../openapi/api.swagger.json")), string(publicOpenAPI), "run: go generate ./pkg/cataloggen")
	secondOpenAPI, err := cataloggen.RenderPublicOpenAPI(publicOpenAPI, surface, service)
	require.NoError(t, err)
	require.Equal(t, publicOpenAPI, secondOpenAPI)

	var raw, public map[string]any
	require.NoError(t, json.Unmarshal(rawOpenAPI, &raw))
	require.NoError(t, json.Unmarshal(publicOpenAPI, &public))
	require.Equal(t, 115, openAPIOperationCount(t, raw))
	require.Equal(t, 115, openAPIOperationCount(t, public))
	require.Equal(t, "saas.rest.surface.v1", public["x-codefly-rest-schema"])
	publicPaths := public["paths"].(map[string]any)
	require.NotContains(t, publicPaths, "/v1/permissions:check")
	require.NotContains(t, publicPaths, "/v1/identity:resolve")
}

func TestRESTSurfaceRejectsUnsafeDriftAndBindings(t *testing.T) {
	surface, err := cataloggen.BuildRESTSurfaceCatalog(readFixture(t, "../../../generated/gateway-routes.json"))
	require.NoError(t, err)
	service := &catalogv1.ServiceCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(readFixture(t, "../../../generated/service-catalog.json"), service))
	bindings := string(readFixture(t, "../adapters/rest_bindings.yaml"))

	internal := proto.Clone(surface).(*catalogv1.RESTSurfaceCatalog)
	internal.Routes[0].Exposure = policyv1.Exposure_EXPOSURE_INTERNAL
	require.ErrorContains(t, cataloggen.ValidateRESTSurfaceCatalog(internal), "exposure")

	alias := proto.Clone(surface).(*catalogv1.RESTSurfaceCatalog)
	alias.Routes[0].Source = catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_COMPATIBILITY_ALIAS
	require.ErrorContains(t, cataloggen.ValidateRESTSurfaceCatalog(alias), "non-descriptor")

	missing := strings.Replace(bindings, "  WebhookService:\n    kind: generated\n", "", 1)
	_, err = cataloggen.RenderAccountsRESTRuntime(surface, service, []byte(missing))
	require.ErrorContains(t, err, "missing surface service")

	unsafe := strings.Replace(bindings, "kind: plugin\n    plugin: permissions", "kind: expression\n    plugin: arbitrary()", 1)
	_, err = cataloggen.RenderAccountsRESTRuntime(surface, service, []byte(unsafe))
	require.ErrorContains(t, err, "unsupported kind")

	unknownField := bindings + "unknown: true\n"
	_, err = cataloggen.RenderAccountsRESTRuntime(surface, service, []byte(unknownField))
	require.ErrorContains(t, err, "field unknown not found")

	reduced := proto.Clone(surface).(*catalogv1.RESTSurfaceCatalog)
	reduced.Routes = reduced.Routes[1:]
	_, err = cataloggen.RenderPublicOpenAPI(readFixture(t, "../../../generated/openapi-raw/api.swagger.json"), reduced, service)
	require.ErrorContains(t, err, "absent from the public REST surface")
}

func openAPIOperationCount(t *testing.T, document map[string]any) int {
	t.Helper()
	paths, ok := document["paths"].(map[string]any)
	require.True(t, ok)
	count := 0
	for _, value := range paths {
		pathItem, ok := value.(map[string]any)
		require.True(t, ok)
		for method := range pathItem {
			switch strings.ToUpper(method) {
			case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS":
				count++
			}
		}
	}
	return count
}
