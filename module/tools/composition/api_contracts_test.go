package composition

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corecomposition "github.com/codefly-dev/core/composition"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
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

// generatedLibraryDocument is the minimal view of a `library.codefly.yaml` this
// test needs: where each language's bindings were written, which package
// contract the library was built from, and the digest of the contract bytes it
// was built against.
type generatedLibraryDocument struct {
	Languages []struct {
		Name string `yaml:"name"`
		Path string `yaml:"path"`
	} `yaml:"languages"`
	Sources []struct {
		Package        string `yaml:"package"`
		Service        string `yaml:"service"`
		Endpoint       string `yaml:"endpoint"`
		ContractDigest string `yaml:"contract-digest"`
	} `yaml:"sources"`
}

// declaredPackages renders the package ids a library claims to be generated
// from, for the diagnostic on a library this gate cannot match.
func (d generatedLibraryDocument) declaredPackages() string {
	if len(d.Sources) == 0 {
		return "none"
	}
	seen := make([]string, 0, len(d.Sources))
	for _, source := range d.Sources {
		if !slices.Contains(seen, source.Package) {
			seen = append(seen, source.Package)
		}
	}
	return strings.Join(seen, ", ")
}

// contractProtoFiles lists the proto file names inside a serialized
// FileDescriptorSet, in declaration order.
func contractProtoFiles(data []byte) ([]string, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(set.GetFile()))
	for _, file := range set.GetFile() {
		names = append(names, file.GetName())
	}
	return names, nil
}

// findGeneratedLibraries walks the module for every `library.codefly.yaml`.
// Installed dependencies and build output can contain copies of a library
// manifest that no one in this repository regenerates, so both are skipped.
func findGeneratedLibraries(t *testing.T, moduleRoot string) []string {
	t.Helper()
	var manifests []string
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "dist", ".next":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "library.codefly.yaml" {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module for generated libraries: %v", err)
	}
	return manifests
}

// TestGeneratedLibraryContractDigestsMatchThePackage proves no generated client
// library has fallen behind the contract it is generated from. A library vendors
// its own copy of the contract and records that copy's digest; nothing in the
// existing pipeline reads it back. `codefly generate contracts --check` compares
// the exported contract against the service protos and stops there, and
// `module-package run-generators` never enters a library directory — so a
// library's bindings can lag the wire indefinitely, silently, and a consumer
// generated against them will not see fields the API already serves. That is
// exactly what happened to @codefly-dev/saas-sdk between #515 and #585.
func TestGeneratedLibraryContractDigestsMatchThePackage(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	manifest, err := corecomposition.LoadPackageManifest(moduleRoot)
	if err != nil {
		t.Fatalf("load package manifest: %v", err)
	}
	published := make(map[string]string)
	for _, service := range manifest.Services {
		for _, contract := range service.APIContracts {
			published[service.Name+"\x00"+contract.Endpoint] = contract.Digest
		}
	}

	libraries := findGeneratedLibraries(t, moduleRoot)
	if len(libraries) == 0 {
		t.Fatal("no library.codefly.yaml found; expected at least the saas-sdk client library")
	}

	checked := 0
	for _, path := range libraries {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		var document generatedLibraryDocument
		if err := yaml.Unmarshal(data, &document); err != nil {
			t.Fatalf("decode %q: %v", path, err)
		}
		relative, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			t.Fatalf("relativize %q: %v", path, err)
		}
		matched := 0
		for _, source := range document.Sources {
			if source.Package != manifest.ID {
				continue
			}
			matched++
			checked++
			key := source.Service + "\x00" + source.Endpoint
			digest, exported := published[key]
			if !exported {
				t.Errorf("%s is generated from %s/%s, which this package publishes no API contract for", relative, source.Service, source.Endpoint)
				continue
			}
			if source.ContractDigest != digest {
				t.Errorf("%s is stale: generated from %s/%s at %s, the package publishes %s — regenerate the library", relative, source.Service, source.Endpoint, source.ContractDigest, digest)
				continue
			}
			assertLibraryBindingsMatchContract(t, moduleRoot, path, relative, document, digest)
		}
		// A library this gate silently skips is a library that can rot. The one
		// in the tree matches, so this cannot fire today; it fires the first time
		// someone adds a library whose provenance does not name this package —
		// either the package id drifted, or the library is genuinely foreign and
		// this gate has to learn how to check it. Both want a human.
		if matched == 0 {
			t.Errorf("%s records no source from %s (declares: %s); this gate cannot check it", relative, manifest.ID, document.declaredPackages())
		}
	}
	if checked == 0 {
		t.Fatalf("no generated library records a source from %s; expected at least the saas-sdk accounts/connect client", manifest.ID)
	}
}

// assertLibraryBindingsMatchContract checks the two things the recorded digest
// cannot: that the contract the library actually vendors is the published one,
// and that the bindings on disk cover every proto in it.
//
// The recorded `contract-digest` is one string in one YAML file. Clearing this
// gate by editing that string is a one-line change, and the regeneration it
// stands in for is not one line — so the string alone is an invitation, not a
// guarantee. These two checks are what make the gate about the artifact.
func assertLibraryBindingsMatchContract(t *testing.T, moduleRoot, libraryPath, relative string, document generatedLibraryDocument, digest string) {
	t.Helper()

	libraryDir := filepath.Dir(libraryPath)
	vendored := filepath.Join(libraryDir, "contract", "contract.binpb")
	data, err := os.ReadFile(vendored)
	if err != nil {
		t.Errorf("%s records a contract digest but its vendored contract is unreadable: %v", relative, err)
		return
	}
	if vendoredDigest := corecomposition.APIContractDigest(data); vendoredDigest != digest {
		t.Errorf("%s records %s but the contract it vendors hashes to %s — the digest was edited without regenerating", relative, digest, vendoredDigest)
		return
	}

	files, err := contractProtoFiles(data)
	if err != nil {
		t.Errorf("%s: decode vendored contract: %v", relative, err)
		return
	}
	bindings, ok := typescriptBindingsRoot(libraryDir, document)
	if !ok {
		return
	}
	for _, file := range files {
		// Well-known types are not generated into the tree: the bindings import
		// them from `@bufbuild/protobuf/wkt`. Everything else the contract names
		// has to be on disk, or the library serves a contract it cannot describe.
		if strings.HasPrefix(file, "google/protobuf/") {
			continue
		}
		binding := filepath.Join(bindings, filepath.FromSlash(strings.TrimSuffix(file, ".proto")+"_pb.ts"))
		if _, err := os.Stat(binding); err != nil {
			missing, relErr := filepath.Rel(moduleRoot, binding)
			if relErr != nil {
				missing = binding
			}
			t.Errorf("%s vendors %s but has no bindings for it (%s missing) — regenerate the library", relative, file, missing)
		}
	}
}

// typescriptBindingsRoot resolves where a library's TypeScript bindings live.
// The language path comes from the manifest; `src/gen` under it is the layout
// `codefly generate client` writes. Reports false for a library that generates
// no TypeScript, which has no bindings for this check to look at.
func typescriptBindingsRoot(libraryDir string, document generatedLibraryDocument) (string, bool) {
	for _, language := range document.Languages {
		if language.Name == "typescript" {
			return filepath.Join(libraryDir, filepath.FromSlash(language.Path), "src", "gen"), true
		}
	}
	return "", false
}
