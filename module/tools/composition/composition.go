package composition

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
	corecomposition "github.com/codefly-dev/core/composition"
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
	PermissionGoOutput     = "services/accounts/code/pkg/permissioncatalog/catalog_gen.go"
	CompositionCatalogOut  = corecomposition.CompositionCatalogName
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
	InputPath   string
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
	Source  string `yaml:"-"`
	Owner   string `yaml:"-"`
}

type SettingsContribution struct {
	Schema      string        `yaml:"schema"`
	Namespace   string        `yaml:"namespace"`
	ProtoImport string        `yaml:"proto-import"`
	Message     string        `yaml:"message"`
	Field       SettingsField `yaml:"field"`
	FieldRange  NumericRange  `yaml:"field-range"`
	Source      string        `yaml:"-"`
	Owner       string        `yaml:"-"`
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
	Owner       string       `yaml:"-"`
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
	Owner     string            `yaml:"-"`
}

type FixtureDocument struct {
	Name   string `yaml:"name" json:"name"`
	Path   string `yaml:"path" json:"path"`
	Source string `yaml:"-" json:"-"`
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

type frontendInstallCatalog struct {
	Schema       string            `json:"schema"`
	Dependencies map[string]string `json:"dependencies"`
}

type topologyCatalog struct {
	Schema   string                   `json:"schema"`
	Bindings []FrontendServiceBinding `json:"bindings"`
}

func Generate(options Options) error {
	if options.InputPath != "" {
		return generateCoreComposition(options)
	}
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
	if err := validate(frontends, settings, permissions, fixtures, topologies, manifest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(options.OutputRoot, "services/accounts/proto/contributed"), 0o755); err != nil {
		return fmt.Errorf("create contributed settings source directory: %w", err)
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

func generateCoreComposition(options Options) error {
	body, err := os.ReadFile(filepath.Clean(options.InputPath))
	if err != nil {
		return fmt.Errorf("read Core composition input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input corecomposition.CompositionInput
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("decode Core composition input: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode Core composition input: %w", err)
	}
	if input.Schema != "codefly/composition-input/v2" || input.Descriptor == nil || input.ConsumerRoot == "" || input.Projection == "" {
		return fmt.Errorf("core composition input is incomplete")
	}
	moduleRoot := options.ModuleRoot
	if moduleRoot == "" {
		moduleRoot = filepath.Clean(filepath.Join(filepath.Dir(options.InputPath), ".."))
	}
	manifest, err := modulepackage.ReadManifest(moduleRoot)
	if err != nil {
		return err
	}
	if input.Package != manifest.ID || input.Version != manifest.Version {
		return fmt.Errorf("core composition input identifies %s@%s, package is %s@%s", input.Package, input.Version, manifest.ID, manifest.Version)
	}
	catalogInputs, err := corecomposition.LoadContributionInputs(input.ConsumerRoot, input.Descriptor)
	if err != nil {
		return err
	}

	frontends, err := coreFrontendContributions(input.ConsumerRoot, input.Descriptor.Contributions.Frontend)
	if err != nil {
		return err
	}
	settings := coreSettingsContributions(input.ConsumerRoot, input.Descriptor.Contributions.Settings)
	permissions, err := corePermissionContributions(input.ConsumerRoot, input.Descriptor.Contributions.Permissions)
	if err != nil {
		return err
	}
	fixtures := coreFixtureContributions(input.ConsumerRoot, input.Descriptor.Contributions.Fixtures)
	topologies := []TopologyContribution{{Schema: "codefly/saas/topology-contribution/v1"}}
	for _, binding := range input.Descriptor.Bindings {
		topologies[0].Bindings = append(topologies[0].Bindings, FrontendServiceBinding{
			Plugin: binding.Plugin,
			Alias:  binding.Alias,
			Target: FrontendServiceTarget{Module: binding.Target.Module, Service: binding.Target.Service},
		})
	}
	if err := validate(frontends, settings, permissions, fixtures, topologies, manifest); err != nil {
		return err
	}
	if err := stageContributionSources(input.Projection, frontends, settings, fixtures); err != nil {
		return err
	}
	if err := updateFrontendInstallGraph(input.Projection, frontends); err != nil {
		return err
	}
	files, err := render(frontends, settings, permissions, fixtures, topologies)
	if err != nil {
		return err
	}
	catalog := corecomposition.Catalog{
		Schema:       "codefly/composition-catalog/v2",
		Inputs:       catalogInputs,
		Claims:       contributionClaims(frontends, settings, permissions, fixtures),
		Dependencies: frontendDependencies(frontends),
	}
	catalogBody, err := marshalJSON(catalog)
	if err != nil {
		return err
	}
	files[CompositionCatalogOut] = catalogBody
	return writeOutputs(input.Projection, files)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

type frontendPackageDocument struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Codefly struct {
		FrontendPlugin string `json:"frontendPlugin"`
	} `json:"codefly"`
}

func coreFrontendContributions(root string, descriptors []corecomposition.FrontendContribution) ([]FrontendContribution, error) {
	contributions := make([]FrontendContribution, 0, len(descriptors))
	for _, descriptor := range descriptors {
		source := filepath.Join(root, filepath.FromSlash(descriptor.Path))
		body, err := os.ReadFile(filepath.Join(source, "package.json"))
		if err != nil {
			return nil, fmt.Errorf("read frontend contribution %q package.json: %w", descriptor.Path, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		var document frontendPackageDocument
		if err := decoder.Decode(&document); err != nil {
			return nil, fmt.Errorf("decode frontend contribution %q package.json: %w", descriptor.Path, err)
		}
		if err := requireJSONEOF(decoder); err != nil {
			return nil, fmt.Errorf("decode frontend contribution %q package.json: %w", descriptor.Path, err)
		}
		contributions = append(contributions, FrontendContribution{
			Schema:  "codefly/saas/frontend-contribution/v2",
			Package: document.Name,
			Version: document.Version,
			Export:  descriptor.Export,
			Plugin:  document.Codefly.FrontendPlugin,
			Source:  source,
			Owner:   descriptor.Path,
		})
	}
	return contributions, nil
}

func coreSettingsContributions(root string, descriptors []corecomposition.SettingsContribution) []SettingsContribution {
	ordered := append([]corecomposition.SettingsContribution(nil), descriptors...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		return ordered[i].Message < ordered[j].Message
	})
	contributions := make([]SettingsContribution, 0, len(ordered))
	for _, descriptor := range ordered {
		fieldName := protoFieldName(descriptor.Message)
		namespace, _, _ := strings.Cut(descriptor.Message, ".")
		number := stableSettingsFieldNumber(descriptor.Path, descriptor.Message)
		contributions = append(contributions, SettingsContribution{
			Schema:      "codefly/saas/settings-contribution/v1",
			Namespace:   namespace,
			ProtoImport: filepath.ToSlash(filepath.Join("contributed", descriptor.Path)),
			Message:     descriptor.Message,
			Field:       SettingsField{Name: fieldName, Number: number},
			FieldRange:  NumericRange{Start: number, End: number},
			Source:      filepath.Join(root, filepath.FromSlash(descriptor.Path)),
			Owner:       descriptor.Path,
		})
	}
	return contributions
}

func stableSettingsFieldNumber(path, message string) int {
	digest := sha256.Sum256([]byte(path + "\x00" + message))
	const first = uint32(20000)
	const last = uint32(536870911)
	return int(first + binary.BigEndian.Uint32(digest[:4])%(last-first+1))
}

func protoFieldName(message string) string {
	name := message[strings.LastIndex(message, ".")+1:]
	var out strings.Builder
	for index, character := range name {
		if character >= 'A' && character <= 'Z' {
			if index > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(character + ('a' - 'A'))
			continue
		}
		out.WriteRune(character)
	}
	return out.String()
}

func corePermissionContributions(root string, descriptors []corecomposition.PathContribution) ([]PermissionsContribution, error) {
	paths := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		paths = append(paths, filepath.Join(root, filepath.FromSlash(descriptor.Path)))
	}
	contributions, err := readDocuments[PermissionsContribution](paths)
	if err != nil {
		return nil, err
	}
	for index := range contributions {
		contributions[index].Owner = descriptors[index].Path
	}
	return contributions, nil
}

func coreFixtureContributions(root string, descriptors []corecomposition.PathContribution) []FixturesContribution {
	contributions := make([]FixturesContribution, 0, len(descriptors))
	for _, descriptor := range descriptors {
		name := strings.TrimSuffix(filepath.Base(descriptor.Path), filepath.Ext(descriptor.Path))
		namespace := name
		if prefix, _, found := strings.Cut(name, "-"); found {
			namespace = prefix
		}
		contributions = append(contributions, FixturesContribution{
			Schema:    "codefly/saas/fixtures-contribution/v1",
			Namespace: namespace,
			Owner:     descriptor.Path,
			Fixtures: []FixtureDocument{{
				Name:   name,
				Path:   filepath.ToSlash(filepath.Join("fixtures", name+".yaml")),
				Source: filepath.Join(root, filepath.FromSlash(descriptor.Path)),
			}},
		})
	}
	return contributions
}

func stageContributionSources(projection string, frontends []FrontendContribution, settings []SettingsContribution, fixtures []FixturesContribution) error {
	if err := removeManagedFixtureFiles(projection); err != nil {
		return err
	}
	previousFrontend, err := readFrontendInstallCatalog(projection)
	if err != nil {
		return err
	}
	protoRoot := filepath.Join(projection, "services", "accounts", "proto", "contributed")
	if err := os.RemoveAll(protoRoot); err != nil {
		return fmt.Errorf("reset contributed settings sources: %w", err)
	}
	if err := os.MkdirAll(protoRoot, 0o755); err != nil {
		return fmt.Errorf("create contributed settings source directory: %w", err)
	}
	packagesRoot := filepath.Join(projection, "services", "frontend", "code", "packages")
	if err := os.MkdirAll(packagesRoot, 0o755); err != nil {
		return fmt.Errorf("create frontend package directory: %w", err)
	}
	for packageName := range previousFrontend.Dependencies {
		workspace := frontendWorkspaceName(packageName)
		if err := os.RemoveAll(filepath.Join(packagesRoot, workspace)); err != nil {
			return fmt.Errorf("reset contributed frontend package %q: %w", workspace, err)
		}
	}
	for _, contribution := range settings {
		destination := filepath.Join(projection, "services", "accounts", "proto", filepath.FromSlash(contribution.ProtoImport))
		if err := copyRegularFile(contribution.Source, destination); err != nil {
			return fmt.Errorf("stage settings contribution %q: %w", contribution.Owner, err)
		}
	}
	for _, contribution := range frontends {
		destination := filepath.Join(packagesRoot, frontendWorkspaceName(contribution.Package))
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("reset frontend contribution %q: %w", contribution.Owner, err)
		}
		if err := copyRegularTree(contribution.Source, destination); err != nil {
			return fmt.Errorf("stage frontend contribution %q: %w", contribution.Owner, err)
		}
	}
	for _, contribution := range fixtures {
		for _, fixture := range contribution.Fixtures {
			destination := filepath.Join(projection, filepath.FromSlash(fixture.Path))
			if _, err := os.Lstat(destination); err == nil {
				return fmt.Errorf("fixture contribution %q collides with projection path %q", contribution.Owner, fixture.Path)
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := copyRegularFile(fixture.Source, destination); err != nil {
				return fmt.Errorf("stage fixture contribution %q: %w", contribution.Owner, err)
			}
		}
	}
	return nil
}

func removeManagedFixtureFiles(projection string) error {
	catalogPath := filepath.Join(projection, filepath.FromSlash(FixturesOutput))
	body, err := os.ReadFile(catalogPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read previous fixture catalog: %w", err)
	}
	var catalog fixtureCatalog
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return fmt.Errorf("decode previous fixture catalog: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode previous fixture catalog: %w", err)
	}
	if catalog.Schema != "codefly/saas/fixtures-catalog/v1" {
		return fmt.Errorf("previous fixture catalog has unsupported schema %q", catalog.Schema)
	}
	fixturesRoot := filepath.Join(projection, "fixtures")
	if info, err := os.Lstat(fixturesRoot); err == nil && !info.IsDir() {
		return fmt.Errorf("projection fixtures path is not a directory")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect projection fixtures directory: %w", err)
	}
	for _, fixture := range catalog.Fixtures {
		if !safeRelativePath(fixture.Path) || filepath.ToSlash(filepath.Dir(fixture.Path)) != "fixtures" {
			return fmt.Errorf("previous fixture catalog contains unsafe managed path %q", fixture.Path)
		}
		destination := filepath.Join(projection, filepath.FromSlash(fixture.Path))
		info, err := os.Lstat(destination)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect previous managed fixture %q: %w", fixture.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("previous managed fixture %q is not a regular file", fixture.Path)
		}
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("remove previous managed fixture %q: %w", fixture.Path, err)
		}
	}
	return nil
}

func copyRegularTree(source, destination string) error {
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("contribution contains unsafe path %q", relative)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyRegularFile(current, target)
	})
}

