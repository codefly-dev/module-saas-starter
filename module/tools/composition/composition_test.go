package composition

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
	corecomposition "github.com/codefly-dev/core/composition"
)

func TestGenerateConsumesCoreInputAndPublishesCompleteCatalog(t *testing.T) {
	fixture := newCoreCompositionFixture(t)
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: fixture.inputPath}); err != nil {
		t.Fatal(err)
	}

	expected, err := corecomposition.LoadContributionInputs(fixture.consumerRoot, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := corecomposition.LoadCatalog(fixture.projection)
	if err != nil {
		t.Fatalf("Core could not load generated catalog: %v", err)
	}
	if err := corecomposition.ValidateCatalog(catalog, expected); err != nil {
		t.Fatalf("Core rejected generated catalog: %v", err)
	}
	for _, claim := range []corecomposition.Claim{
		{Kind: corecomposition.CollisionPackage, Key: "@example/reference-frontend"},
		{Kind: corecomposition.CollisionRoute, Key: "plugin/reference-product"},
		{Kind: corecomposition.CollisionSettingsField, Key: "reference_settings"},
		{Kind: corecomposition.CollisionPermission, Key: "reference.console:read"},
		{Kind: corecomposition.CollisionTopology, Key: "fixture/reference-local"},
	} {
		if !catalogHasClaim(catalog, claim.Kind, claim.Key) {
			t.Errorf("catalog does not claim %s %q", claim.Kind, claim.Key)
		}
	}

	packageJSON := string(readOutput(t, fixture.projection, "services/frontend/code/package.json"))
	if !strings.Contains(packageJSON, `"@example/reference-frontend": "1.0.0"`) {
		t.Fatalf("frontend dependency was not installed in the real package graph:\n%s", packageJSON)
	}
	lock := string(readOutput(t, fixture.projection, "services/frontend/code/package-lock.json"))
	if !strings.Contains(lock, `"node_modules/@example/reference-frontend"`) {
		t.Fatalf("frontend package lock does not install contribution:\n%s", lock)
	}
	frontend := string(readOutput(t, fixture.projection, FrontendOutput))
	if !strings.Contains(frontend, `from "@example/reference-frontend"`) || !strings.Contains(frontend, `plugin: "reference-product"`) {
		t.Fatalf("frontend contribution was not wired into runtime source:\n%s", frontend)
	}
	settings := string(readOutput(t, fixture.projection, SettingsProtoOutput))
	if !strings.Contains(settings, `import "contributed/contributions/settings/reference.proto";`) || !strings.Contains(settings, "example.settings.v1.ReferenceSettings reference_settings = ") {
		t.Fatalf("settings contribution was not wired into composed protobuf:\n%s", settings)
	}
	if _, err := os.Stat(filepath.Join(fixture.projection, "services/accounts/proto/contributed/contributions/settings/reference.proto")); err != nil {
		t.Fatalf("settings source was not staged for protobuf compilation: %v", err)
	}
	command := exec.Command("buf", "build")
	command.Dir = filepath.Join(fixture.projection, "services/accounts/proto")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("staged composed settings do not compile: %v: %s", err, output)
	}
	permissionGo := string(readOutput(t, fixture.projection, PermissionGoOutput))
	if !strings.Contains(permissionGo, `Name: "reference.console:read"`) {
		t.Fatalf("permission contribution was not emitted for Accounts runtime:\n%s", permissionGo)
	}
	fixtureBody := readOutput(t, fixture.projection, "fixtures/reference-local.yaml")
	if !bytes.Contains(fixtureBody, []byte("users:")) {
		t.Fatalf("fixture contribution was not staged as selectable runtime YAML:\n%s", fixtureBody)
	}
}

