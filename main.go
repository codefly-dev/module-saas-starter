package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/wool"
	"gopkg.in/yaml.v3"
)

//go:embed all:module
var embeddedModule embed.FS

const (
	moduleYamlPath    = "module.codefly.yaml"
	moduleDirName     = "module"
	sourceEnvVar      = "SAAS_STARTER_MODULE_SRC"
	bundleRelativeDir = "deployment/kustomize"
	workspaceYamlPath = "workspace.codefly.yaml"
)

// resolveSource finds the module/ directory to copy from.
// Priority: SAAS_STARTER_MODULE_SRC env var → <executable-dir>/module → embedded package.
func resolveSource() (string, func(), error) {
	if src := os.Getenv(sourceEnvVar); src != "" {
		return src, func() {}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("cannot resolve executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", nil, fmt.Errorf("cannot eval executable symlinks: %w", err)
	}
	sibling := filepath.Join(filepath.Dir(exe), moduleDirName)
	if _, err := os.Stat(filepath.Join(sibling, moduleYamlPath)); err == nil {
		return sibling, func() {}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("inspect packaged module source: %w", err)
	}
	root, err := os.MkdirTemp("", "saas-starter-module-")
	if err != nil {
		return "", nil, fmt.Errorf("create embedded module source: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := fs.WalkDir(embeddedModule, moduleDirName, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(moduleDirName, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, moduleDirName, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(embeddedModule, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("materialize embedded module source: %w", err)
	}
	return filepath.Join(root, moduleDirName), cleanup, nil
}

func Create(ctx context.Context, dir, name string) error {
	w := wool.Get(ctx).In("saas-starter::Create")

	src, cleanupSource, err := resolveSource()
	if err != nil {
		return w.Wrapf(err, "cannot resolve source")
	}
	defer cleanupSource()
	if _, err := os.Stat(filepath.Join(src, moduleYamlPath)); err != nil {
		return w.Wrapf(err, "source %q is not a module directory (missing %s)", src, moduleYamlPath)
	}

	if _, err := shared.CheckDirectoryOrCreate(ctx, dir); err != nil {
		return w.Wrapf(err, "cannot create target directory")
	}

	workspaceRoot, err := findWorkspaceRoot(dir)
	if err != nil {
		return w.Wrapf(err, "cannot find workspace")
	}
	workspace, err := loadWorkspaceManifest(workspaceRoot)
	if err != nil {
		return w.Wrapf(err, "cannot load workspace")
	}

	parent := filepath.Dir(dir)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+"-stage-*")
	if err != nil {
		return w.Wrapf(err, "cannot create module staging directory")
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if err := copyTree(dir, stage, "", false); err != nil {
		return w.Wrapf(err, "cannot stage existing module scaffold")
	}
	existing, hasConsumerInventory, err := existingModuleInventory(dir)
	if err != nil {
		return w.Wrapf(err, "cannot read existing module inventory")
	}
	if hasConsumerInventory && existing.Name != name {
		return fmt.Errorf("existing module name %q does not match requested name %q", existing.Name, name)
	}
	if err := copyModuleSource(src, stage, name, existing, hasConsumerInventory); err != nil {
		return w.Wrapf(err, "cannot stage module source")
	}
	if hasConsumerInventory {
		if err := refreshModuleInterface(src, stage); err != nil {
			return w.Wrapf(err, "cannot refresh module interface")
		}
	}
	if err := normalizeDeploymentMetadata(stage); err != nil {
		return w.Wrapf(err, "cannot normalize module deployment metadata")
	}
	if err := generateDeploymentBundle(stage, workspace); err != nil {
		return w.Wrapf(err, "cannot generate deployment bundle")
	}

	backup := stage + "-previous"
	if err := os.Rename(dir, backup); err != nil {
		return w.Wrapf(err, "cannot preserve existing module scaffold")
	}
	if err := os.Rename(stage, dir); err != nil {
		if restoreErr := os.Rename(backup, dir); restoreErr != nil {
			return w.Wrapf(err, "cannot install generated module; cannot restore scaffold: %v", restoreErr)
		}
		return w.Wrapf(err, "cannot install generated module")
	}
	if err := os.RemoveAll(backup); err != nil {
		return w.Wrapf(err, "cannot remove replaced module scaffold")
	}

	w.Info("module created", wool.Field("name", name), wool.Field("source", src))
	return nil
}

func existingModuleInventory(dir string) (moduleManifest, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, moduleYamlPath))
	if errors.Is(err, os.ErrNotExist) {
		return moduleManifest{}, false, nil
	}
	if err != nil {
		return moduleManifest{}, false, err
	}
	var manifest moduleManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return moduleManifest{}, false, err
	}
	return manifest, len(manifest.Services) > 0, nil
}

