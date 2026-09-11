package cataloggen_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

func TestDeploymentTopologyIsDeterministicAndCurrent(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	allowlist := readFixture(t, "../../../../frontend/code/server/plugin-service-allowlist.generated.json")
	application := readOptionalFixture(t, "../../../../../deployment/application.bindings.codefly.yaml")

	first, err := cataloggen.BuildDeploymentArtifactsWithApplicationBindings(serviceCatalog, bindings, allowlist, application)
	require.NoError(t, err)
	second, err := cataloggen.BuildDeploymentArtifactsWithApplicationBindings(serviceCatalog, bindings, allowlist, application)
	require.NoError(t, err)
	require.Equal(t, first.CatalogJSON, second.CatalogJSON)
	require.Equal(t, first.ModuleManifest, second.ModuleManifest)
	require.Equal(t, first.ServiceManifests, second.ServiceManifests)
	require.Equal(t, first.NetworkPolicy, second.NetworkPolicy)
	require.Equal(t, first.MeshPolicy, second.MeshPolicy)

	require.Equal(t, string(readFixture(t, "../../../../../deployment/generated/service-topology.json")), string(first.CatalogJSON), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "../../../../../module.codefly.yaml")), string(first.ModuleManifest), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "testdata/network-policy.golden.yaml")), string(first.NetworkPolicy), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "testdata/mesh-policy.golden.yaml")), string(first.MeshPolicy), "run: go generate ./pkg/cataloggen")
	for service, document := range first.ServiceManifests {
		checkedIn := readFixture(t, filepath.Join("../../../../../services", service, "service.codefly.yaml"))
		require.Equal(t, string(checkedIn), string(document), "service %s: run go generate ./pkg/cataloggen", service)
	}

	require.Len(t, first.Catalog.GetServices(), 8)
	require.Len(t, first.Catalog.GetInterfaceEndpoints(), 6)
	require.Len(t, first.Catalog.GetPublicEgress(), 4)
	endpointCount, dependencyCount := 0, 0
	for _, service := range first.Catalog.GetServices() {
		endpointCount += len(service.GetEndpoints())
		dependencyCount += len(service.GetDependencies())
	}
	require.Equal(t, 14, endpointCount)
	require.Equal(t, 8, dependencyCount)
	privateREST := map[string]bool{"accounts": false, "auth-gateway": false}
	authGatewayTelemetry := false
	for _, service := range first.Catalog.GetServices() {
		if _, ok := privateREST[service.GetName()]; !ok {
			continue
		}
		for _, endpoint := range service.GetEndpoints() {
			if endpoint.GetName() == "rest" {
				require.Equal(t, catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_PRIVATE, endpoint.GetVisibility())
				privateREST[service.GetName()] = true
			}
		}
		if service.GetName() == "auth-gateway" {
			for _, dependency := range service.GetDependencies() {
				if dependency.GetService() == "telemetry" {
					require.Equal(t, []string{"grpc"}, dependency.GetEndpoints())
					authGatewayTelemetry = true
				}
			}
		}
	}
	require.Equal(t, map[string]bool{"accounts": true, "auth-gateway": true}, privateREST)
	require.True(t, authGatewayTelemetry)
	accountsConnectExposed := false
	for _, endpoint := range first.Catalog.GetInterfaceEndpoints() {
		if endpoint.GetService() == "accounts" && endpoint.GetEndpoint() == "connect" {
			accountsConnectExposed = true
		}
		require.False(t,
			(endpoint.GetService() == "accounts" && endpoint.GetEndpoint() != "connect" && endpoint.GetEndpoint() != "custody" && endpoint.GetEndpoint() != "revision") ||
				(endpoint.GetService() == "auth-gateway" && endpoint.GetEndpoint() == "rest"),
		)
	}
	require.True(t, accountsConnectExposed)
	require.Contains(t, string(first.ServiceManifests["accounts"]), "- observability")
	authGatewayManifest := string(first.ServiceManifests["auth-gateway"])
	require.Contains(t, authGatewayManifest, "- observability")
	require.Contains(t, authGatewayManifest, "- name: telemetry")
	frontendManifest := string(first.ServiceManifests["frontend"])
	require.Contains(t, frontendManifest, "execution-profiles:")
	require.Contains(t, frontendManifest, "local: development")
	require.Contains(t, frontendManifest, "production: production")
	require.Equal(t, 20, strings.Count(string(first.NetworkPolicy), "\nkind: NetworkPolicy\n"))
	require.NotContains(t, string(first.NetworkPolicy), "allow-intra-namespace")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-accounts-from-dependents")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-auth-gateway-from-dependents")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-auth-gateway-to-dependencies")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-frontend-to-dependencies")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-store-from-bootstrap")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-store-bootstrap-to-store")
	require.Equal(t, 2, strings.Count(string(first.NetworkPolicy), "codefly.dev/bootstrap-service: store"))
	require.Contains(t, string(first.NetworkPolicy), "name: allow-frontend-public-egress")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-marketing-public-egress")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-telemetry-from-dependents")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-telemetry-public-egress")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-istio-ingress-to-marketing")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-istio-ingress-to-frontend")
	require.NotContains(t, string(first.NetworkPolicy), "name: allow-istio-ingress-to-auth-gateway")
	require.Contains(t, string(first.NetworkPolicy), "192.175.48.0/24")
	require.Contains(t, string(first.NetworkPolicy), "64:ff9b::/96")

	// Mesh policy: STRICT mTLS baseline, a positive ALLOW AuthorizationPolicy
	// that gates the internal authority method paths to the target's declared
	// callers by their per-service ServiceAccount — deny-by-default for everyone
	// else — and a DENY subtracting the frontend's authored internal HTTP routes
	// from the port-wide ingress allow. accounts' only caller is auth-gateway, so
	// only sa/auth-gateway is allowed; the shared sa/default and the ingress
	// gateway SA are not.
	mesh := string(first.MeshPolicy)
	require.Contains(t, mesh, "kind: PeerAuthentication")
	require.Contains(t, mesh, "mode: STRICT")
	require.Contains(t, mesh, "name: allow-accounts-internal-authority")
	require.Contains(t, mesh, "action: ALLOW")
	require.Contains(t, mesh, "name: deny-frontend-internal-http")
	require.Contains(t, mesh, "action: DENY")
	require.Contains(t, mesh, `- "/api/solutions/register"`)
	require.Contains(t, mesh, "gatewayClassName: istio-waypoint")
	require.Contains(t, mesh, "cluster.local/ns/saas-starter/sa/auth-gateway")
	require.NotContains(t, mesh, "cluster.local/ns/saas-starter/sa/default")
	// Neither L7 policy may carry a workload selector: ztunnel cannot evaluate an
	// L7 rule and fails safe by denying everything to the workload it selects.
	require.NotContains(t, mesh, "  selector:")
	require.Contains(t, mesh, "  targetRefs:")
	for _, procedure := range []string{
		"/saas.accounts.v1.PermissionService/CheckPermission",
		"/saas.accounts.v1.PermissionService/CheckAccess",
		"/saas.accounts.v1.PermissionService/Decide",
		"/saas.accounts.v1.IdentityService/ResolveIdentity",
		"/saas.accounts.v1.APIKeyService/ValidateAPIKey",
		"/saas.accounts.v1.PrincipalService/GetPrincipal",
		"/saas.accounts.v1.PrincipalService/GetAgentPrincipal",
		"/saas.accounts.v1.UsageService/ConsumeUsage",
	} {
		require.Contains(t, mesh, procedure)
	}
}

