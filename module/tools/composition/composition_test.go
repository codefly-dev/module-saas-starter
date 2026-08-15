package composition

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
)

func TestGenerateComposesEverySupportedContributionAndIsReversible(t *testing.T) {
	inputs := writeReferenceContributions(t)
	root := t.TempDir()
	first := filepath.Join(root, "first")
	options := Options{
		ModuleRoot:  findModuleRoot(t),
		OutputRoot:  first,
		Frontend:    []string{inputs["frontend"]},
		Settings:    []string{inputs["settings"]},
		Permissions: []string{inputs["permissions"]},
		Fixtures:    []string{inputs["fixtures"]},
		Topology:    []string{inputs["topology"]},
	}
	if err := Generate(options); err != nil {
		t.Fatal(err)
	}
	frontend := readOutput(t, first, FrontendOutput)
	for _, wanted := range []string{`from "@example/reference-frontend"`, "referenceFrontendPlugin", `plugin: "reference-product"`} {
		if !strings.Contains(string(frontend), wanted) {
			t.Errorf("frontend projection does not contain %q", wanted)
		}
	}
	settings := readOutput(t, first, SettingsProtoOutput)
	for _, wanted := range []string{"saas.settings.v1.CommonSettings common = 1", "example.settings.v1.ReferenceSettings reference = 1000"} {
		if !strings.Contains(string(settings), wanted) {
			t.Errorf("settings projection does not contain %q", wanted)
		}
	}

	second := filepath.Join(root, "second")
	options.OutputRoot = second
	if err := Generate(options); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, first, second)

	if err := Generate(Options{ModuleRoot: findModuleRoot(t), OutputRoot: first}); err != nil {
		t.Fatal(err)
	}
	if body := readOutput(t, first, FrontendInstallOutput); !strings.Contains(string(body), `"dependencies": {}`) {
		t.Fatalf("uninstall left a frontend dependency:\n%s", body)
	}
	if err := Generate(optionsWithOutput(options, first)); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, first, second)
}

func TestGenerateRejectsUnknownFieldsAndContributionCollisions(t *testing.T) {
	inputs := writeReferenceContributions(t)
	unknown := writeInput(t, "unknown.yaml", `schema: codefly/saas/frontend-contribution/v2
package: "@example/reference-frontend"
version: 1.0.0
export: referenceFrontendPlugin
plugin: reference-product
root-package-edit: true
`)
	if err := Generate(Options{ModuleRoot: findModuleRoot(t), OutputRoot: t.TempDir(), Frontend: []string{unknown}}); err == nil || !strings.Contains(err.Error(), "root-package-edit") {
		t.Fatalf("Generate() error = %v, want unknown field rejection", err)
	}

	for name, mutate := range map[string]func(Options) Options{
		"frontend plugin": func(options Options) Options {
			options.Frontend = append(options.Frontend, inputs["frontend"])
			return options
		},
		"settings range": func(options Options) Options {
			options.Settings = append(options.Settings, writeInput(t, "settings-overlap.yaml", `schema: codefly/saas/settings-contribution/v1
namespace: other
proto-import: example/settings/v1/other.proto
message: example.settings.v1.OtherSettings
field: {name: other, number: 1001}
field-range: {start: 1001, end: 1100}
`))
			return options
		},
		"permission": func(options Options) Options {
			options.Permissions = append(options.Permissions, inputs["permissions"])
			return options
		},
		"fixture": func(options Options) Options {
			options.Fixtures = append(options.Fixtures, inputs["fixtures"])
			return options
		},
		"topology binding": func(options Options) Options {
			options.Topology = append(options.Topology, inputs["topology"])
			return options
		},
	} {
		t.Run(name, func(t *testing.T) {
			options := Options{
				ModuleRoot:  findModuleRoot(t),
				OutputRoot:  t.TempDir(),
				Frontend:    []string{inputs["frontend"]},
				Settings:    []string{inputs["settings"]},
				Permissions: []string{inputs["permissions"]},
				Fixtures:    []string{inputs["fixtures"]},
				Topology:    []string{inputs["topology"]},
			}
			if err := Generate(mutate(options)); err == nil || !strings.Contains(err.Error(), "duplicated") && !strings.Contains(err.Error(), "collides") {
				t.Fatalf("Generate() error = %v, want collision rejection", err)
			}
		})
	}
}

