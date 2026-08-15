package composition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
	"gopkg.in/yaml.v3"
)

const (
	FrontendOutput         = "services/frontend/code/src/generated/frontend-contributions.ts"
	FrontendInstallOutput  = "services/frontend/code/frontend.install.generated.json"
	SettingsProtoOutput    = "services/accounts/proto/saas/composed/settings/v1/settings.proto"
	SettingsGoOutput       = "services/accounts/code/pkg/settingscatalog/catalog_gen.go"
	SettingsTypeScriptOut  = "services/frontend/code/src/gen/saas/settings/v1/field_catalog.ts"
	PermissionsOutput      = "deployment/generated/contributed-permissions.json"
	FixturesOutput         = "deployment/generated/contributed-fixtures.json"
	TopologyBindingsOutput = "deployment/generated/contributed-topology.json"
)

var (
	logicalIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	codeflyIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	fixtureNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)
	packagePattern     = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	protoNamePattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)
	fieldNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	exportNamePattern  = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
)

type Options struct {
	ModuleRoot  string
	OutputRoot  string
	Frontend    []string
	Settings    []string
	Permissions []string
	Fixtures    []string
	Topology    []string
}

type FrontendContribution struct {
	Schema  string `yaml:"schema"`
	Package string `yaml:"package"`
	Version string `yaml:"version"`
	Export  string `yaml:"export"`
	Plugin  string `yaml:"plugin"`
}

type SettingsContribution struct {
	Schema      string        `yaml:"schema"`
	Namespace   string        `yaml:"namespace"`
	ProtoImport string        `yaml:"proto-import"`
	Message     string        `yaml:"message"`
	Field       SettingsField `yaml:"field"`
	FieldRange  NumericRange  `yaml:"field-range"`
}

type SettingsField struct {
	Name   string `yaml:"name"`
	Number int    `yaml:"number"`
}

type NumericRange struct {
	Start int `yaml:"start"`
	End   int `yaml:"end"`
}

type PermissionsContribution struct {
	Schema      string       `yaml:"schema"`
	Namespace   string       `yaml:"namespace"`
	Permissions []Permission `yaml:"permissions"`
}

type Permission struct {
	Name     string `yaml:"name" json:"name"`
	Resource string `yaml:"resource" json:"resource"`
	Action   string `yaml:"action" json:"action"`
}

type FixturesContribution struct {
	Schema    string            `yaml:"schema"`
	Namespace string            `yaml:"namespace"`
	Fixtures  []FixtureDocument `yaml:"fixtures"`
}

type FixtureDocument struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

type TopologyContribution struct {
	Schema   string                   `yaml:"schema"`
	Bindings []FrontendServiceBinding `yaml:"bindings"`
}

type FrontendServiceBinding struct {
	Plugin string                `yaml:"plugin" json:"plugin"`
	Alias  string                `yaml:"alias" json:"alias"`
	Target FrontendServiceTarget `yaml:"target" json:"target"`
}

type FrontendServiceTarget struct {
	Module  string `yaml:"module" json:"module"`
	Service string `yaml:"service" json:"service"`
}

type permissionCatalog struct {
	Schema      string       `json:"schema"`
	Permissions []Permission `json:"permissions"`
}

type fixtureCatalog struct {
	Schema   string            `json:"schema"`
	Fixtures []FixtureDocument `json:"fixtures"`
}

type topologyCatalog struct {
	Schema   string                   `json:"schema"`
	Bindings []FrontendServiceBinding `json:"bindings"`
}