func copyRegularFile(source, destination string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, body, 0o644)
}

func frontendWorkspaceName(packageName string) string {
	digest := sha256.Sum256([]byte(packageName))
	return fmt.Sprintf("codefly-contributed-%x", digest[:8])
}

func updateFrontendInstallGraph(projection string, contributions []FrontendContribution) error {
	previous, err := readFrontendInstallCatalog(projection)
	if err != nil {
		return err
	}
	packagePath := filepath.Join(projection, "services", "frontend", "code", "package.json")
	body, err := os.ReadFile(packagePath)
	if err != nil {
		return fmt.Errorf("read frontend package.json: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode frontend package.json: %w", err)
	}
	dependencies := map[string]string{}
	if raw := document["dependencies"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &dependencies); err != nil {
			return fmt.Errorf("decode frontend package.json dependencies: %w", err)
		}
	}
	for packageName := range previous.Dependencies {
		delete(dependencies, packageName)
	}
	for _, contribution := range contributions {
		if _, exists := dependencies[contribution.Package]; exists {
			return fmt.Errorf("frontend package %q collides with a base dependency", contribution.Package)
		}
		dependencies[contribution.Package] = contribution.Version
	}
	dependencyBody, err := json.Marshal(dependencies)
	if err != nil {
		return fmt.Errorf("encode frontend package.json dependencies: %w", err)
	}
	document["dependencies"] = dependencyBody
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode frontend package.json: %w", err)
	}
	if err := os.WriteFile(packagePath, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write frontend package.json: %w", err)
	}
	if len(contributions) == 0 && len(previous.Dependencies) == 0 {
		return nil
	}
	command := exec.Command("npm", "install", "--ignore-scripts", "--no-audit", "--no-fund")
	command.Dir = filepath.Dir(packagePath)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("generate frontend install graph: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for _, contribution := range contributions {
		build := exec.Command("npm", "run", "build", "--if-present", "--workspace", contribution.Package)
		build.Dir = filepath.Dir(packagePath)
		if output, err := build.CombinedOutput(); err != nil {
			return fmt.Errorf("build frontend contribution %q: %w: %s", contribution.Package, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func readFrontendInstallCatalog(projection string) (frontendInstallCatalog, error) {
	catalog := frontendInstallCatalog{
		Schema:       "codefly/saas/frontend-install/v1",
		Dependencies: map[string]string{},
	}
	path := filepath.Join(projection, filepath.FromSlash(FrontendInstallOutput))
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return catalog, nil
	}
	if err != nil {
		return frontendInstallCatalog{}, fmt.Errorf("read previous frontend install catalog: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return frontendInstallCatalog{}, fmt.Errorf("decode previous frontend install catalog: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return frontendInstallCatalog{}, fmt.Errorf("decode previous frontend install catalog: %w", err)
	}
	if catalog.Schema != "codefly/saas/frontend-install/v1" || catalog.Dependencies == nil {
		return frontendInstallCatalog{}, fmt.Errorf("previous frontend install catalog is invalid")
	}
	for packageName := range catalog.Dependencies {
		if !packagePattern.MatchString(packageName) {
			return frontendInstallCatalog{}, fmt.Errorf("previous frontend install catalog contains invalid package %q", packageName)
		}
	}
	return catalog, nil
}

func contributionClaims(frontends []FrontendContribution, settings []SettingsContribution, permissions []PermissionsContribution, fixtures []FixturesContribution) []corecomposition.Claim {
	var claims []corecomposition.Claim
	for _, contribution := range frontends {
		claims = append(claims,
			corecomposition.Claim{Kind: corecomposition.CollisionPackage, Key: contribution.Package, Owner: contribution.Owner},
			corecomposition.Claim{Kind: corecomposition.CollisionRoute, Key: "plugin/" + contribution.Plugin, Owner: contribution.Owner},
		)
	}
	for _, contribution := range settings {
		claims = append(claims, corecomposition.Claim{Kind: corecomposition.CollisionSettingsField, Key: contribution.Field.Name, Owner: contribution.Owner})
	}
	for _, contribution := range permissions {
		for _, permission := range contribution.Permissions {
			claims = append(claims, corecomposition.Claim{Kind: corecomposition.CollisionPermission, Key: permission.Name, Owner: contribution.Owner})
		}
	}
	for _, contribution := range fixtures {
		for _, fixture := range contribution.Fixtures {
			claims = append(claims, corecomposition.Claim{Kind: corecomposition.CollisionTopology, Key: "fixture/" + fixture.Name, Owner: contribution.Owner})
		}
	}
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Kind != claims[j].Kind {
			return claims[i].Kind < claims[j].Kind
		}
		return claims[i].Key < claims[j].Key
	})
	return claims
}

func frontendDependencies(frontends []FrontendContribution) []string {
	dependencies := make([]string, 0, len(frontends))
	for _, contribution := range frontends {
		dependencies = append(dependencies, contribution.Package+"@"+contribution.Version)
	}
	sort.Strings(dependencies)
	return dependencies
}

func writeOutputs(root string, files map[string][]byte) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return fmt.Errorf("create composition output directory: %w", err)
		}
		temporary, err := os.CreateTemp(filepath.Dir(absolute), ".composition-*")
		if err != nil {
			return fmt.Errorf("stage composition output %q: %w", path, err)
		}
		temporaryPath := temporary.Name()
		if _, err := temporary.Write(files[path]); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("write composition output %q: %w", path, err)
		}
		if err := temporary.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("close composition output %q: %w", path, err)
		}
		if err := os.Rename(temporaryPath, absolute); err != nil {
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("publish composition output %q: %w", path, err)
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

func validate(frontends []FrontendContribution, settings []SettingsContribution, permissions []PermissionsContribution, fixtures []FixturesContribution, topologies []TopologyContribution, manifest modulepackage.Manifest) error {
	plugins := map[string]struct{}{}
	packages := map[string]struct{}{}
	fixtureNames := map[string]struct{}{}
	for _, claim := range manifest.Claims {
		switch claim.Kind {
		case corecomposition.CollisionPackage:
			packages[claim.Key] = struct{}{}
		case corecomposition.CollisionRoute:
			if plugin, found := strings.CutPrefix(claim.Key, "plugin/"); found {
				plugins[plugin] = struct{}{}
			}
		case corecomposition.CollisionTopology:
			if fixture, found := strings.CutPrefix(claim.Key, "fixture/"); found {
				fixtureNames[fixture] = struct{}{}
			}
		}
	}
	for _, contribution := range frontends {
		if contribution.Schema != "codefly/saas/frontend-contribution/v2" {
			return fmt.Errorf("frontend contribution schema must be codefly/saas/frontend-contribution/v2")
		}
		if !packagePattern.MatchString(contribution.Package) || isReserved(manifest.ReservedNamespaces, contribution.Package) {
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
	fieldNames := map[string]struct{}{}
	for _, claim := range manifest.Claims {
		if claim.Kind == corecomposition.CollisionSettingsField {
			fieldNames[claim.Key] = struct{}{}
		}
	}
	for _, contribution := range settings {
		if contribution.Schema != "codefly/saas/settings-contribution/v1" {
			return fmt.Errorf("settings contribution schema must be codefly/saas/settings-contribution/v1")
		}
		if !logicalIDPattern.MatchString(contribution.Namespace) || isReserved(manifest.ReservedNamespaces, contribution.Namespace) || !protoNamePattern.MatchString(contribution.Message) || isReserved(manifest.ReservedNamespaces, contribution.Message) || !safeRelativePath(contribution.ProtoImport) || filepath.Ext(contribution.ProtoImport) != ".proto" {
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
	for _, claim := range manifest.Claims {
		if claim.Kind == corecomposition.CollisionPermission {
			permissionNames[claim.Key] = struct{}{}
		}
	}
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
			if permission.Name != permission.Resource+":"+permission.Action || !strings.HasPrefix(permission.Resource, contribution.Namespace+".") || isReserved(manifest.ReservedNamespaces, permission.Name) || !logicalIDPattern.MatchString(permission.Resource) || !logicalIDPattern.MatchString(permission.Action) {
				return fmt.Errorf("permission %q is outside namespace %q or invalid", permission.Name, contribution.Namespace)
			}
			if _, duplicate := permissionNames[permission.Name]; duplicate {
				return fmt.Errorf("permission %q is duplicated", permission.Name)
			}
			permissionNames[permission.Name] = struct{}{}
		}
	}

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
			if (fixture.Name != contribution.Namespace && !strings.HasPrefix(fixture.Name, contribution.Namespace+"-")) || !fixtureNamePattern.MatchString(fixture.Name) || !safeRelativePath(fixture.Path) || (filepath.Ext(fixture.Path) != ".yaml" && filepath.Ext(fixture.Path) != ".yml") {
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

func isReserved(namespaces []string, value string) bool {
	for _, namespace := range namespaces {
		if value == namespace || strings.HasPrefix(value, namespace+"/") || strings.HasPrefix(value, namespace+".") || strings.HasPrefix(value, namespace+":") || strings.HasPrefix(value, namespace) && strings.HasSuffix(namespace, "-") {
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
	install := frontendInstallCatalog{Schema: "codefly/saas/frontend-install/v1", Dependencies: map[string]string{}}
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
	permissionGo, err := format.Source([]byte(renderPermissionsGo(permissions)))
	if err != nil {
		return nil, fmt.Errorf("format Go permission catalog: %w", err)
	}
	return map[string][]byte{
		FrontendOutput:         []byte(frontendSource),
		FrontendInstallOutput:  installBody,
		SettingsProtoOutput:    []byte(renderSettingsProto(settings)),
		SettingsGoOutput:       settingsGo,
		SettingsTypeScriptOut:  []byte(renderSettingsTypeScript(settings)),
		PermissionsOutput:      permissionBody,
		PermissionGoOutput:     permissionGo,
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
	for _, contribution := range settings {
		fmt.Fprintf(&body, "import %q;\n", contribution.ProtoImport)
	}
	body.WriteString("\nmessage Settings {\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "  %s %s = %d;\n", contribution.Message, contribution.Field.Name, contribution.Field.Number)
	}
	body.WriteString("}\n")
	return body.String()
}

func renderSettingsGo(settings []SettingsContribution) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\npackage settingscatalog\n\ntype Field struct {\n\tNamespace string\n\tName string\n\tNumber int\n\tMessage string\n}\n\nvar fields = [...]Field{\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "\t{Namespace: %q, Name: %q, Number: %d, Message: %q},\n", contribution.Namespace, contribution.Field.Name, contribution.Field.Number, contribution.Message)
	}
	body.WriteString("}\n\nfunc Fields() []Field {\n\treturn append([]Field(nil), fields[:]...)\n}\n")
	return body.String()
}

func renderSettingsTypeScript(settings []SettingsContribution) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\nexport const SETTINGS_FIELDS = Object.freeze([\n")
	for _, contribution := range settings {
		fmt.Fprintf(&body, "\t{ namespace: %q, name: %q, number: %d, message: %q },\n", contribution.Namespace, contribution.Field.Name, contribution.Field.Number, contribution.Message)
	}
	body.WriteString("] as const);\n")
	return body.String()
}

func renderPermissionsGo(contributions []PermissionsContribution) string {
	permissions := make([]Permission, 0)
	for _, contribution := range contributions {
		permissions = append(permissions, contribution.Permissions...)
	}
	sort.Slice(permissions, func(i, j int) bool { return permissions[i].Name < permissions[j].Name })
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\npackage permissioncatalog\n\ntype Permission struct {\n\tName string\n\tResource string\n\tAction string\n}\n\nvar permissions = [...]Permission{\n")
	for _, permission := range permissions {
		fmt.Fprintf(&body, "\t{Name: %q, Resource: %q, Action: %q},\n", permission.Name, permission.Resource, permission.Action)
	}
	body.WriteString("}\n\nfunc Permissions() []Permission {\n\treturn append([]Permission(nil), permissions[:]...)\n}\n")
	return body.String()
}

func marshalJSON(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode composition output: %w", err)
	}
	return append(body, '\n'), nil
}