func TestGenerateRejectsRawOrUnknownTopologyTargets(t *testing.T) {
	inputs := writeReferenceContributions(t)
	for name, topology := range map[string]string{
		"unknown plugin": `schema: codefly/saas/topology-contribution/v1
bindings:
  - plugin: missing-product
    alias: api
    target: {module: reference, service: api}
`,
		"raw url": `schema: codefly/saas/topology-contribution/v1
bindings:
  - plugin: reference-product
    alias: api
    target: {module: "https://example.com", service: api}
`,
		"non-Codefly identity": `schema: codefly/saas/topology-contribution/v1
bindings:
  - plugin: reference-product
    alias: api
    target: {module: reference.product, service: api}
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeInput(t, name+".yaml", topology)
			err := Generate(Options{ModuleRoot: findModuleRoot(t), OutputRoot: t.TempDir(), Frontend: []string{inputs["frontend"]}, Topology: []string{path}})
			if err == nil {
				t.Fatal("Generate() accepted an invalid topology target")
			}
		})
	}
}

func TestGenerateRejectsFixtureNamesTheRuntimeCannotSelect(t *testing.T) {
	document := writeInput(t, "invalid-fixture.yaml", `schema: codefly/saas/fixtures-contribution/v1
namespace: reference
fixtures:
  - {name: reference.local, path: fixtures/reference.yaml}
`)
	fixturePath := filepath.Join(filepath.Dir(document), "fixtures", "reference.yaml")
	if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, []byte("users: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Generate(Options{
		ModuleRoot: findModuleRoot(t),
		OutputRoot: t.TempDir(),
		Fixtures:   []string{document},
	})
	if err == nil {
		t.Fatal("Generate() accepted a fixture name the Accounts runtime cannot select")
	}
}

func TestGenerateRejectsPackageOwnedNamespaces(t *testing.T) {
	for name, options := range map[string]Options{
		"package": {
			Frontend: []string{writeInput(t, "reserved-frontend.yaml", `schema: codefly/saas/frontend-contribution/v2
package: "@codefly/saas-product"
version: 1.0.0
export: productPlugin
plugin: product
`)},
		},
		"settings": {
			Settings: []string{writeInput(t, "reserved-settings.yaml", `schema: codefly/saas/settings-contribution/v1
namespace: common
proto-import: example/settings/v1/product.proto
message: example.settings.v1.ProductSettings
field: {name: product, number: 1000}
field-range: {start: 1000, end: 1099}
`)},
		},
		"protobuf": {
			Settings: []string{writeInput(t, "reserved-protobuf.yaml", `schema: codefly/saas/settings-contribution/v1
namespace: product
proto-import: saas/product/settings/v1/product.proto
message: saas.product.settings.v1.ProductSettings
field: {name: product, number: 1000}
field-range: {start: 1000, end: 1099}
`)},
		},
		"permission": {
			Permissions: []string{writeInput(t, "reserved-permission.yaml", `schema: codefly/saas/permissions-contribution/v1
namespace: saas
permissions:
  - {name: "saas.product:read", resource: saas.product, action: read}
`)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			options.ModuleRoot = findModuleRoot(t)
			options.OutputRoot = t.TempDir()
			if err := Generate(options); err == nil {
				t.Fatal("Generate() accepted a package-owned namespace")
			}
		})
	}
}

func writeReferenceContributions(t *testing.T) map[string]string {
	t.Helper()
	fixtureDocument := writeInput(t, "fixtures.yaml", `schema: codefly/saas/fixtures-contribution/v1
namespace: reference
fixtures:
  - {name: reference-local, path: fixtures/reference-local.yaml}
`)
	fixturePath := filepath.Join(filepath.Dir(fixtureDocument), "fixtures", "reference-local.yaml")
	if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, []byte("users: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"frontend": writeInput(t, "frontend.yaml", `schema: codefly/saas/frontend-contribution/v2
package: "@example/reference-frontend"
version: 1.0.0
export: referenceFrontendPlugin
plugin: reference-product
`),
		"settings": writeInput(t, "settings.yaml", `schema: codefly/saas/settings-contribution/v1
namespace: reference
proto-import: example/settings/v1/reference.proto
message: example.settings.v1.ReferenceSettings
field: {name: reference, number: 1000}
field-range: {start: 1000, end: 1099}
`),
		"permissions": writeInput(t, "permissions.yaml", `schema: codefly/saas/permissions-contribution/v1
namespace: reference
permissions:
  - {name: "reference.console:read", resource: reference.console, action: read}
`),
		"fixtures": fixtureDocument,
		"topology": writeInput(t, "topology.yaml", `schema: codefly/saas/topology-contribution/v1
bindings:
  - plugin: reference-product
    alias: api
    target: {module: reference, service: api}
`),
	}
}

func writeInput(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func optionsWithOutput(options Options, output string) Options {
	options.OutputRoot = output
	return options
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
		FixturesOutput,
		TopologyBindingsOutput,
	} {
		if !bytes.Equal(readOutput(t, left, output), readOutput(t, right, output)) {
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
