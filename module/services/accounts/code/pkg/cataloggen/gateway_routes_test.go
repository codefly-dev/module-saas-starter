package cataloggen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

func TestGatewayRouteCatalogCompilationAndParity(t *testing.T) {
	serviceDocument := readFixture(t, "../../../generated/service-catalog.json")
	bindingDocument := readFixture(t, "../../../gateway.bindings.codefly.yaml")
	topologyDocument := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	routes, err := cataloggen.BuildGatewayRouteCatalog(serviceDocument, bindingDocument, topologyDocument)
	require.NoError(t, err)
	require.NoError(t, cataloggen.ValidateGatewayRouteCatalog(routes))

	// Connect/REST/alias totals churn on every endpoint and are pinned
	// byte-for-byte by the generated/gateway-routes.json fixture-equality
	// below; keep only publicCount as a review tripwire for public
	// attack-surface growth, plus the per-route no-INTERNAL invariant.
	publicCount := 0
	byMatch := make(map[string]*catalogv1.GatewayRoute, len(routes.GetRoutes()))
	for _, route := range routes.GetRoutes() {
		byMatch[route.GetMethod()+" "+route.GetPath()] = route
		if route.GetExposure() == policyv1.Exposure_EXPOSURE_PUBLIC {
			publicCount++
		}
		require.NotEqual(t, policyv1.Exposure_EXPOSURE_INTERNAL, route.GetExposure())
	}
	require.Equal(t, 48, publicCount)

	require.Nil(t, byMatch["POST /saas.accounts.v1.APIKeyService/ValidateAPIKey"])
	require.Equal(t, policyv1.Exposure_EXPOSURE_PUBLIC, byMatch["POST /saas.accounts.v1.AuthService/BeginOAuth"].GetExposure())
	require.Equal(t, "/saas.accounts.v1.UserService/GetUser", byMatch["GET /v1/users:byEmail"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.PlatformAdminService/GetJobOperations", byMatch["GET /v1/platform/jobs/operations"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.PlatformAdminService/ReplayJob", byMatch["POST /v1/platform/jobs/{source_job_id}:replay"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.WorkContextService/ExchangeAudience", byMatch["POST /v1/work-contexts:exchange-audience"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.UsageService/GetUsageHistory", byMatch["GET /v1/organizations/{organization_id}/usage/{meter}/history"].GetProcedure())
	require.Equal(t, "/saas.accounts.v1.PlatformAdminService/UpsertFeatureFlag", byMatch["PUT /v1/platform/feature-flags/{name}"].GetProcedure())
	require.Equal(t, policyv1.Exposure_EXPOSURE_AUTHENTICATED, byMatch["POST /v1/invitations:inspect-id"].GetExposure())
	legacy := byMatch["POST /customers.UserService/GetSelf"]
	require.Equal(t, "/saas.accounts.v1.UserService/GetSelf", legacy.GetRewritePath())
	require.Equal(t, "2026-10-11", legacy.GetRemoveAfter())
	require.Equal(t, "accounts", legacy.GetOwner().GetService())
	require.Equal(t, "connect", legacy.GetUpstreamEndpoint())

	renamedTopology := strings.Replace(string(topologyDocument), "      - name: connect\n", "      - name: connect-api\n", 1)
	renamedTopology = strings.ReplaceAll(renamedTopology, "          - connect\n", "          - connect-api\n")
	renamedRoutes, err := cataloggen.BuildGatewayRouteCatalog(serviceDocument, bindingDocument, []byte(renamedTopology))
	require.NoError(t, err)
	require.Equal(t, "connect-api", renamedRoutes.GetRoutes()[0].GetUpstreamEndpoint())
}

func TestGatewayArtifactsAreDeterministicAndCurrent(t *testing.T) {
	serviceDocument := readFixture(t, "../../../generated/service-catalog.json")
	bindingDocument := readFixture(t, "../../../gateway.bindings.codefly.yaml")
	topologyDocument := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	routes, err := cataloggen.BuildGatewayRouteCatalog(serviceDocument, bindingDocument, topologyDocument)
	require.NoError(t, err)

	routeJSON, err := cataloggen.RenderGatewayRouteCatalogJSON(routes)
	require.NoError(t, err)
	require.Equal(t, string(routeJSON), string(readFixture(t, "../../../generated/gateway-routes.json")), "run: go generate ./pkg/cataloggen")
	secondJSON, err := cataloggen.RenderGatewayRouteCatalogJSON(routes)
	require.NoError(t, err)
	require.Equal(t, routeJSON, secondJSON)

	parsed := &catalogv1.GatewayRouteCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(routeJSON, parsed))
	require.True(t, proto.Equal(routes, parsed))

	goRoutes, err := cataloggen.RenderAuthGatewayConnectRoutes(routes)
	require.NoError(t, err)
	require.Equal(t, string(goRoutes), string(readFixture(t, "../../../../auth-gateway/code/routing_catalog_gen.go")), "run: go generate ./pkg/cataloggen")

}

func TestGatewayRouteValidationRejectsUnsafeDrift(t *testing.T) {
	serviceDocument := readFixture(t, "../../../generated/service-catalog.json")
	bindingDocument := readFixture(t, "../../../gateway.bindings.codefly.yaml")
	topologyDocument := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	routes, err := cataloggen.BuildGatewayRouteCatalog(serviceDocument, bindingDocument, topologyDocument)
	require.NoError(t, err)

	internal := proto.Clone(routes).(*catalogv1.GatewayRouteCatalog)
	internal.Routes[0].Exposure = policyv1.Exposure_EXPOSURE_INTERNAL
	require.ErrorContains(t, cataloggen.ValidateGatewayRouteCatalog(internal), "non-public-edge exposure")

	aliasWithoutRewrite := proto.Clone(routes).(*catalogv1.GatewayRouteCatalog)
	aliasWithoutRewrite.Routes[0].RewritePath = ""
	require.ErrorContains(t, cataloggen.ValidateGatewayRouteCatalog(aliasWithoutRewrite), "incomplete rewrite metadata")

	wrongSchema := proto.Clone(routes).(*catalogv1.GatewayRouteCatalog)
	wrongSchema.SchemaVersion = "saas.gateway.routes.v2"
	require.ErrorContains(t, cataloggen.ValidateGatewayRouteCatalog(wrongSchema), "unsupported")

	badEndpoint := strings.Replace(string(topologyDocument), "api: connect", "api: connect;raw", 1)
	_, err = cataloggen.BuildGatewayRouteCatalog(serviceDocument, bindingDocument, []byte(badEndpoint))
	require.ErrorContains(t, err, "Codefly API")

	unknownField := string(bindingDocument) + "unknown: true\n"
	_, err = cataloggen.BuildGatewayRouteCatalog(serviceDocument, []byte(unknownField), topologyDocument)
	require.ErrorContains(t, err, "field unknown not found")
}