func servicePathOverrides(manifest moduleManifest) map[string]*string {
	overrides := make(map[string]*string, len(manifest.Services))
	for _, service := range manifest.Services {
		overrides[service.Name] = service.Path
	}
	return overrides
}

func remapServicePath(relative string, override *string) (string, bool, error) {
	if override == nil {
		return relative, true, nil
	}
	if filepath.IsAbs(*override) {
		return "", false, nil
	}
	serviceDir, err := cleanRelativeFilesystemPath(*override)
	if err != nil {
		return "", false, err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	target := filepath.Join("services", serviceDir)
	if len(parts) > 2 {
		target = filepath.Join(target, filepath.FromSlash(strings.Join(parts[2:], "/")))
	}
	return target, true, nil
}

func copyModuleSource(
	src,
	dst,
	name string,
	consumer moduleManifest,
	preserveInventory bool,
) error {
	declared := servicePathOverrides(consumer)
	return filepath.WalkDir(src, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(src, file)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		slashRelative := filepath.ToSlash(relative)
		targetRelative := relative
		skip := slashRelative == bundleRelativeDir ||
			strings.HasPrefix(slashRelative, bundleRelativeDir+"/")
		if preserveInventory {
			skip = skip ||
				slashRelative == moduleYamlPath ||
				slashRelative == "deployment/generated" ||
				strings.HasPrefix(slashRelative, "deployment/generated/")
			parts := strings.Split(slashRelative, "/")
			if len(parts) >= 2 && parts[0] == "services" && (len(parts) >= 3 || entry.IsDir()) {
				override, exists := declared[parts[1]]
				if !exists {
					skip = true
				} else {
					mapped, local, mapErr := remapServicePath(relative, override)
					if mapErr != nil {
						return fmt.Errorf("service %q path: %w", parts[1], mapErr)
					}
					if !local {
						skip = true
					} else {
						targetRelative = mapped
					}
				}
			}
		}
		if skip {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, targetRelative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(file, target, info.Mode().Perm(), relative == moduleYamlPath, name)
	})
}

func normalizeDeploymentMetadata(moduleDir string) error {
	manifest, err := loadModuleManifest(moduleDir)
	if err != nil {
		return err
	}
	declared := make(map[string]struct{}, len(manifest.Services))
	for _, service := range manifest.Services {
		declared[service.Name] = struct{}{}
	}
	if err := pruneModuleInterface(moduleDir, declared); err != nil {
		return err
	}
	if manifest.ServiceEntry != "" && !containsService(declared, manifest.ServiceEntry) {
		return fmt.Errorf("service entry %q is not declared by module %q", manifest.ServiceEntry, manifest.Name)
	}
	if err := regenerateServiceManifests(moduleDir, manifest); err != nil {
		return err
	}
	services, err := validateServiceInventory(moduleDir, manifest.Services)
	if err != nil {
		return err
	}
	topology, err := loadDeploymentTopology(moduleDir, manifest.Name, services)
	if err != nil {
		return err
	}
	return normalizeGeneratedDeploymentMetadata(moduleDir, topology)
}

func containsService(services map[string]struct{}, name string) bool {
	_, exists := services[name]
	return exists
}

// refreshModuleInterface carries the base's contract into a consumer that keeps
// its own module.codefly.yaml: the `interface` block is the base's to declare,
// so the consumer's copy takes the source's, and pruneModuleInterface then
// drops the endpoints of services the consumer did not compose. A source that
// declares no interface leaves the consumer's untouched.
func refreshModuleInterface(src, moduleDir string) error {
	sourceData, err := os.ReadFile(filepath.Join(src, moduleYamlPath))
	if err != nil {
		return err
	}
	var source yaml.Node
	if err := yaml.Unmarshal(sourceData, &source); err != nil {
		return fmt.Errorf("parse source %s: %w", moduleYamlPath, err)
	}
	if len(source.Content) != 1 || source.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("source %s must contain one mapping", moduleYamlPath)
	}
	contract := mappingValue(source.Content[0], "interface")
	if contract == nil {
		return nil
	}
	path := filepath.Join(moduleDir, moduleYamlPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse %s: %w", moduleYamlPath, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s must contain one mapping", moduleYamlPath)
	}
	root := document.Content[0]
	replaced := false
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == "interface" {
			root.Content[index+1] = contract
			replaced = true
			break
		}
	}
	if !replaced {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "interface"}, contract)
	}
	rendered, err := yaml.Marshal(&document)
	if err != nil {
		return fmt.Errorf("render %s: %w", moduleYamlPath, err)
	}
	return os.WriteFile(path, rendered, 0o644)
}

