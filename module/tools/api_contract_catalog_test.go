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

// findModuleDir returns the directory holding module.codefly.yaml. The tools
// module lives at module/tools, so the module root is the repository root's
// module/ subtree (or the repository root itself in a flat layout).
func findModuleDir(t *testing.T) string {
	t.Helper()
	root := findRepositoryRoot(t)
	candidate := filepath.Join(root, "module")
	if _, err := os.Stat(filepath.Join(candidate, "module.codefly.yaml")); err == nil {
		return candidate
	}
	return root
}
