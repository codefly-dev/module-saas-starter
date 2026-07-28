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

	require.Equal(t, string(readFixture(t, "../../../../../deployment/generated/service-topology.json")), string(first.CatalogJSON), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "../../../../../module.codefly.yaml")), string(first.ModuleManifest), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "testdata/network-policy.golden.yaml")), string(first.NetworkPolicy), "run: go generate ./pkg/cataloggen")
	for service, document := range first.ServiceManifests {
		checkedIn := readFixture(t, filepath.Join("../../../../../services", service, "service.codefly.yaml"))
		require.Equal(t, string(checkedIn), string(document), "service %s: run go generate ./pkg/cataloggen", service)
	}

	require.Len(t, first.Catalog.GetServices(), 7)
	require.Len(t, first.Catalog.GetInterfaceEndpoints(), 2)
	require.Len(t, first.Catalog.GetPublicEgress(), 2)
	endpointCount, dependencyCount := 0, 0
	for _, service := range first.Catalog.GetServices() {
		endpointCount += len(service.GetEndpoints())
		dependencyCount += len(service.GetDependencies())
	}
	require.Equal(t, 11, endpointCount)
	require.Equal(t, 8, dependencyCount)
	frontendManifest := string(first.ServiceManifests["frontend"])
	require.Contains(t, frontendManifest, "execution-profiles:")
	require.Contains(t, frontendManifest, "local: development")
	require.Contains(t, frontendManifest, "production: production")
	require.Equal(t, 15, strings.Count(string(first.NetworkPolicy), "\nkind: NetworkPolicy\n"))
	require.NotContains(t, string(first.NetworkPolicy), "allow-intra-namespace")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-accounts-from-dependents")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-auth-sidecar-to-dependencies")
	require.Contains(t, string(first.NetworkPolicy), "name: allow-frontend-public-egress")
	require.Contains(t, string(first.NetworkPolicy), "192.175.48.0/24")
	require.Contains(t, string(first.NetworkPolicy), "64:ff9b::/96")
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
	require.Len(t, loadedServices, 7)

	moduleDocument := readFixture(t, "../../../../../module.codefly.yaml")
	var moduleEntry struct {
		ServiceEntry string `yaml:"service-entry"`
	}
	require.NoError(t, yaml.Unmarshal(moduleDocument, &moduleEntry))
	require.Equal(t, "auth-sidecar", moduleEntry.ServiceEntry)
	var module resources.Module
	require.NoError(t, yaml.Unmarshal(moduleDocument, &module))
	_, err = module.Proto(ctx)
	require.NoError(t, err)
	require.Len(t, module.ServiceReferences, 7)

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
	require.Len(t, names, 15)
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

	missingProtocol := strings.Replace(bindings, "        api: connect", "        api: http", 1)
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, []byte(missingProtocol))
	require.ErrorContains(t, err, "required API CODEFLY_API_CONNECT")

	unknownServiceEntry := strings.Replace(bindings, "service_entry: auth-sidecar", "service_entry: missing", 1)
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