func Generate(options Options) error {
	manifest, err := modulepackage.ReadManifest(options.ModuleRoot)
	if err != nil {
		return err
	}
	frontends, err := readDocuments[FrontendContribution](options.Frontend)
	if err != nil {
		return err
	}
	settings, err := readDocuments[SettingsContribution](options.Settings)
	if err != nil {
		return err
	}
	permissions, err := readDocuments[PermissionsContribution](options.Permissions)
	if err != nil {
		return err
	}
	fixtures, err := readDocuments[FixturesContribution](options.Fixtures)
	if err != nil {
		return err
	}
	if err := validateFixturePaths(options.Fixtures, fixtures); err != nil {
		return err
	}
	topologies, err := readDocuments[TopologyContribution](options.Topology)
	if err != nil {
		return err
	}
	if err := validate(frontends, settings, permissions, fixtures, topologies, manifest.ReservedNamespaces); err != nil {
		return err
	}
	files, err := render(frontends, settings, permissions, fixtures, topologies)
	if err != nil {
		return err
	}
	for path, body := range files {
		absolute := filepath.Join(options.OutputRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return fmt.Errorf("create composition output directory: %w", err)
		}
		if err := os.WriteFile(absolute, body, 0o644); err != nil {
			return fmt.Errorf("write composition output %q: %w", path, err)
		}
	}
	return nil
}

func readDocuments[T any](paths []string) ([]T, error) {
	documents := make([]T, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("read contribution %q: %w", path, err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(body))
		decoder.KnownFields(true)
		var document T
		if err := decoder.Decode(&document); err != nil {
			return nil, fmt.Errorf("decode contribution %q: %w", path, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("decode contribution %q: multiple YAML documents are not allowed", path)
			}
			return nil, fmt.Errorf("decode contribution %q: %w", path, err)
		}
		documents = append(documents, document)
	}
	return documents, nil
}

