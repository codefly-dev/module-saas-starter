package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	corecomposition "github.com/codefly-dev/core/composition"
)

// serviceCatalog is the shape of module/services/accounts/generated/
// service-catalog.json (schema saas.catalog.v1) — the generator handoff inside
// this module. Only the procedures are needed here.
type serviceCatalog struct {
	SchemaVersion string `json:"schema_version"`
	Services      []struct {
		FullName   string   `json:"full_name"`
		Procedures []string `json:"procedures"`
	} `json:"services"`
}

// TestServiceCatalogProceduresAppearInAPIContractCatalog ties the two handoffs
// together: service-catalog.json (saas.catalog.v1, the in-module generator
// handoff) and contracts/api/catalog.codefly.json (the cross-repo contract
// handoff). Every procedure the module generates for itself must also be
// exported in the published contract catalog, so a consumer generating a client
// from the package reaches the same surface the module builds against.
func TestServiceCatalogProceduresAppearInAPIContractCatalog(t *testing.T) {
	moduleDir := findModuleDir(t)

	catalog, err := corecomposition.LoadAPIContractCatalog(moduleDir)
	if err != nil {
		t.Fatalf("load API contract catalog: %v", err)
	}
	exported := make(map[string]struct{})
	for _, endpoint := range catalog.Endpoints {
		for _, service := range endpoint.Services {
			for _, procedure := range service.Procedures {
				exported[procedure] = struct{}{}
			}
		}
	}
	if len(exported) == 0 {
		t.Fatal("API contract catalog exports no procedures")
	}

	data, err := os.ReadFile(filepath.Join(moduleDir, "services/accounts/generated/service-catalog.json"))
	if err != nil {
		t.Fatalf("read service catalog: %v", err)
	}
	var generated serviceCatalog
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode service catalog: %v", err)
	}

	procedureCount := 0
	for _, service := range generated.Services {
		for _, procedure := range service.Procedures {
			procedureCount++
			if _, ok := exported[procedure]; !ok {
				t.Errorf("service catalog procedure %q is not exported in the API contract catalog", procedure)
			}
		}
	}
	if procedureCount == 0 {
		t.Fatal("service catalog declares no procedures")
	}
}

// findModuleDir returns the nearest ancestor of the working directory that
// holds module.codefly.yaml. It walks up rather than deriving the module from
// the repository root, because these tests also run wherever a consumer base-
// syncs this module to: module/ here, the root in a flat layout, and
// modules/<name>/ in an aggregator workspace. Resolving "repository root
// + module/" broke the aggregator case by falling back to the workspace root,
// which holds no contracts/ or services/ tree.
func findModuleDir(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "module.codefly.yaml")); statErr == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("module.codefly.yaml not found above the working directory")
		}
		directory = parent
	}
}