func TestApplicationBindingsAddOnlyNamedPostgresMigrationSources(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	allowlist := []byte(`{"schemaVersion":1,"contractVersion":2,"entries":[]}`)
	application := []byte(`version: v1
module_name: installed-saas
postgres_migration_sources:
  - service: store
    name: acme
    path: ../../../platform/services/acme/migrations
  - service: store
    name: eventlog
    path: ../../../platform/services/eventlog/migrations
`)

	artifacts, err := cataloggen.BuildDeploymentArtifactsWithApplicationBindings(
		serviceCatalog,
		bindings,
		allowlist,
		application,
	)
	require.NoError(t, err)
	require.Contains(t, string(artifacts.ModuleManifest), "name: installed-saas")
	store := string(artifacts.ServiceManifests["store"])
	require.Contains(t, store, "migration-sources:")
	require.Contains(t, store, "name: eventlog")
	require.Contains(t, store, "name: acme")
	require.Contains(t, store, "../../../platform/services/eventlog/migrations")
	require.NotContains(t, string(artifacts.ServiceManifests["accounts"]), "migration-sources:")

	unsafe := strings.Replace(string(application), "../../../platform/services/eventlog/migrations", "/tmp/eventlog", 1)
	_, err = cataloggen.BuildDeploymentArtifactsWithApplicationBindings(serviceCatalog, bindings, allowlist, []byte(unsafe))
	require.ErrorContains(t, err, "portable relative path")

	wrongService := strings.Replace(string(application), "service: store", "service: accounts", 1)
	_, err = cataloggen.BuildDeploymentArtifactsWithApplicationBindings(serviceCatalog, bindings, allowlist, []byte(wrongService))
	require.ErrorContains(t, err, "non-Postgres")
}

