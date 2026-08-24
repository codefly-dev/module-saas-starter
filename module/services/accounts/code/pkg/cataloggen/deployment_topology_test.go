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
	require.Len(t, first.Catalog.GetInterfaceEndpoints(), 3)
	require.Len(t, first.Catalog.GetPublicEgress(), 4)
	endpointCount, dependencyCount := 0, 0
	for _, service := range first.Catalog.GetServices() {
		endpointCount += len(service.GetEndpoints())
		dependencyCount += len(service.GetDependencies())
	}
	require.Equal(t, 12, endpointCount)
	require.Equal(t, 8, dependencyCount)
	privateREST := map[string]bool{"accounts": false, "auth-sidecar": false}
	authSidecarTelemetry := false
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
		if service.GetName() == "auth-sidecar" {
			for _, dependency := range service.GetDependencies() {
				if dependency.GetService() == "telemetry" {
					require.Equal(t, []string{"grpc"}, dependency.GetEndpoints())
					authSidecarTelemetry = true
				}
			}
		}
	}
	require.Equal(t, map[string]bool{"accounts": true, "auth-sidecar": true}, privateREST)
	require.True(t, authSidecarTelemetry)
	for _, endpoint := range first.Catalog.GetInterfaceEndpoints() {
		require.False(t,
			endpoint.GetService() == "accounts" ||
				(endpoint.GetService() == "auth-sidecar" && endpoint.GetEndpoint() == "rest"),
		)
	}
	require.Contains(t, string(first.ServiceManifests["accounts"]), "- observability")
	authSidecarManifest := string(first.ServiceManifests["auth-sidecar"])
	require.Contains(t, authSidecarManifest, "- observability")
	require.Contains(t, authSidecarManifest, "- name: telemetry")
	frontendManifest := string(first.ServiceManifests["frontend"])
	require.Contains(t, frontendManifest, "execution-profiles:")
	require.Contains(t, frontendManifest, "local: development")
	require.Contains(t, frontendManifest, "production: production")
	require.Equal(t, 20, strings.Count(string(first.NetworkPolicy), "\nkind: NetworkPolicy\n"))
	require.NotContains(t, string(first.NetworkPolicy), "allow-intra-namespace")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-accounts-from-dependents")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-auth-sidecar-from-dependents")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-auth-sidecar-to-dependencies")
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
	require.NotContains(t, string(first.NetworkPolicy), "name: allow-istio-ingress-to-auth-sidecar")
	require.Contains(t, string(first.NetworkPolicy), "192.175.48.0/24")
	require.Contains(t, string(first.NetworkPolicy), "64:ff9b::/96")

	// Mesh policy: STRICT mTLS baseline plus a positive ALLOW AuthorizationPolicy
	// that gates the internal authority method paths to the target's declared
	// callers by their per-service ServiceAccount — deny-by-default for everyone
	// else. accounts' only caller is auth-sidecar, so only sa/auth-sidecar is
	// allowed; the shared sa/default and the ingress gateway SA are not.
	mesh := string(first.MeshPolicy)
	require.Contains(t, mesh, "kind: PeerAuthentication")
	require.Contains(t, mesh, "mode: STRICT")
	require.Contains(t, mesh, "name: allow-accounts-internal-authority")
	require.Contains(t, mesh, "action: ALLOW")
	require.Contains(t, mesh, "gatewayClassName: istio-waypoint")
	require.Contains(t, mesh, "cluster.local/ns/saas-starter/sa/auth-sidecar")
	require.NotContains(t, mesh, "cluster.local/ns/saas-starter/sa/default")
	require.NotContains(t, mesh, "istio-ingressgateway-service-account")
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
    name: eventlog
    path: ../../../platform/services/eventlog/migrations
  - service: store
    name: warden
    path: ../../../platform/services/warden/migrations
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
	require.Contains(t, store, "name: warden")
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
	require.False(t, names["allow-istio-ingress-to-auth-sidecar"])
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
			require.Equal(t, "allow-accounts-internal-authority", document.Metadata.Name)
			require.Equal(t, "ALLOW", document.Spec["action"])
			allowFound = true

			rule := document.Spec["rules"].([]any)[0].(map[string]any)
			source := rule["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
			principals := source["principals"].([]any)
			// Positive allowlist: only accounts' declared caller (auth-sidecar),
			// named by its per-service SA. The shared sa/default (any namespace
			// pod) and the ingress gateway SA are NOT admitted — deny-by-default.
			require.Equal(t, []any{"cluster.local/ns/saas-starter/sa/auth-sidecar"}, principals)
			require.NotContains(t, principals, "cluster.local/ns/saas-starter/sa/default")
			require.NotContains(t, principals, ingressGatewaySA)

			paths := rule["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)["paths"].([]any)
			require.Contains(t, paths, "/saas.accounts.v1.APIKeyService/ValidateAPIKey")
			require.Contains(t, paths, "/saas.accounts.v1.UsageService/ConsumeUsage")
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