func validate(frontends []FrontendContribution, settings []SettingsContribution, permissions []PermissionsContribution, fixtures []FixturesContribution, topologies []TopologyContribution, reserved []modulepackage.ReservedNamespace) error {
	plugins := map[string]struct{}{}
	packages := map[string]struct{}{}
	for _, contribution := range frontends {
		if contribution.Schema != "codefly/saas/frontend-contribution/v2" {
			return fmt.Errorf("frontend contribution schema must be codefly/saas/frontend-contribution/v2")
		}
		if !packagePattern.MatchString(contribution.Package) || isReserved(reserved, "package", contribution.Package) {
			return fmt.Errorf("frontend package %q is invalid or reserved", contribution.Package)
		}
		if !modulepackage.IsSemanticVersion(contribution.Version) {
			return fmt.Errorf("frontend package %q must declare an exact semantic version", contribution.Package)
		}
		if !exportNamePattern.MatchString(contribution.Export) || !logicalIDPattern.MatchString(contribution.Plugin) {
			return fmt.Errorf("frontend contribution %q has an invalid export or plugin identity", contribution.Package)
		}
		if _, duplicate := packages[contribution.Package]; duplicate {
			return fmt.Errorf("frontend package %q is duplicated", contribution.Package)
		}
		if _, duplicate := plugins[contribution.Plugin]; duplicate {
			return fmt.Errorf("frontend plugin %q is duplicated", contribution.Plugin)
		}
		packages[contribution.Package] = struct{}{}
		plugins[contribution.Plugin] = struct{}{}
	}

	namespaces := map[string]struct{}{}
	ranges := []NumericRange{{Start: 1, End: 999}}
	fieldNumbers := map[int]struct{}{1: {}}
	fieldNames := map[string]struct{}{"common": {}}
	for _, contribution := range settings {
		if contribution.Schema != "codefly/saas/settings-contribution/v1" {
			return fmt.Errorf("settings contribution schema must be codefly/saas/settings-contribution/v1")
		}
		if !logicalIDPattern.MatchString(contribution.Namespace) || isReserved(reserved, "settings", contribution.Namespace) || !protoNamePattern.MatchString(contribution.Message) || isReserved(reserved, "protobuf", contribution.Message) || !safeRelativePath(contribution.ProtoImport) || filepath.Ext(contribution.ProtoImport) != ".proto" {
			return fmt.Errorf("settings contribution %q has an invalid namespace or protobuf descriptor", contribution.Namespace)
		}
		if _, duplicate := namespaces[contribution.Namespace]; duplicate {
			return fmt.Errorf("settings namespace %q is duplicated or reserved", contribution.Namespace)
		}
		if !fieldNamePattern.MatchString(contribution.Field.Name) || contribution.Field.Number < contribution.FieldRange.Start || contribution.Field.Number > contribution.FieldRange.End || contribution.FieldRange.Start < 1000 || contribution.FieldRange.End > 536870911 || contribution.FieldRange.Start <= 19999 && contribution.FieldRange.End >= 19000 {
			return fmt.Errorf("settings namespace %q has an invalid field or numeric range", contribution.Namespace)
		}
		if _, duplicate := fieldNumbers[contribution.Field.Number]; duplicate {
			return fmt.Errorf("settings field number %d is duplicated", contribution.Field.Number)
		}
		if _, duplicate := fieldNames[contribution.Field.Name]; duplicate {
			return fmt.Errorf("settings field name %q is duplicated", contribution.Field.Name)
		}
		for _, existing := range ranges {
			if contribution.FieldRange.Start <= existing.End && existing.Start <= contribution.FieldRange.End {
				return fmt.Errorf("settings numeric range %d-%d collides with %d-%d", contribution.FieldRange.Start, contribution.FieldRange.End, existing.Start, existing.End)
			}
		}
		namespaces[contribution.Namespace] = struct{}{}
		fieldNumbers[contribution.Field.Number] = struct{}{}
		fieldNames[contribution.Field.Name] = struct{}{}
		ranges = append(ranges, contribution.FieldRange)
	}

	permissionNames := map[string]struct{}{}
	permissionNamespaces := map[string]struct{}{}
	for _, contribution := range permissions {
		if contribution.Schema != "codefly/saas/permissions-contribution/v1" || !logicalIDPattern.MatchString(contribution.Namespace) || len(contribution.Permissions) == 0 {
			return fmt.Errorf("permission contribution has an invalid schema, namespace, or empty catalog")
		}
		if _, duplicate := permissionNamespaces[contribution.Namespace]; duplicate {
			return fmt.Errorf("permission namespace %q is duplicated", contribution.Namespace)
		}
		permissionNamespaces[contribution.Namespace] = struct{}{}
		for _, permission := range contribution.Permissions {
			if permission.Name != permission.Resource+":"+permission.Action || !strings.HasPrefix(permission.Resource, contribution.Namespace+".") || isReserved(reserved, "permission", permission.Name) || !logicalIDPattern.MatchString(permission.Resource) || !logicalIDPattern.MatchString(permission.Action) {
				return fmt.Errorf("permission %q is outside namespace %q or invalid", permission.Name, contribution.Namespace)
			}
			if _, duplicate := permissionNames[permission.Name]; duplicate {
				return fmt.Errorf("permission %q is duplicated", permission.Name)
			}
			permissionNames[permission.Name] = struct{}{}
		}
	}

	fixtureNames := map[string]struct{}{}
	fixtureNamespaces := map[string]struct{}{}
	for _, contribution := range fixtures {
		if contribution.Schema != "codefly/saas/fixtures-contribution/v1" || !fixtureNamePattern.MatchString(contribution.Namespace) || len(contribution.Fixtures) == 0 {
			return fmt.Errorf("fixture contribution has an invalid schema, namespace, or empty document list")
		}
		if _, duplicate := fixtureNamespaces[contribution.Namespace]; duplicate {
			return fmt.Errorf("fixture namespace %q is duplicated", contribution.Namespace)
		}
		fixtureNamespaces[contribution.Namespace] = struct{}{}
		for _, fixture := range contribution.Fixtures {
			if !strings.HasPrefix(fixture.Name, contribution.Namespace+"-") || !fixtureNamePattern.MatchString(fixture.Name) || !safeRelativePath(fixture.Path) || (filepath.Ext(fixture.Path) != ".yaml" && filepath.Ext(fixture.Path) != ".yml") {
				return fmt.Errorf("fixture %q is outside namespace %q or has an unsafe path", fixture.Name, contribution.Namespace)
			}
			if _, duplicate := fixtureNames[fixture.Name]; duplicate {
				return fmt.Errorf("fixture %q is duplicated", fixture.Name)
			}
			fixtureNames[fixture.Name] = struct{}{}
		}
	}

	bindingKeys := map[string]struct{}{}
	for _, contribution := range topologies {
		if contribution.Schema != "codefly/saas/topology-contribution/v1" {
			return fmt.Errorf("topology contribution schema must be codefly/saas/topology-contribution/v1")
		}
		for _, binding := range contribution.Bindings {
			if _, found := plugins[binding.Plugin]; !found {
				return fmt.Errorf("topology binding references unknown frontend plugin %q", binding.Plugin)
			}
			if !logicalIDPattern.MatchString(binding.Alias) || !codeflyIDPattern.MatchString(binding.Target.Module) || !codeflyIDPattern.MatchString(binding.Target.Service) {
				return fmt.Errorf("topology binding for plugin %q has an invalid logical target", binding.Plugin)
			}
			key := binding.Plugin + "\x00" + binding.Alias
			if _, duplicate := bindingKeys[key]; duplicate {
				return fmt.Errorf("topology binding for plugin %q alias %q is duplicated", binding.Plugin, binding.Alias)
			}
			bindingKeys[key] = struct{}{}
		}
	}
	return nil
}