func TestFrontendPluginAllowlistGeneratesExternalCodeflyDependencies(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	allowlist := []byte(`{
  "schemaVersion": 1,
  "contractVersion": 2,
  "entries": [
    {
      "plugin": "example.analytics",
      "alias": "connect-api",
      "protocol": "connect",
      "routePrefix": "/connect/example.analytics.v1.AnalyticsService",
      "compatibility": { "contract": "example.analytics", "major": 1 },
      "target": { "module": "products", "service": "telemetry", "endpoint": "connect" }
    },
    {
      "plugin": "example.analytics",
      "alias": "rest-api",
      "protocol": "rest",
      "routePrefix": "/api/v1/analytics",
      "compatibility": {
        "contract": "example.analytics",
        "major": 1,
        "probePath": "/api/v1/analytics/capabilities"
      },
      "target": { "module": "products", "service": "telemetry", "endpoint": "rest" }
    }
  ]
}`)

	withPlugins, err := cataloggen.BuildDeploymentArtifactsWithFrontendPluginAllowlist(
		serviceCatalog,
		bindings,
		allowlist,
	)
	require.NoError(t, err)
	withoutPlugins, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, bindings)
	require.NoError(t, err)

	frontend := string(withPlugins.ServiceManifests["frontend"])
	require.Contains(t, frontend, "plugin-service-allowlist.generated.json")
	require.Contains(t, frontend, `    - name: telemetry
      module: products
      endpoints:
        - name: connect
        - name: rest`)
	require.Equal(t, 1, strings.Count(frontend, "- name: telemetry"))
	var parsed resources.Service
	require.NoError(t, yaml.Unmarshal(withPlugins.ServiceManifests["frontend"], &parsed))
	require.Len(t, parsed.ServiceDependencies, 2)
	require.Equal(t, "telemetry", parsed.ServiceDependencies[1].Name)
	require.Equal(t, "products", parsed.ServiceDependencies[1].Module)
	require.Equal(t, []string{"connect", "rest"}, []string{
		parsed.ServiceDependencies[1].Endpoints[0].Name,
		parsed.ServiceDependencies[1].Endpoints[1].Name,
	})
	for service, manifest := range withoutPlugins.ServiceManifests {
		if service != "frontend" {
			require.Equal(t, manifest, withPlugins.ServiceManifests[service], service)
		}
	}
}

