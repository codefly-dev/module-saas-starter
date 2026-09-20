package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomposition "github.com/codefly-dev/core/composition"
	updatev0 "github.com/codefly-dev/core/generated/go/codefly/update/v0"
	"github.com/codefly-dev/core/runnable"
	"google.golang.org/protobuf/encoding/protojson"
)

// loadBehavioralContracts decodes the declaration exactly as
// composition.BuildPackageContractSnapshot does — same message, same strict
// unmarshal — so a field this repository spells differently from the proto
// fails here rather than at publication.
func loadBehavioralContracts(t *testing.T, moduleRoot string) *updatev0.BehavioralContracts {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot, corecomposition.BehavioralContractsFileName))
	if err != nil {
		t.Fatalf("read behavioral contracts: %v", err)
	}
	contracts := new(updatev0.BehavioralContracts)
	if err := protojson.Unmarshal(data, contracts); err != nil {
		t.Fatalf("decode behavioral contracts: %v", err)
	}
	return contracts
}

func TestBehavioralContractsDeclareCompleteCoverage(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	contracts := loadBehavioralContracts(t, moduleRoot)

	if contracts.SchemaVersion != 1 {
		t.Fatalf("behavioral contracts schema version = %d, want 1", contracts.SchemaVersion)
	}
	// Core reads an absent source file as a derivation failure and an empty set
	// as nothing declared; neither ever means "no behavioural requirements".
	if !contracts.Complete {
		t.Fatal("behavioral contracts must explicitly declare complete coverage")
	}
	if len(contracts.Contracts) == 0 {
		t.Fatal("behavioral contracts declare no contract")
	}

	seen := make(map[string]bool, len(contracts.Contracts))
	for _, contract := range contracts.Contracts {
		if seen[contract.Id] {
			t.Errorf("behavioral contract %q is declared twice", contract.Id)
		}
		seen[contract.Id] = true
		if contract.Contract == nil || len(contract.Contract.Fields) == 0 {
			t.Errorf("behavioral contract %q declares no canonical content", contract.Id)
		}
		if contract.Documentation == "" {
			t.Errorf("behavioral contract %q names no adoption instructions", contract.Id)
		} else if _, err := os.Stat(filepath.Join(moduleRoot, contract.Documentation)); err != nil {
			t.Errorf("behavioral contract %q points outside the shipped tree: %v", contract.Id, err)
		}
	}
}

// TestBehavioralContractsCanonicalizeToStableDigests runs the exact
// canonicalization publication takes the digest over, so content core cannot
// canonicalize is rejected here rather than producing an unpublishable release.
func TestBehavioralContractsCanonicalizeToStableDigests(t *testing.T) {
	for _, contract := range loadBehavioralContracts(t, findModuleRoot(t)).Contracts {
		canonical, err := runnable.CanonicalJSON(contract.Contract)
		if err != nil {
			t.Fatalf("canonicalize %q: %v", contract.Id, err)
		}
		digest := corecomposition.APIContractDigest(canonical)
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
			t.Errorf("contract %q digest %q is not a sha256 digest", contract.Id, digest)
		}
		again, err := runnable.CanonicalJSON(contract.Contract)
		if err != nil {
			t.Fatalf("re-canonicalize %q: %v", contract.Id, err)
		}
		if string(canonical) != string(again) {
			t.Errorf("contract %q does not canonicalize deterministically", contract.Id)
		}
	}
}

// TestBehavioralContractDependenciesResolve holds every declared dependency to
// a real item identity. A dependency core cannot resolve makes the whole
// snapshot unpublishable, and an RPC renamed out from under a behavioural
// contract is exactly the silent drift this declaration exists to catch.
func TestBehavioralContractDependenciesResolve(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	contracts := loadBehavioralContracts(t, moduleRoot)

	catalog, err := corecomposition.LoadAPIContractCatalog(moduleRoot)
	if err != nil {
		t.Fatalf("load API contract catalog: %v", err)
	}
	// Item identities follow core's derivation: an RPC is
	// api/<service>/<endpoint>#/<proto service>/<method>.
	procedures := make(map[string]bool)
	for _, endpoint := range catalog.Endpoints {
		prefix := "api/" + endpoint.Service + "/" + endpoint.Endpoint
		for _, service := range endpoint.Services {
			for _, procedure := range service.Procedures {
				procedures[prefix+"#"+procedure] = true
			}
		}
	}

	declared := make(map[string]bool, len(contracts.Contracts))
	for _, contract := range contracts.Contracts {
		declared["behavior/"+contract.Id] = true
	}
	for _, contract := range contracts.Contracts {
		for _, dependency := range contract.Dependencies {
			switch {
			case strings.HasPrefix(dependency, "behavior/"):
				if !declared[dependency] {
					t.Errorf("contract %q depends on undeclared behavior %q", contract.Id, dependency)
				}
			case strings.HasPrefix(dependency, "api/"):
				if !procedures[dependency] {
					t.Errorf("contract %q depends on %q, which the API contract catalog does not expose", contract.Id, dependency)
				}
			default:
				t.Errorf("contract %q dependency %q is neither a behavioural nor an API item identity", contract.Id, dependency)
			}
		}
	}
}