func isReserved(namespaces []modulepackage.ReservedNamespace, kind, value string) bool {
	for _, namespace := range namespaces {
		if namespace.Kind == kind && strings.HasPrefix(value, namespace.Value) {
			return true
		}
	}
	return false
}

func validateFixturePaths(documents []string, contributions []FixturesContribution) error {
	for index, contribution := range contributions {
		base := filepath.Dir(documents[index])
		for _, fixture := range contribution.Fixtures {
			if !safeRelativePath(fixture.Path) {
				continue
			}
			info, err := os.Lstat(filepath.Join(base, filepath.FromSlash(fixture.Path)))
			if err != nil {
				return fmt.Errorf("fixture %q path %q: %w", fixture.Name, fixture.Path, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("fixture %q path %q is not a regular file", fixture.Name, fixture.Path)
			}
		}
	}
	return nil
}

func safeRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, `\`) {
		return false
	}
	clean := filepath.Clean(path)
	return clean == path && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func render(frontends []FrontendContribution, settings []SettingsContribution, permissions []PermissionsContribution, fixtures []FixturesContribution, topologies []TopologyContribution) (map[string][]byte, error) {
	sort.Slice(frontends, func(i, j int) bool { return frontends[i].Package < frontends[j].Package })
	sort.Slice(settings, func(i, j int) bool { return settings[i].Field.Number < settings[j].Field.Number })
	allPermissions := make([]Permission, 0)
	for _, contribution := range permissions {
		allPermissions = append(allPermissions, contribution.Permissions...)
	}
	sort.Slice(allPermissions, func(i, j int) bool { return allPermissions[i].Name < allPermissions[j].Name })
	allFixtures := make([]FixtureDocument, 0)
	for _, contribution := range fixtures {
		allFixtures = append(allFixtures, contribution.Fixtures...)
	}
	sort.Slice(allFixtures, func(i, j int) bool { return allFixtures[i].Name < allFixtures[j].Name })
	bindings := make([]FrontendServiceBinding, 0)
	for _, contribution := range topologies {
		bindings = append(bindings, contribution.Bindings...)
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].Plugin == bindings[j].Plugin {
			return bindings[i].Alias < bindings[j].Alias
		}
		return bindings[i].Plugin < bindings[j].Plugin
	})

	frontendSource := renderFrontend(frontends, bindings)
	install := struct {
		Schema       string            `json:"schema"`
		Dependencies map[string]string `json:"dependencies"`
	}{Schema: "codefly/saas/frontend-install/v1", Dependencies: map[string]string{}}
	for _, contribution := range frontends {
		install.Dependencies[contribution.Package] = contribution.Version
	}
	installBody, err := marshalJSON(install)
	if err != nil {
		return nil, err
	}
	permissionBody, err := marshalJSON(permissionCatalog{Schema: "codefly/saas/permissions-catalog/v1", Permissions: allPermissions})
	if err != nil {
		return nil, err
	}
	fixtureBody, err := marshalJSON(fixtureCatalog{Schema: "codefly/saas/fixtures-catalog/v1", Fixtures: allFixtures})
	if err != nil {
		return nil, err
	}
	topologyBody, err := marshalJSON(topologyCatalog{Schema: "codefly/saas/topology-bindings/v1", Bindings: bindings})
	if err != nil {
		return nil, err
	}
	settingsGo, err := format.Source([]byte(renderSettingsGo(settings)))
	if err != nil {
		return nil, fmt.Errorf("format Go settings catalog: %w", err)
	}
	return map[string][]byte{
		FrontendOutput:         []byte(frontendSource),
		FrontendInstallOutput:  installBody,
		SettingsProtoOutput:    []byte(renderSettingsProto(settings)),
		SettingsGoOutput:       settingsGo,
		SettingsTypeScriptOut:  []byte(renderSettingsTypeScript(settings)),
		PermissionsOutput:      permissionBody,
		FixturesOutput:         fixtureBody,
		TopologyBindingsOutput: topologyBody,
	}, nil
}

func renderFrontend(frontends []FrontendContribution, bindings []FrontendServiceBinding) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\n")
	body.WriteString("import type { FrontendReactPlugin } from \"@codefly/saas-plugin-react\";\n")
	body.WriteString("import type { FrontendServiceBinding } from \"@codefly/saas-plugin-contract\";\n")
	for index, contribution := range frontends {
		fmt.Fprintf(&body, "import { %s as contribution%d } from %q;\n", contribution.Export, index, contribution.Package)
	}
	if len(frontends) > 0 {
		body.WriteString("\nexport const contributedPlugins = [\n")
		for index := range frontends {
			fmt.Fprintf(&body, "\tcontribution%d,\n", index)
		}
		body.WriteString("] as const satisfies readonly FrontendReactPlugin[];\n")
	} else {
		body.WriteString("\nexport const contributedPlugins: readonly FrontendReactPlugin[] = [];\n")
	}
	if len(bindings) == 0 {
		body.WriteString("\nexport const contributedServiceBindings = [] as const satisfies readonly FrontendServiceBinding[];\n")
	} else {
		body.WriteString("\nexport const contributedServiceBindings = [\n")
		for _, binding := range bindings {
			fmt.Fprintf(&body, "\t{ plugin: %q, alias: %q, target: { module: %q, service: %q } },\n", binding.Plugin, binding.Alias, binding.Target.Module, binding.Target.Service)
		}
		body.WriteString("] as const satisfies readonly FrontendServiceBinding[];\n")
	}
	return body.String()
}

func renderSettingsProto(settings []SettingsContribution) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\nsyntax = \"proto3\";\n\npackage saas.composed.settings.v1;\n\n")
	body.WriteString("import \"saas/settings/v1/common_settings.proto\";\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "import %q;\n", contribution.ProtoImport)
	}
	body.WriteString("\nmessage Settings {\n  saas.settings.v1.CommonSettings common = 1;\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "  %s %s = %d;\n", contribution.Message, contribution.Field.Name, contribution.Field.Number)
	}
	body.WriteString("}\n")
	return body.String()
}

func renderSettingsGo(settings []SettingsContribution) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\npackage settingscatalog\n\ntype Field struct {\n\tNamespace string\n\tName string\n\tNumber int\n\tMessage string\n}\n\nvar fields = [...]Field{\n\t{Namespace: \"common\", Name: \"common\", Number: 1, Message: \"saas.settings.v1.CommonSettings\"},\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "\t{Namespace: %q, Name: %q, Number: %d, Message: %q},\n", contribution.Namespace, contribution.Field.Name, contribution.Field.Number, contribution.Message)
	}
	body.WriteString("}\n\nfunc Fields() []Field {\n\treturn append([]Field(nil), fields[:]...)\n}\n")
	return body.String()
}

func renderSettingsTypeScript(settings []SettingsContribution) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\nexport const SETTINGS_FIELDS = Object.freeze([\n\t{ namespace: \"common\", name: \"common\", number: 1, message: \"saas.settings.v1.CommonSettings\" },\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "\t{ namespace: %q, name: %q, number: %d, message: %q },\n", contribution.Namespace, contribution.Field.Name, contribution.Field.Number, contribution.Message)
	}
	body.WriteString("] as const);\n")
	return body.String()
}

func marshalJSON(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode composition output: %w", err)
	}
	return append(body, '\n'), nil
}