func TestFrontendPluginAllowlistRejectsUnsafeDeploymentDrift(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")
	valid := `{
  "schemaVersion": 1,
  "contractVersion": 2,
  "entries": [{
    "plugin": "example",
    "alias": "api",
    "protocol": "rest",
    "routePrefix": "/api/v1/example",
    "compatibility": { "contract": "example", "major": 1 },
    "target": { "module": "products", "service": "example", "endpoint": "rest" }
  }]
}`

	tests := []struct {
		name      string
		document  string
		wantError string
	}{
		{
			name:      "unknown field",
			document:  strings.Replace(valid, `"alias": "api",`, `"alias": "api", "hostname": "internal",`, 1),
			wantError: "unknown field",
		},
		{
			name:      "protocol endpoint mismatch",
			document:  strings.Replace(valid, `"endpoint": "rest"`, `"endpoint": "connect"`, 1),
			wantError: "disagrees with protocol",
		},
		{
			name:      "unsafe route",
			document:  strings.Replace(valid, `/api/v1/example`, `/api/../private`, 1),
			wantError: "unsafe route prefix",
		},
		{
			name:      "invalid compatibility",
			document:  strings.Replace(valid, `"major": 1`, `"major": 0`, 1),
			wantError: "invalid compatibility",
		},
		{
			name:      "unsafe compatibility probe",
			document:  strings.Replace(valid, `"major": 1`, `"major": 1, "probePath": "/api/../private"`, 1),
			wantError: "unsafe compatibility probe path",
		},
		{
			name:      "connect compatibility probe",
			document:  strings.Replace(strings.Replace(valid, `"protocol": "rest"`, `"protocol": "connect"`, 1), `"major": 1`, `"major": 1, "probePath": "/connect/example"`, 1),
			wantError: "unsafe compatibility probe path",
		},
		{
			name:      "unsafe Codefly target",
			document:  strings.Replace(valid, `"module": "products"`, `"module": "https://products"`, 1),
			wantError: "unsafe Codefly target",
		},
		{
			name:      "starter internal target",
			document:  strings.Replace(valid, `"module": "products", "service": "example"`, `"module": "saas-starter", "service": "accounts"`, 1),
			wantError: "external product module",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := cataloggen.BuildDeploymentArtifactsWithFrontendPluginAllowlist(
				serviceCatalog,
				bindings,
				[]byte(test.document),
			)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestGeneratedCodeflyAndNetworkManifestsParseStrictly(t *testing.T) {
	ctx := context.Background()
	loadedModule, err := resources.LoadModuleFromDir(ctx, filepath.Clean("../../../../../"))
	require.NoError(t, err)
	require.NoError(t, loadedModule.ValidateInterface(ctx))
	loadedServices, err := loadedModule.LoadServices(ctx)
	require.NoError(t, err)
	require.Len(t, loadedServices, 8)

	moduleDocument := readFixture(t, "../../../../../module.codefly.yaml")
	var moduleEntry struct {
		ServiceEntry string `yaml:"service-entry"`
	}
	require.NoError(t, yaml.Unmarshal(moduleDocument, &moduleEntry))
	require.Equal(t, "frontend", moduleEntry.ServiceEntry)
	var module resources.Module
	require.NoError(t, yaml.Unmarshal(moduleDocument, &module))
	_, err = module.Proto(ctx)
	require.NoError(t, err)
	require.Len(t, module.ServiceReferences, 8)

	for _, reference := range module.ServiceReferences {
		document := readFixture(t, filepath.Join("../../../../../services", reference.Name, "service.codefly.yaml"))
		var service resources.Service
		require.NoError(t, yaml.Unmarshal(document, &service), reference.Name)
		require.Equal(t, reference.Name, service.Name)
		for _, endpoint := range service.Endpoints {
			endpoint.Module = module.Name
			endpoint.Service = service.Name
			if endpoint.Visibility == "" {
				endpoint.Visibility = resources.VisibilityPrivate
			}
			_, err := endpoint.Proto()
			require.NoError(t, err, "%s/%s", service.Name, endpoint.Name)
		}
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(readFixture(t, "testdata/network-policy.golden.yaml"))))
	names := make(map[string]bool)
	for {
		var document struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, "networking.k8s.io/v1", document.APIVersion)
		require.Equal(t, "NetworkPolicy", document.Kind)
		require.Equal(t, "saas-starter", document.Metadata.Namespace)
		require.NotEmpty(t, document.Spec)
		require.False(t, names[document.Metadata.Name], "duplicate NetworkPolicy %s", document.Metadata.Name)
		names[document.Metadata.Name] = true
	}
	require.Len(t, names, 20)
	require.True(t, names["allow-istio-ingress-to-marketing"])
	require.True(t, names["allow-istio-ingress-to-frontend"])
	require.False(t, names["allow-istio-ingress-to-auth-gateway"])
	require.True(t, names["allow-store-from-bootstrap"])
	require.True(t, names["allow-store-bootstrap-to-store"])
	require.True(t, names["allow-telemetry-from-dependents"])
	require.True(t, names["allow-telemetry-public-egress"])
}

func TestGeneratedMeshPolicyGatesInternalAuthorityByCallerIdentity(t *testing.T) {
	const ingressGatewaySA = "cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account"

	decoder := yaml.NewDecoder(strings.NewReader(string(readFixture(t, "testdata/mesh-policy.golden.yaml"))))
	strictMTLS := false
	allowFound := false
	waypointFound := false
	for {
		var document struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string            `yaml:"name"`
				Namespace string            `yaml:"namespace"`
				Labels    map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, "saas-starter", document.Metadata.Namespace)

		switch document.Kind {
		case "PeerAuthentication":
			require.Equal(t, "security.istio.io/v1", document.APIVersion)
			mtls, ok := document.Spec["mtls"].(map[string]any)
			require.True(t, ok, "PeerAuthentication must configure mtls")
			require.Equal(t, "STRICT", mtls["mode"])
			strictMTLS = true
		case "AuthorizationPolicy":
			require.Equal(t, "security.istio.io/v1", document.APIVersion)
			// Attachment, not selection. A selector routes an L7 policy to ztunnel,
			// which cannot evaluate one and fails safe by turning it into a blanket
			// DENY on the workload it selects. A targetRef to a Service both puts
			// the policy on the waypoint that can evaluate it and keeps it scoped to
			// one service rather than the namespace (the shape the GitOps baseline's
			// empty-ALLOW default-deny takes).
			require.NotContains(t, document.Spec, "selector",
				"an L7 policy attached by selector is enforced by ztunnel, which fails safe into a blanket deny")
			targetRefs, ok := document.Spec["targetRefs"].([]any)
			require.True(t, ok, "an L7 policy must attach to a waypoint by targetRef")
			require.Len(t, targetRefs, 1, "the gate scopes to exactly one service")
			targetRef, ok := targetRefs[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "Service", targetRef["kind"])
			require.Equal(t, "", targetRef["group"])

			if document.Metadata.Name == "deny-frontend-internal-http" {
				// A different resource gating a different surface: the authored HTTP
				// routes, not the catalog-derived procedures. Its rules are pinned by
				// TestMeshPolicyGatesInternalSurfacesByShape.
				require.Equal(t, "frontend", targetRef["name"])
				require.Equal(t, "DENY", document.Spec["action"])
				continue
			}
			require.Equal(t, "allow-accounts-internal-authority", document.Metadata.Name)
			require.Equal(t, "ALLOW", document.Spec["action"])
			require.Equal(t, "accounts", targetRef["name"],
				"only the catalog owner carries a generated reach gate")
			allowFound = true

			rule := document.Spec["rules"].([]any)[0].(map[string]any)
			source := rule["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
			principals := source["principals"].([]any)
			// Positive allowlist: only accounts' declared caller (auth-gateway),
			// named by its per-service SA. The shared sa/default (any namespace
			// pod) and the ingress gateway SA are NOT admitted — deny-by-default.
			require.Equal(t, []any{"cluster.local/ns/saas-starter/sa/auth-gateway"}, principals)
			require.NotContains(t, principals, "cluster.local/ns/saas-starter/sa/default")
			require.NotContains(t, principals, ingressGatewaySA)

			paths := rule["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)["paths"].([]any)
			require.Contains(t, paths, "/saas.accounts.v1.APIKeyService/ValidateAPIKey")
			require.Contains(t, paths, "/saas.accounts.v1.UsageService/ConsumeUsage")
			// EVERY gated path is a gRPC procedure, not only the two named above.
			// The frontend's token-gated HTTP routes are not gated here and cannot
			// be: this policy is derived from authz-methods.json. They are authored
			// instead, and carry their own deny. See DEPLOYMENT_TOPOLOGY.md,
			// "Cluster-internal HTTP routes".
			for _, path := range paths {
				require.True(t, strings.HasPrefix(path.(string), "/saas.accounts.v1."),
					"the gate lists gRPC procedures, not HTTP routes: %q", path)
			}
		case "Gateway":
			// The waypoint that makes the L7 allow enforceable in the ambient mesh.
			require.Equal(t, "gateway.networking.k8s.io/v1", document.APIVersion)
			require.Equal(t, "istio-waypoint", document.Spec["gatewayClassName"])
			require.Equal(t, "service", document.Metadata.Labels["istio.io/waypoint-for"])
			waypointFound = true
		default:
			t.Fatalf("unexpected mesh policy kind %q", document.Kind)
		}
	}
	require.True(t, strictMTLS, "namespace mTLS must be STRICT")
	require.True(t, allowFound, "internal-authority AuthorizationPolicy must be present")
	require.True(t, waypointFound, "L7 allow requires a waypoint to be enforced in the ambient mesh")
}

// TestMeshPolicyGatesInternalSurfacesByShape states the boundary the pin #570
// left behind (TestMeshPolicyGatesOnlyOwnerGRPCProcedures, since folded into
// TestGeneratedMeshPolicyGatesInternalAuthorityByCallerIdentity) was written to
// force: it required the mesh policy to carry exactly one gate, the catalog
// owner's gRPC procedures, and to fail the moment the renderer learned HTTP
// paths. It now has, so the boundary is stated by shape rather than by count.
// A catalog-derived gate lists gRPC procedures for the owner alone; an authored
// HTTP route is only ever subtracted from the port-wide ingress ALLOW by a DENY.
// Attachment is pinned by the walker above; what is pinned here is every rule of
// the deny, which is the half no other test reads.
func TestMeshPolicyGatesInternalSurfacesByShape(t *testing.T) {
	const meshIngressPrincipal = "cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account"

	// Every authored route, and the methods that carry internal authority on it.
	// A GET on /api/solutions/register is the sidebar's unauthenticated nav poll
	// and must stay reachable; a GET on /api/internal/solutions is the internal
	// detail read and must not.
	wantRules := map[string][]any{
		"/api/internal/solutions": {"GET"},
		"/api/solutions/register": {"DELETE", "POST"},
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(readFixture(t, "testdata/mesh-policy.golden.yaml"))))
	allows, denies := 0, 0
	for {
		var document struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if document.Kind != "AuthorizationPolicy" {
			continue
		}
		// Read defensively: a policy shape this test does not expect must fail it,
		// not panic out of the run and take the other assertions with it.
		rules, ok := document.Spec["rules"].([]any)
		require.True(t, ok, "policy %q carries no rules", document.Metadata.Name)

		switch document.Spec["action"] {
		case "ALLOW":
			allows++
			require.Equal(t, "allow-accounts-internal-authority", document.Metadata.Name)
			for _, rule := range rules {
				operation, ok := rule.(map[string]any)["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)
				require.True(t, ok)
				for _, path := range operation["paths"].([]any) {
					require.True(t, strings.HasPrefix(path.(string), "/saas.accounts.v1."),
						"a catalog-derived gate lists gRPC procedures, not HTTP routes: %q", path)
				}
			}
		case "DENY":
			denies++
			require.Equal(t, "deny-frontend-internal-http", document.Metadata.Name)
			require.Len(t, rules, len(wantRules), "one rule per authored route")
			for _, rule := range rules {
				source, ok := rule.(map[string]any)["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
				require.True(t, ok, "a deny with no source clause denies the route outright")
				// The waypoint that evaluates this rule never sees ingress-originated
				// traffic, so the ingress gateway is exempt by construction. Denying
				// it here would read as a north-south boundary and enforce nothing;
				// the route's credential is what gates the front door.
				require.Contains(t, source["notPrincipals"], meshIngressPrincipal)

				operation, ok := rule.(map[string]any)["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)
				require.True(t, ok)
				paths := operation["paths"].([]any)
				// Istio does not merge duplicate slashes by default, so the exact
				// path alone would let //api/solutions/register reach the handler.
				require.Len(t, paths, 2, "each route is matched exactly and as a suffix")
				path := paths[1].(string)
				require.Equal(t, "*"+path, paths[0])
				methods, declared := wantRules[path]
				require.True(t, declared, "policy gates an undeclared route %q", path)
				require.Equal(t, methods, operation["methods"])
				delete(wantRules, path)
			}
		default:
			t.Fatalf("unexpected mesh policy action %v on %q", document.Spec["action"], document.Metadata.Name)
		}
	}
	require.Equal(t, 1, allows, "exactly one internal-authority allow, for the catalog owner")
	require.Equal(t, 1, denies, "exactly one internal-HTTP deny, for the service that declares such routes")
	require.Empty(t, wantRules, "every authored route must be gated")
}

// TestInternalHTTPDenyExemptsDeclaredCallers covers the branch the shipped
// topology does not exercise: once a service declares an edge to the endpoint
// carrying internal routes, that caller — and only it, alongside the ingress
// gateway the waypoint cannot see — is subtracted from the deny.
func TestInternalHTTPDenyExemptsDeclaredCallers(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := strings.NewReplacer(
		`  - name: marketing
    version: 0.0.0`,
		`  - name: marketing
    version: 0.0.0
    dependencies:
      - service: frontend
        endpoints:
          - http`,
		"      mode: ssr\n  - name: store",
		"      mode: ssr\n      service-account:\n        name: marketing\n  - name: store",
	).Replace(string(readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml")))

	require.Contains(t, bindings, "        name: marketing", "the topology binding no longer matches this fixture")

	artifacts, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(bindings))
	require.NoError(t, err)

	// The same authored edge opens the L3 path the exemption is useless without.
	network := string(artifacts.NetworkPolicy)
	require.Contains(t, network, "name: allow-frontend-from-dependents")
	require.Contains(t, network, "name: allow-marketing-to-dependencies")

	decoder := yaml.NewDecoder(strings.NewReader(string(artifacts.MeshPolicy)))
	found := false
	for {
		var document struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if document.Metadata.Name != "deny-frontend-internal-http" {
			continue
		}
		found = true
		rules, ok := document.Spec["rules"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, rules)
		for _, rule := range rules {
			source, ok := rule.(map[string]any)["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, []any{
				"cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account",
				"cluster.local/ns/saas-starter/sa/marketing",
			}, source["notPrincipals"],
				"the exemption is the caller's own ServiceAccount, never the shared default")
		}
	}
	require.True(t, found, "the frontend still declares internal HTTP routes")
}

func TestDeploymentTopologyRejectsUnsafeOrIncompleteBindings(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := string(readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml"))

	unknownField := strings.Replace(bindings, "version: v1", "version: v1\nunknown: true", 1)
	_, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(unknownField))
	require.ErrorContains(t, err, "field unknown not found")

	unknownEndpoint := strings.Replace(bindings, "          - read\n          - write", "          - missing\n          - write", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(unknownEndpoint))
	require.ErrorContains(t, err, "unknown endpoint")

	unknownBootstrapEndpoint := strings.Replace(bindings, "    bootstrap_job_endpoints:\n      - tcp", "    bootstrap_job_endpoints:\n      - missing", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(unknownBootstrapEndpoint))
	require.ErrorContains(t, err, "bootstrap Job references unknown endpoint")

	missingProtocol := strings.Replace(bindings, "        api: connect", "        api: http", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(missingProtocol))
	require.ErrorContains(t, err, "required API CODEFLY_API_CONNECT")

	unknownServiceEntry := strings.Replace(bindings, "service_entry: frontend", "service_entry: missing", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(unknownServiceEntry))
	require.ErrorContains(t, err, "service entry references unknown service")

	unsortedMethods := strings.Replace(bindings, "          - DELETE\n          - POST", "          - POST\n          - DELETE", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(unsortedMethods))
	require.ErrorContains(t, err, "internal HTTP route \"/api/solutions/register\" methods are invalid or unsorted")

	relativeRoute := strings.Replace(bindings, "      - path: /api/solutions/register", "      - path: api/solutions/register", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(relativeRoute))
	require.ErrorContains(t, err, "internal HTTP routes are invalid or unsorted")

	methodlessRoute := strings.Replace(bindings, "        methods:\n          - DELETE\n          - POST", "        methods: []", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(methodlessRoute))
	require.ErrorContains(t, err, "declares no methods")

	// A path policy on a service that speaks TCP matches nothing that will ever
	// be evaluated, so the route must belong to a service that serves HTTP.
	routesOnTCPService := strings.Replace(bindings,
		"    bootstrap_job_endpoints:\n      - tcp",
		"    internal_http_routes:\n      - path: /internal\n        methods:\n          - POST\n    bootstrap_job_endpoints:\n      - tcp",
		1,
	)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(routesOnTCPService))
	require.ErrorContains(t, err, "internal HTTP routes without an HTTP endpoint")

	cycle := strings.Replace(bindings, "    spec:\n      watch: false\n      with-read-replicas: true", `    dependencies:
      - service: accounts
        endpoints:
          - connect
    spec:
      watch: false
      with-read-replicas: true`, 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(cycle))
	require.ErrorContains(t, err, "contains a cycle")
}

func TestDeploymentTopologyGeneratesValidatedSecretServiceConfigurations(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := string(readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml"))

	artifacts, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(bindings))
	require.NoError(t, err)
	require.Contains(t, string(artifacts.ServiceManifests["vault"]), `secret-service-configurations:
    - name: vault
      entries:
        - key: vault_token`)

	emptyEntries := strings.Replace(bindings,
		"      - name: vault\n        entries:\n          - key: vault_token",
		"      - name: vault\n        entries: []",
		1,
	)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(emptyEntries))
	require.ErrorContains(t, err, "secret service configurations are invalid or unsorted")
}

func TestDeploymentTopologyPreservesCompleteModuleAgentIdentity(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	bindings := string(readFixture(t, "../../../../../deployment/topology.bindings.codefly.yaml"))
	withAgent := strings.Replace(bindings,
		`  description: "SaaS foundation — auth, multi-tenancy, generated RPC policy, RBAC, impersonation, audit"`,
		`  description: "SaaS foundation — auth, multi-tenancy, generated RPC policy, RBAC, impersonation, audit"
  agent:
    kind: codefly:module
    name: saas-starter
    version: 0.0.28
    publisher: codefly.dev`,
		1,
	)

	artifacts, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(withAgent))
	require.NoError(t, err)
	require.Contains(t, string(artifacts.ModuleManifest), `agent:
    kind: codefly:module
    name: saas-starter
    version: 0.0.28
    publisher: codefly.dev`)

	incomplete := strings.Replace(withAgent, "    publisher: codefly.dev\n", "", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(incomplete))
	require.ErrorContains(t, err, "module agent identity is incomplete")
}

func TestDeploymentCatalogValidationRejectsConsumerUnsafeDrift(t *testing.T) {
	document := readFixture(t, "../../../../../deployment/generated/service-topology.json")
	catalog := &catalogv1.DeploymentCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(document, catalog))
	require.NoError(t, cataloggen.ValidateDeploymentCatalog(catalog))

	unsorted := proto.Clone(catalog).(*catalogv1.DeploymentCatalog)
	unsorted.Services[0], unsorted.Services[1] = unsorted.Services[1], unsorted.Services[0]
	require.ErrorContains(t, cataloggen.ValidateDeploymentCatalog(unsorted), "unsorted")

	unknownEndpoint := proto.Clone(catalog).(*catalogv1.DeploymentCatalog)
	unknownEndpoint.Services[0].Dependencies[0].Endpoints[0] = "missing"
	require.ErrorContains(t, cataloggen.ValidateDeploymentCatalog(unknownEndpoint), "unknown endpoint")

	invalidEgress := proto.Clone(catalog).(*catalogv1.DeploymentCatalog)
	invalidEgress.PublicEgress[0].Ports[0] = 0
	require.ErrorContains(t, cataloggen.ValidateDeploymentCatalog(invalidEgress), "public egress")
}

func readOptionalFixture(t *testing.T, path string) []byte {
	t.Helper()
	document, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return document
}
