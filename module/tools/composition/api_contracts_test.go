package composition

import (
	"os"
	"path/filepath"
	"testing"

	corecomposition "github.com/codefly-dev/core/composition"
	"gopkg.in/yaml.v3"
)

// moduleInterfaceDocument is the minimal view of module.codefly.yaml this test
// needs: the formally declared interface endpoints. Parsing the file directly
// (rather than through the full resources.Module loader, which post-loads every
// service) keeps the invariant honest — it reads exactly the bytes a consumer
// sees — and dependency-light.
type moduleInterfaceDocument struct {
	Interface struct {
		Endpoints []struct {
			Service    string `yaml:"service"`
			Endpoint   string `yaml:"endpoint"`
			Visibility string `yaml:"visibility"`
		} `yaml:"endpoints"`
	} `yaml:"interface"`
}

func loadModuleInterfaceEndpoints(t *testing.T, moduleRoot string) map[string]struct{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot, "module.codefly.yaml"))
	if err != nil {
		t.Fatalf("read module.codefly.yaml: %v", err)
	}
	var document moduleInterfaceDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode module.codefly.yaml interface: %v", err)
	}
	endpoints := make(map[string]struct{}, len(document.Interface.Endpoints))
	for _, endpoint := range document.Interface.Endpoints {
		endpoints[endpoint.Service+"\x00"+endpoint.Endpoint] = struct{}{}
	}
	if len(endpoints) == 0 {
		t.Fatal("module interface declares no endpoints")
	}
	return endpoints
}

// TestPackageAPIContractsAreASubsetOfTheModuleInterface guards the invariant
// #483 introduces: every endpoint the package publishes a machine-readable API
// contract for must be one the module formally exposes on its interface. Core's
// ValidatePackageAPIContracts only cross-checks manifest ⇄ catalog ⇄ on-disk
// digests; it does not tie contracts back to the interface. Without this gate a
// package could ship a client contract for an endpoint it never actually
// exposes to consumers.
func TestPackageAPIContractsAreASubsetOfTheModuleInterface(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	manifest, err := corecomposition.LoadPackageManifest(moduleRoot)
	if err != nil {
		t.Fatalf("load package manifest: %v", err)
	}
	interfaceEndpoints := loadModuleInterfaceEndpoints(t, moduleRoot)

	contractCount := 0
	for _, service := range manifest.Services {
		for _, contract := range service.APIContracts {
			contractCount++
			key := service.Name + "\x00" + contract.Endpoint
			if _, exposed := interfaceEndpoints[key]; !exposed {
				t.Errorf("package publishes an API contract for %s/%s, which the module interface does not expose", service.Name, contract.Endpoint)
			}
		}
	}
	if contractCount == 0 {
		t.Fatal("package manifest declares no api-contracts; expected at least the accounts/connect export")
	}
}

// TestPackageAPIContractCatalogDigestsRecomputeFromContractBytes proves the
// published catalog is not stale: each endpoint's digest recomputes from the
// contract file on disk, and the manifest and catalog agree end to end. This is
// the guard that trips if someone regenerates a contract's .binpb without
// refreshing catalog.codefly.json (or vice versa).
func TestPackageAPIContractCatalogDigestsRecomputeFromContractBytes(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	manifest, err := corecomposition.LoadPackageManifest(moduleRoot)
	if err != nil {
		t.Fatalf("load package manifest: %v", err)
	}
	catalog, err := corecomposition.LoadAPIContractCatalog(moduleRoot)
	if err != nil {
		t.Fatalf("load API contract catalog: %v", err)
	}

	if len(catalog.Endpoints) == 0 {
		t.Fatal("API contract catalog declares no endpoints")
	}
	for _, endpoint := range catalog.Endpoints {
		data, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(endpoint.Path)))
		if err != nil {
			t.Fatalf("read contract file %q: %v", endpoint.Path, err)
		}
		if digest := corecomposition.APIContractDigest(data); digest != endpoint.Digest {
			t.Errorf("catalog digest for %s/%s is stale: recomputed %s from %q, catalog records %s", endpoint.Service, endpoint.Endpoint, digest, endpoint.Path, endpoint.Digest)
		}
	}

	// Ties the manifest, the catalog, and the on-disk contract files together:
	// every manifest api-contract has a matching catalog entry with identical
	// path/kind/package/digest, and no catalog entry is left undeclared.
	if err := corecomposition.ValidatePackageAPIContracts(moduleRoot, manifest, catalog); err != nil {
		t.Fatalf("package API contracts do not validate against the catalog: %v", err)
	}

	// The canonical digest of the whole catalog is deterministic — recomputing
	// it yields the same bytes, so the drift gate that compares it is stable.
	first, err := catalog.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalize catalog: %v", err)
	}
	second, err := catalog.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalize catalog (second pass): %v", err)
	}
	if corecomposition.APIContractDigest(first) != corecomposition.APIContractDigest(second) {
		t.Fatal("catalog canonical digest is not deterministic")
	}
}