func TestGenerateIsDeterministicAcrossCleanInstallUninstallAndReinstall(t *testing.T) {
	first := newCoreCompositionFixture(t)
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: first.inputPath}); err != nil {
		t.Fatal(err)
	}
	second := newCoreCompositionFixture(t)
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: second.inputPath}); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, first.projection, second.projection)

	installedContributions := first.descriptor.Contributions
	installedBindings := first.descriptor.Bindings
	first.descriptor.Contributions = corecomposition.Contributions{}
	first.descriptor.Bindings = nil
	writeCoreInput(t, first)
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: first.inputPath}); err != nil {
		t.Fatal(err)
	}
	catalog, err := corecomposition.LoadCatalog(first.projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Inputs) != 0 || len(catalog.Claims) != 0 || len(catalog.Dependencies) != 0 {
		t.Fatalf("uninstall catalog still contains contributions: %+v", catalog)
	}
	if body := string(readOutput(t, first.projection, FrontendInstallOutput)); !strings.Contains(body, `"dependencies": {}`) {
		t.Fatalf("uninstall left generated frontend dependencies:\n%s", body)
	}
	if body := string(readOutput(t, first.projection, "services/frontend/code/package.json")); strings.Contains(body, "@example/reference-frontend") {
		t.Fatalf("uninstall left contribution in frontend package graph:\n%s", body)
	}
	if body := string(readOutput(t, first.projection, "services/frontend/code/package-lock.json")); strings.Contains(body, "node_modules/@example/reference-frontend") {
		t.Fatalf("uninstall left contribution in frontend package lock:\n%s", body)
	}
	for _, stale := range []string{
		"fixtures/reference-local.yaml",
		"services/accounts/proto/contributed/contributions/settings/reference.proto",
		"services/frontend/code/packages/" + frontendWorkspaceName("@example/reference-frontend"),
	} {
		if _, err := os.Lstat(filepath.Join(first.projection, filepath.FromSlash(stale))); !os.IsNotExist(err) {
			t.Errorf("uninstall left staged contribution %q: %v", stale, err)
		}
	}
	if _, err := os.Stat(filepath.Join(first.projection, "services/frontend/code/packages/codefly-contributed-base/package.json")); err != nil {
		t.Fatalf("uninstall removed a base-owned workspace with a generated-looking name: %v", err)
	}

	first.descriptor.Contributions = installedContributions
	first.descriptor.Bindings = installedBindings
	writeCoreInput(t, first)
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: first.inputPath}); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, first.projection, second.projection)
}

func TestGenerateRejectsBaseClaimsBeforeChangingProjection(t *testing.T) {
	tests := map[string]func(*coreCompositionFixture){
		"frontend package": func(f *coreCompositionFixture) {
			writeFile(t, filepath.Join(f.consumerRoot, "contributions/frontend/package.json"), `{"name":"next","version":"1.0.0","codefly":{"frontendPlugin":"external"}}`+"\n")
		},
		"frontend plugin": func(f *coreCompositionFixture) {
			writeFile(t, filepath.Join(f.consumerRoot, "contributions/frontend/package.json"), `{"name":"@example/external","version":"1.0.0","codefly":{"frontendPlugin":"audit"}}`+"\n")
		},
		"fixture": func(f *coreCompositionFixture) {
			oldPath := filepath.Join(f.consumerRoot, "contributions/fixtures/reference-local.yml")
			newPath := filepath.Join(f.consumerRoot, "contributions/fixtures/dev-admin.yml")
			if err := os.Rename(oldPath, newPath); err != nil {
				t.Fatal(err)
			}
			f.descriptor.Contributions.Fixtures[0].Path = "contributions/fixtures/dev-admin.yml"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newCoreCompositionFixture(t)
			mutate(fixture)
			writeCoreInput(t, fixture)
			marker := filepath.Join(fixture.projection, "unchanged")
			writeFile(t, marker, "sentinel\n")
			err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: fixture.inputPath})
			if err == nil || (!strings.Contains(err.Error(), "duplicated") && !strings.Contains(err.Error(), "reserved")) {
				t.Fatalf("Generate() error = %v, want base-claim collision", err)
			}
			if body, readErr := os.ReadFile(marker); readErr != nil || string(body) != "sentinel\n" {
				t.Fatalf("failed validation changed projection before returning: %q, %v", body, readErr)
			}
		})
	}
}

func TestGenerateRejectsUnknownCoreInputFields(t *testing.T) {
	fixture := newCoreCompositionFixture(t)
	body := readOutput(t, filepath.Dir(filepath.Dir(fixture.inputPath)), corecomposition.CompositionInputName)
	body = bytes.Replace(body, []byte("\n}"), []byte(",\n  \"unrecognized\": true\n}"), 1)
	if err := os.WriteFile(fixture.inputPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), InputPath: fixture.inputPath}); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("Generate() error = %v, want strict Core input rejection", err)
	}
}

type coreCompositionFixture struct {
	consumerRoot string
	projection   string
	inputPath    string
	descriptor   *corecomposition.Descriptor
}