// pruneModuleInterface keeps the composed module's contract consistent with
// the services the consumer declares: an interface endpoint of a service the
// consumer did not compose is dropped from its module.codefly.yaml. Nothing
// else in that file is touched.
func pruneModuleInterface(moduleDir string, declared map[string]struct{}) error {
	path := filepath.Join(moduleDir, moduleYamlPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse %s: %w", moduleYamlPath, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s must contain one mapping", moduleYamlPath)
	}
	endpoints := mappingValue(mappingValue(document.Content[0], "interface"), "endpoints")
	if endpoints == nil || endpoints.Kind != yaml.SequenceNode {
		return nil
	}
	kept := endpoints.Content[:0]
	for _, endpoint := range endpoints.Content {
		if service := mappingValue(endpoint, "service"); service == nil || containsService(declared, service.Value) {
			kept = append(kept, endpoint)
		}
	}
	if len(kept) == len(endpoints.Content) {
		return nil
	}
	endpoints.Content = kept
	rendered, err := yaml.Marshal(&document)
	if err != nil {
		return fmt.Errorf("render %s: %w", moduleYamlPath, err)
	}
	return os.WriteFile(path, rendered, 0o644)
}

// mappingValue returns the value node of key in a mapping node, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

type generatedServiceDependency struct {
	Name      string                              `yaml:"name"`
	Module    string                              `yaml:"module,omitempty"`
	Endpoints []generatedServiceEndpointReference `yaml:"endpoints"`
}

type generatedServiceEndpointReference struct {
	Name string `yaml:"name"`
}

