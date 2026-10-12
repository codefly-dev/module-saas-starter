package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/network"
)

// A service like accounts: three conventional endpoints and one named gRPC
// endpoint the agent binds at the port Codefly allocates for it.
const portedServiceManifest = `name: accounts
version: 0.0.0
agent:
  kind: codefly:service
  name: go-grpc
  publisher: codefly.dev
  version: 0.1.0
endpoints:
  - name: authority
    api: grpc
    visibility: internal
    allow-modules: ["*"]
  - name: connect
    visibility: internal
    allow-modules: ["*"]
  - name: grpc
  - name: rest
spec:
  deployment:
    endpoint-ports:
      connect: 8080
      grpc: 9090
      rest: 8080
`

func portedModule(t *testing.T, name string) (string, []serviceDefinition) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, moduleYamlPath), "kind: module\nname: "+name+"\nservices:\n  - name: accounts\n")
	serviceDir := filepath.Join(dir, "services", "accounts")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(serviceDir, "service.codefly.yaml"), portedServiceManifest)
	return dir, []serviceDefinition{{name: "accounts", directory: serviceDir}}
}

func endpointsByName(t *testing.T, topology deploymentTopology) map[string]topologyEndpoint {
	t.Helper()
	out := map[string]topologyEndpoint{}
	for _, endpoint := range topology.Services[0].Endpoints {
		out[endpoint.Name] = endpoint
	}
	return out
}

// Every Service port is Codefly's own in-cluster allocation, keyed by the name
// the workspace composes the module under, so the render and the CLI can never
// disagree. A pod port is the declared one where the service binds a fixed
// port, and the allocated one where it binds what Codefly allocates — which is
// never typed into a manifest.
func TestTopologyPortsComeFromCodeflyAllocation(t *testing.T) {
	allocated := map[string]map[string]uint16{}
	for _, composed := range []string{"identity", "acme-host"} {
		dir, services := portedModule(t, composed)
		topology, err := assembleDeploymentTopology(dir, composed, services)
		if err != nil {
			t.Fatal(err)
		}
		endpoints := endpointsByName(t, topology)

		want, err := network.DeployedEndpointPorts(context.Background(), composed, "accounts", []*basev0.Endpoint{
			{Name: "authority", Api: "grpc"}, {Name: "connect", Api: "connect"}, {Name: "grpc", Api: "grpc"}, {Name: "rest", Api: "rest"},
		})
		if err != nil {
			t.Fatal(err)
		}
		allocated[composed] = want
		for name, endpoint := range endpoints {
			if endpoint.ServicePort != uint32(want[name]) {
				t.Errorf("%s: %s service port = %d, want Codefly's %d", composed, name, endpoint.ServicePort, want[name])
			}
		}
		// Declared pod ports are kept; the named endpoint binds its allocation.
		for name, pod := range map[string]uint32{"connect": 8080, "grpc": 9090, "rest": 8080} {
			if endpoints[name].Port != pod {
				t.Errorf("%s: %s pod port = %d, want the declared %d", composed, name, endpoints[name].Port, pod)
			}
		}
		if endpoints["authority"].Port != endpoints["authority"].ServicePort {
			t.Errorf("%s: authority pod port = %d, want its allocated port %d",
				composed, endpoints["authority"].Port, endpoints["authority"].ServicePort)
		}
		if endpoints["connect"].ServicePort == endpoints["connect"].Port {
			t.Errorf("%s: connect's Service port %d should differ from the pod port it shares with rest", composed, endpoints["connect"].ServicePort)
		}
	}
	if allocated["identity"]["authority"] == allocated["acme-host"]["authority"] {
		t.Fatalf("the named endpoint's port must follow the composed name; both compositions got %d", allocated["identity"]["authority"])
	}
}

// The shipped module: accounts' authority endpoint declares no pod port, and
// renders at its allocation under the module's own name; every other declared
// pod port is kept as declared.
func TestShippedModuleAuthorityPortIsAllocatedNotDeclared(t *testing.T) {
	moduleDir, err := filepath.Abs("module")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := loadModuleManifest(moduleDir)
	if err != nil {
		t.Fatal(err)
	}
	services, err := validateServiceInventory(moduleDir, manifest.Services)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := assembleDeploymentTopology(moduleDir, manifest.Name, services)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range topology.Services {
		for _, endpoint := range service.Endpoints {
			if service.Name == "accounts" && endpoint.Name == "authority" {
				if endpoint.Port != endpoint.ServicePort {
					t.Errorf("authority pod port %d, want its allocated port %d", endpoint.Port, endpoint.ServicePort)
				}
				if endpoint.Port == 9091 {
					t.Error("authority still renders the retired declared port 9091")
				}
			}
			if endpoint.ServicePort == 0 {
				t.Errorf("%s/%s has no Service port", service.Name, endpoint.Name)
			}
		}
	}
}
