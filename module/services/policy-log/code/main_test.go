package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestResolveSettingsNamesEveryMissingKeyAtOnce: a half-provisioned group must
// cost one restart to diagnose, not one per key, and it must fail rather than
// start a listener that cannot witness.
func TestResolveSettingsNamesEveryMissingKeyAtOnce(t *testing.T) {
	_, err := resolveSettings(func(string, string) (string, error) { return "", nil })
	if err == nil {
		t.Fatal("resolveSettings accepted a group that delivered nothing")
	}
	for _, key := range []string{settingKeys.bucket, settingKeys.warehouseEndpoint, settingKeys.warehouseToken} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the refusal does not name the missing %s: %v", key, err)
		}
	}
}

// TestResolveSettingsRefusesEachMissingKeyIndividually proves no single setting
// has a default. A bucket without a warehouse credential, or a warehouse
// without a bucket, is not a startable configuration.
func TestResolveSettingsRefusesEachMissingKeyIndividually(t *testing.T) {
	delivered := map[string]string{
		settingKeys.bucket:            "example-witness-bucket",
		settingKeys.warehouseEndpoint: "https://warehouse.example.com/ingest",
		settingKeys.warehouseToken:    "example-warehouse-token",
	}
	for withheld := range delivered {
		_, err := resolveSettings(func(_, key string) (string, error) {
			if key == withheld {
				return "", nil
			}
			return delivered[key], nil
		})
		if err == nil {
			t.Errorf("resolveSettings started with %s missing", withheld)
			continue
		}
		if !strings.Contains(err.Error(), withheld) {
			t.Errorf("the refusal for a missing %s does not name it: %v", withheld, err)
		}
	}
}

// TestResolveSettingsNeverFallsBackToTheProcessEnvironment is the one that
// keeps "no development bypass" true. An ambient bucket or warehouse credential
// would make this service witness somewhere nobody provisioned — and it would
// do so by starting successfully.
func TestResolveSettingsNeverFallsBackToTheProcessEnvironment(t *testing.T) {
	t.Setenv(settingKeys.bucket, "ambient-bucket")
	t.Setenv(settingKeys.warehouseEndpoint, "https://ambient.example.com/ingest")
	t.Setenv(settingKeys.warehouseToken, "ambient-token")
	// The group is absent, exactly as it is on a workstation.
	resolved, err := resolveSettings(func(string, string) (string, error) {
		return "", errors.New("no configuration found for policy-log")
	})
	if err == nil {
		t.Fatalf("resolveSettings fell back to the process environment and resolved %+v", resolved)
	}
	for _, ambient := range []string{"ambient-bucket", "ambient.example.com", "ambient-token"} {
		if strings.Contains(err.Error(), ambient) {
			t.Errorf("the refusal echoed an ambient value %q", ambient)
		}
	}
}

// TestResolveSettingsNeverEchoesADeliveredValue: one of the three settings is a
// credential, and a startup refusal is the most widely read log line a service
// has. The refusal names keys, never values.
func TestResolveSettingsNeverEchoesADeliveredValue(t *testing.T) {
	const credential = "s3cr3t-warehouse-token"
	_, err := resolveSettings(func(_, key string) (string, error) {
		switch key {
		case settingKeys.warehouseToken:
			return credential, nil
		case settingKeys.bucket:
			return "example-witness-bucket", nil
		}
		return "", nil
	})
	if err == nil {
		t.Fatal("resolveSettings accepted a group with no warehouse endpoint")
	}
	for _, value := range []string{credential, "example-witness-bucket"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("the refusal echoed a delivered value %q: %v", value, err)
		}
	}
}