// regenerateServiceManifests derives what a consumer composition changes about
// a copied service manifest and nothing more: a dependency on a service the
// consumer did not compose is dropped, and the frontend gains the cross-module
// services its installed plugins reach. A manifest that needs neither stays
// the authored bytes the base ships.
func regenerateServiceManifests(moduleDir string, manifest moduleManifest) error {
	declared := make(map[string]struct{}, len(manifest.Services))
	for _, reference := range manifest.Services {
		declared[reference.Name] = struct{}{}
	}
	for _, reference := range manifest.Services {
		if reference.Path != nil && filepath.IsAbs(*reference.Path) {
			continue
		}
		serviceDir := reference.Name
		if reference.Path != nil {
			serviceDir = *reference.Path
		}
		path := filepath.Join(moduleDir, "services", serviceDir, "service.codefly.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read service %q manifest: %w", reference.Name, err)
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("parse service %q manifest: %w", reference.Name, err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("service %q manifest must contain one mapping", reference.Name)
		}
		root := document.Content[0]
		changed := false
		sources := "services/" + serviceDir + "/service.codefly.yaml"
		if dependencies := mappingValue(root, "service-dependencies"); dependencies != nil && dependencies.Kind == yaml.SequenceNode {
			kept := dependencies.Content[:0]
			for _, dependency := range dependencies.Content {
				name := mappingValue(dependency, "name")
				module := mappingValue(dependency, "module")
				external := module != nil && module.Value != "" && module.Value != manifest.Name
				if external || name == nil || containsService(declared, name.Value) {
					kept = append(kept, dependency)
				}
			}
			if len(kept) != len(dependencies.Content) {
				dependencies.Content = kept
				changed = true
			}
		}
		if reference.Name == "frontend" {
			external, err := frontendPluginServiceDependencies(moduleDir, serviceDir, manifest.Name)
			if err != nil {
				return err
			}
			if len(external) > 0 {
				dependencies := mappingValue(root, "service-dependencies")
				if dependencies == nil {
					root.Content = append(root.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Value: "service-dependencies"},
						&yaml.Node{Kind: yaml.SequenceNode})
					dependencies = root.Content[len(root.Content)-1]
				}
				for _, dependency := range external {
					var node yaml.Node
					if err := node.Encode(dependency); err != nil {
						return fmt.Errorf("render frontend plugin dependency %q: %w", dependency.Name, err)
					}
					dependencies.Content = append(dependencies.Content, &node)
				}
				changed = true
				sources += " and services/frontend/code/server/plugin-service-allowlist.generated.json"
			}
		}
		if !changed {
			continue
		}
		rendered, err := yaml.Marshal(&document)
		if err != nil {
			return fmt.Errorf("render service manifest %q: %w", reference.Name, err)
		}
		header := []byte("# Composed by the module agent from " + sources + ".\n")
		if err := os.WriteFile(path, append(header, rendered...), 0o644); err != nil {
			return fmt.Errorf("write service manifest %q: %w", reference.Name, err)
		}
	}
	return nil
}

type frontendPluginAllowlist struct {
	SchemaVersion   int `json:"schemaVersion"`
	ContractVersion int `json:"contractVersion"`
	Entries         []struct {
		Target struct {
			Module   string `json:"module"`
			Service  string `json:"service"`
			Endpoint string `json:"endpoint"`
		} `json:"target"`
	} `json:"entries"`
}

func frontendPluginServiceDependencies(moduleDir, serviceDir, moduleName string) ([]generatedServiceDependency, error) {
	path := filepath.Join(
		moduleDir,
		"services",
		serviceDir,
		"code",
		"server",
		"plugin-service-allowlist.generated.json",
	)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read frontend plugin service allowlist: %w", err)
	}
	var allowlist frontendPluginAllowlist
	if err := json.Unmarshal(data, &allowlist); err != nil {
		return nil, fmt.Errorf("parse frontend plugin service allowlist: %w", err)
	}
	if allowlist.SchemaVersion != 1 || allowlist.ContractVersion != 2 {
		return nil, fmt.Errorf(
			"unsupported frontend plugin service allowlist schema=%d contract=%d",
			allowlist.SchemaVersion,
			allowlist.ContractVersion,
		)
	}
	targets := make(map[string]map[string]struct{})
	for _, entry := range allowlist.Entries {
		target := entry.Target
		if !dnsLabelPattern.MatchString(target.Module) || !dnsLabelPattern.MatchString(target.Service) {
			return nil, fmt.Errorf("frontend plugin service allowlist contains invalid target %q/%q", target.Module, target.Service)
		}
		if target.Module == moduleName {
			return nil, fmt.Errorf("frontend plugin service target %q/%q is not external", target.Module, target.Service)
		}
		if target.Endpoint != "connect" && target.Endpoint != "rest" {
			return nil, fmt.Errorf(
				"frontend plugin service target %q/%q has unsupported endpoint %q",
				target.Module,
				target.Service,
				target.Endpoint,
			)
		}
		key := target.Module + "/" + target.Service
		if targets[key] == nil {
			targets[key] = make(map[string]struct{})
		}
		targets[key][target.Endpoint] = struct{}{}
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dependencies := make([]generatedServiceDependency, 0, len(keys))
	for _, key := range keys {
		module, service, _ := strings.Cut(key, "/")
		dependency := generatedServiceDependency{Name: service, Module: module}
		endpoints := make([]string, 0, len(targets[key]))
		for endpoint := range targets[key] {
			endpoints = append(endpoints, endpoint)
		}
		sort.Strings(endpoints)
		for _, endpoint := range endpoints {
			dependency.Endpoints = append(dependency.Endpoints, generatedServiceEndpointReference{Name: endpoint})
		}
		dependencies = append(dependencies, dependency)
	}
	return dependencies, nil
}