func newCoreCompositionFixture(t *testing.T) *coreCompositionFixture {
	t.Helper()
	root := t.TempDir()
	consumer := filepath.Join(root, "consumer")
	projection := filepath.Join(root, "projection")
	if err := os.MkdirAll(filepath.Join(projection, "services/frontend/code/packages"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(projection, "services/frontend/code/packages/codefly-contributed-base/package.json"), `{"name":"@example/base-workspace","version":"1.0.0"}`+"\n")
	writeFile(t, filepath.Join(projection, "services/frontend/code/package.json"), `{
  "name": "projection",
  "version": "1.0.0",
  "private": true,
	"workspaces": ["packages/*"],
  "dependencies": {}
}`+"\n")
	writeFile(t, filepath.Join(projection, "services/accounts/proto/buf.yaml"), "version: v2\nmodules:\n  - path: .\n")
	writeFile(t, filepath.Join(consumer, "contributions/frontend/package.json"), `{
  "name": "@example/reference-frontend",
  "version": "1.0.0",
  "codefly": {"frontendPlugin": "reference-product"}
}`+"\n")
	writeFile(t, filepath.Join(consumer, "contributions/frontend/index.js"), "export const referenceFrontendPlugin = {};\n")
	writeFile(t, filepath.Join(consumer, "contributions/settings/reference.proto"), `syntax = "proto3";
package example.settings.v1;
message ReferenceSettings { string theme = 1; }
`)
	writeFile(t, filepath.Join(consumer, "contributions/permissions/reference.yaml"), `schema: codefly/saas/permissions-contribution/v1
namespace: reference
permissions:
  - {name: "reference.console:read", resource: reference.console, action: read}
`)
	writeFile(t, filepath.Join(consumer, "contributions/fixtures/reference-local.yml"), "users: []\n")
	descriptor := &corecomposition.Descriptor{
		Kind: corecomposition.DescriptorKind,
		Name: "reference-product",
		Base: corecomposition.Base{ID: "codefly/saas-starter", Version: ">=0.1.0 <1.0.0"},
		Contributions: corecomposition.Contributions{
			Frontend:    []corecomposition.FrontendContribution{{Path: "contributions/frontend", Export: "referenceFrontendPlugin"}},
			Settings:    []corecomposition.SettingsContribution{{Path: "contributions/settings/reference.proto", Message: "example.settings.v1.ReferenceSettings"}},
			Permissions: []corecomposition.PathContribution{{Path: "contributions/permissions/reference.yaml"}},
			Fixtures:    []corecomposition.PathContribution{{Path: "contributions/fixtures/reference-local.yml"}},
		},
		Bindings: []corecomposition.Binding{{
			Plugin: "reference-product", Alias: "api",
			Target: corecomposition.BindingTarget{Module: "reference", Service: "api"},
		}},
	}
	fixture := &coreCompositionFixture{
		consumerRoot: consumer,
		projection:   projection,
		inputPath:    filepath.Join(projection, filepath.FromSlash(corecomposition.CompositionInputName)),
		descriptor:   descriptor,
	}
	writeCoreInput(t, fixture)
	return fixture
}

func writeCoreInput(t *testing.T, fixture *coreCompositionFixture) {
	t.Helper()
	input := corecomposition.CompositionInput{
		Schema:       "codefly/composition-input/v2",
		Module:       fixture.descriptor.Name,
		Package:      "codefly/saas-starter",
		Version:      "0.1.0",
		ConsumerRoot: fixture.consumerRoot,
		Projection:   fixture.projection,
		Contracts: map[string]string{
			corecomposition.ContractComposition:    "2.0.0",
			corecomposition.ContractFrontendPlugin: "1.0.0",
			corecomposition.ContractSettings:       "1.0.0",
			corecomposition.ContractPermissions:    "1.0.0",
			corecomposition.ContractFixtures:       "1.0.0",
		},
		Descriptor: fixture.descriptor,
	}
	body, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, fixture.inputPath, string(append(body, '\n')))
}

func catalogHasClaim(catalog *corecomposition.Catalog, kind corecomposition.CollisionKind, key string) bool {
	for _, claim := range catalog.Claims {
		if claim.Kind == kind && claim.Key == key {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOutput(t *testing.T, root, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertSameOutputs(t *testing.T, left, right string) {
	t.Helper()
	for _, output := range []string{
		FrontendOutput,
		FrontendInstallOutput,
		SettingsProtoOutput,
		SettingsGoOutput,
		SettingsTypeScriptOut,
		PermissionsOutput,
		PermissionGoOutput,
		FixturesOutput,
		TopologyBindingsOutput,
		CompositionCatalogOut,
	} {
		leftBody := readOutput(t, left, output)
		rightBody := readOutput(t, right, output)
		if output == CompositionCatalogOut {
			var leftCatalog, rightCatalog corecomposition.Catalog
			if err := json.Unmarshal(leftBody, &leftCatalog); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(rightBody, &rightCatalog); err != nil {
				t.Fatal(err)
			}
			for index := range leftCatalog.Inputs {
				leftCatalog.Inputs[index].Path = filepath.Base(leftCatalog.Inputs[index].Path)
				rightCatalog.Inputs[index].Path = filepath.Base(rightCatalog.Inputs[index].Path)
			}
			leftBody, _ = json.Marshal(leftCatalog)
			rightBody, _ = json.Marshal(rightCatalog)
		}
		if !bytes.Equal(leftBody, rightBody) {
			t.Errorf("composition output %s is not deterministic", output)
		}
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Clean(filepath.Join(directory, "..", ".."))
		if _, err := os.Stat(filepath.Join(candidate, modulepackage.ManifestName)); err == nil {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("module root not found")
		}
		directory = parent
	}
}