// TestResolveSettingsAcceptsAWholeGroup is the positive half: a fully delivered
// group resolves, and each value lands in its own field rather than being
// transposed.
func TestResolveSettingsAcceptsAWholeGroup(t *testing.T) {
	resolved, err := resolveSettings(func(group, key string) (string, error) {
		if group != policyLogConfiguration {
			t.Fatalf("resolveSettings read group %q, want %q", group, policyLogConfiguration)
		}
		switch key {
		case settingKeys.bucket:
			return "example-witness-bucket", nil
		case settingKeys.warehouseEndpoint:
			return "https://warehouse.example.com/ingest", nil
		case settingKeys.warehouseToken:
			return "example-warehouse-token", nil
		}
		t.Fatalf("resolveSettings read an undeclared key %q", key)
		return "", nil
	})
	if err != nil {
		t.Fatalf("resolveSettings: %v", err)
	}
	if resolved.bucket != "example-witness-bucket" ||
		resolved.warehouseEndpoint != "https://warehouse.example.com/ingest" ||
		resolved.warehouseToken != "example-warehouse-token" {
		t.Fatalf("resolveSettings transposed a value: %+v", resolved)
	}
}

type manifest struct {
	Name      string `yaml:"name"`
	Endpoints []struct {
		Name       string `yaml:"name"`
		API        string `yaml:"api"`
		Visibility string `yaml:"visibility"`
	} `yaml:"endpoints"`
	ServiceDependencies []struct {
		Name string `yaml:"name"`
	} `yaml:"service-dependencies"`
	Spec struct {
		ConnectEndpoint   bool     `yaml:"connect-endpoint"`
		ProtocolOutputDir []string `yaml:"protocol-output-dirs"`
	} `yaml:"spec"`
}

func readManifest(t *testing.T) manifest {
	t.Helper()
	body, err := os.ReadFile("../service.codefly.yaml")
	if err != nil {
		t.Fatalf("read the service manifest: %v", err)
	}
	var declared manifest
	if err := yaml.Unmarshal(body, &declared); err != nil {
		t.Fatalf("parse the service manifest: %v", err)
	}
	return declared
}

// TestTheWitnessListenerStaysPrivate is a gate, not a description. The witness
// must be reachable only by the host's declared client through
// workload-authenticated mesh policy: a `visibility` of module or public, a
// second endpoint, a Connect listener or a REST api would each put it in front
// of composed workloads or the public gateway, where a shared credential would
// be enough authority to append.
func TestTheWitnessListenerStaysPrivate(t *testing.T) {
	declared := readManifest(t)
	if len(declared.Endpoints) != 1 {
		t.Fatalf("the witness declares %d endpoints; it must declare exactly the one private listener", len(declared.Endpoints))
	}
	endpoint := declared.Endpoints[0]
	if endpoint.Name != grpcEndpointName {
		t.Errorf("the declared endpoint is %q but the binary looks up %q, so it would bind no port", endpoint.Name, grpcEndpointName)
	}
	if endpoint.Visibility != "" {
		t.Errorf("the witness endpoint declares visibility %q; it must declare none, which is private", endpoint.Visibility)
	}
	if endpoint.API != "" && endpoint.API != "grpc" {
		t.Errorf("the witness endpoint declares api %q; only grpc may be projected", endpoint.API)
	}
	if declared.Spec.ConnectEndpoint {
		t.Error("the witness declares a Connect endpoint, which is browser-reachable through the gateway")
	}
	for _, dependency := range declared.ServiceDependencies {
		t.Errorf("the witness declares a dependency on %q; it depends on nothing in the graph and must not be routed through one", dependency.Name)
	}
}

// TestEveryDeclaredProtocolOutputDirIsGenerated: a declared generated directory
// that no generator writes is a claim nobody can reproduce, and the inverse —
// generated bytes under a directory the manifest does not declare — is output
// Codefly's sync-drift phase does not know it owns.
func TestEveryDeclaredProtocolOutputDirIsGenerated(t *testing.T) {
	declared := readManifest(t)
	if len(declared.Spec.ProtocolOutputDir) == 0 {
		t.Fatal("the manifest declares no protocol output directory, so the agent has no protobuf sync input")
	}
	for _, dir := range declared.Spec.ProtocolOutputDir {
		entries, err := os.ReadDir("../" + dir)
		if err != nil {
			t.Errorf("declared protocol output dir %q does not exist: %v", dir, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("declared protocol output dir %q is empty; nothing generates into it", dir)
		}
	}
}