func normalizeGeneratedDeploymentMetadata(
	moduleDir string,
	topology deploymentTopology,
) error {
	generatedRoot := filepath.Join(moduleDir, "deployment", "generated")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		return err
	}
	services := make([]any, 0, len(topology.Services))
	var publicEgress []any
	for _, service := range topology.Services {
		endpoints := make([]any, 0, len(service.Endpoints))
		for _, endpoint := range service.Endpoints {
			endpoints = append(endpoints, map[string]any{
				"name":       endpoint.Name,
				"api":        "CODEFLY_API_" + strings.ToUpper(endpoint.API),
				"visibility": "ENDPOINT_VISIBILITY_" + strings.ToUpper(endpoint.Visibility),
				"port":       endpoint.Port,
			})
		}
		dependencies := make([]any, 0, len(service.Dependencies))
		for _, dependency := range service.Dependencies {
			dependencies = append(dependencies, map[string]any{
				"service":   dependency.Service,
				"endpoints": dependency.Endpoints,
			})
		}
		entry := map[string]any{"name": service.Name, "endpoints": endpoints}
		if len(dependencies) > 0 {
			entry["dependencies"] = dependencies
		}
		services = append(services, entry)
		if len(service.PublicEgressPorts) > 0 {
			publicEgress = append(publicEgress, map[string]any{
				"service": service.Name,
				"ports":   service.PublicEgressPorts,
			})
		}
	}
	interfaces := make([]any, 0, len(topology.Interface))
	for _, exposed := range topology.Interface {
		interfaces = append(interfaces, map[string]any{
			"service":    exposed.Service,
			"endpoint":   exposed.Endpoint,
			"visibility": "ENDPOINT_VISIBILITY_" + strings.ToUpper(exposed.Visibility),
		})
	}
	catalog := map[string]any{
		"schema_version":      "saas.deployment.topology.v1",
		"module":              topology.Module.Name,
		"namespace":           topology.Module.Namespace,
		"services":            services,
		"interface_endpoints": interfaces,
		"public_egress":       publicEgress,
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(generatedRoot, "service-topology.json"), data, 0o644); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(generatedRoot, "accounts-routes.virtualservice.yaml")); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func findWorkspaceRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, workspaceYamlPath)); err == nil {
			return dir, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found above %s", workspaceYamlPath, start)
		}
		dir = parent
	}
}

func copyTree(src, dst, name string, skipGitOps bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skipGitOps && (rel == bundleRelativeDir ||
			strings.HasPrefix(rel, bundleRelativeDir+string(filepath.Separator))) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm(), rel == moduleYamlPath && name != "", name)
	})
}

func copyFile(srcPath, dstPath string, mode os.FileMode, rewriteName bool, name string) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	if rewriteName {
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("parse %s: %w", moduleYamlPath, err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("%s must contain one mapping", moduleYamlPath)
		}
		mapping := document.Content[0]
		found := false
		for index := 0; index < len(mapping.Content); index += 2 {
			if mapping.Content[index].Value == "name" {
				mapping.Content[index+1].Value = name
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s has no module name", moduleYamlPath)
		}
		data, err = yaml.Marshal(&document)
		if err != nil {
			return fmt.Errorf("render %s: %w", moduleYamlPath, err)
		}
		return os.WriteFile(dstPath, data, mode)
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: saas-starter <dir> <name>\n")
		os.Exit(1)
	}
	dir := strings.TrimSpace(os.Args[1])
	name := strings.TrimSpace(os.Args[2])
	if dir == "" || name == "" {
		fmt.Fprintf(os.Stderr, "Error: dir and name must be non-empty\n")
		os.Exit(1)
	}
	if err := Create(context.Background(), dir, name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
