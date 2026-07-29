package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/wool"
	"gopkg.in/yaml.v3"
)

const (
	moduleYamlPath    = "module.codefly.yaml"
	moduleDirName     = "module"
	sourceEnvVar      = "SAAS_STARTER_MODULE_SRC"
	gitOpsRelativeDir = "deployment/kustomize"
	workspaceYamlPath = "workspace.codefly.yaml"
)

// resolveSource finds the module/ directory to copy from.
// Priority: SAAS_STARTER_MODULE_SRC env var → <executable-dir>/module.
func resolveSource() (string, error) {
	if src := os.Getenv(sourceEnvVar); src != "" {
		return src, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("cannot eval executable symlinks: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), moduleDirName), nil
}

func Create(ctx context.Context, dir, name string) error {
	w := wool.Get(ctx).In("saas-starter::Create")

	src, err := resolveSource()
	if err != nil {
		return w.Wrapf(err, "cannot resolve source")
	}
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
	if err := normalizeDeploymentMetadata(stage); err != nil {
		return w.Wrapf(err, "cannot normalize module deployment metadata")
	}
	if err := generateGitOps(ctx, stage, workspace); err != nil {
		return w.Wrapf(err, "cannot generate GitOps manifests")
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

func copyModuleSource(
	src,
	dst,
	name string,
	consumer moduleManifest,
	preserveInventory bool,
) error {
	declared := make(map[string]*string, len(consumer.Services))
	for _, service := range consumer.Services {
		declared[service.Name] = service.Path
	}
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
		skip := slashRelative == gitOpsRelativeDir ||
			strings.HasPrefix(slashRelative, gitOpsRelativeDir+"/")
		if preserveInventory {
			skip = skip ||
				slashRelative == moduleYamlPath ||
				slashRelative == "deployment/topology.bindings.codefly.yaml" ||
				slashRelative == "deployment/generated" ||
				strings.HasPrefix(slashRelative, "deployment/generated/")
			parts := strings.Split(slashRelative, "/")
			if len(parts) >= 2 && parts[0] == "services" {
				override, exists := declared[parts[1]]
				if !exists {
					skip = true
				} else if override != nil && filepath.IsAbs(*override) {
					skip = true
				} else if override != nil {
					targetRelative = filepath.Join(
						"services",
						filepath.FromSlash(*override),
						filepath.Join(parts[2:]...),
					)
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
	topologyPath := filepath.Join(moduleDir, "deployment", "topology.bindings.codefly.yaml")
	data, err := os.ReadFile(topologyPath)
	if err != nil {
		return fmt.Errorf("read deployment topology: %w", err)
	}
	var topology deploymentTopology
	if err := yaml.Unmarshal(data, &topology); err != nil {
		return fmt.Errorf("parse deployment topology: %w", err)
	}
	declared := make(map[string]struct{}, len(manifest.Services))
	for _, service := range manifest.Services {
		declared[service.Name] = struct{}{}
	}
	previousName := topology.Module.Name
	previousNamespace := topology.Module.Namespace
	topology.Module.Name = manifest.Name
	topology.Module.Namespace = manifest.Name
	if manifest.ServiceEntry != "" {
		topology.Module.ServiceEntry = manifest.ServiceEntry
	}
	topology.Services = slices.DeleteFunc(topology.Services, func(service topologyService) bool {
		return !containsService(declared, service.Name)
	})
	for index := range topology.Services {
		topology.Services[index].Dependencies = slices.DeleteFunc(
			topology.Services[index].Dependencies,
			func(dependency topologyDependency) bool {
				return !containsService(declared, dependency.Service)
			},
		)
	}
	topology.Interface = slices.DeleteFunc(topology.Interface, func(exposed topologyInterface) bool {
		return !containsService(declared, exposed.Service)
	})
	if _, exists := declared[topology.Module.ServiceEntry]; !exists {
		return fmt.Errorf("deployment service entry %q is not declared by module %q", topology.Module.ServiceEntry, manifest.Name)
	}
	encoded, err := yaml.Marshal(topology)
	if err != nil {
		return fmt.Errorf("render deployment topology: %w", err)
	}
	if err := os.WriteFile(topologyPath, encoded, 0o644); err != nil {
		return err
	}
	return normalizeGeneratedDeploymentMetadata(moduleDir, topology, previousName, previousNamespace)
}

func containsService(services map[string]struct{}, name string) bool {
	_, exists := services[name]
	return exists
}

func normalizeGeneratedDeploymentMetadata(
	moduleDir string,
	topology deploymentTopology,
	previousName,
	previousNamespace string,
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
	return normalizeGeneratedGatewayRoute(generatedRoot, topology, previousName, previousNamespace)
}

func normalizeGeneratedGatewayRoute(
	generatedRoot string,
	topology deploymentTopology,
	previousName,
	previousNamespace string,
) error {
	routePath := filepath.Join(generatedRoot, "accounts-routes.virtualservice.yaml")
	if _, exists := topologyServiceByName(topology, "accounts"); !exists {
		if err := os.Remove(routePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := os.ReadFile(routePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var route map[string]any
	if err := yaml.Unmarshal(data, &route); err != nil {
		return fmt.Errorf("parse generated gateway route: %w", err)
	}
	metadata, ok := route["metadata"].(map[string]any)
	if !ok {
		return errors.New("generated gateway route metadata must be an object")
	}
	metadata["name"] = topology.Module.Name + "-accounts-catalog"
	metadata["namespace"] = topology.Module.Namespace
	spec, ok := route["spec"].(map[string]any)
	if !ok {
		return errors.New("generated gateway route spec must be an object")
	}
	spec["gateways"] = []any{topology.Module.Name}
	rewriteGeneratedRouteStrings(
		spec["http"],
		previousName,
		previousNamespace,
		topology.Module.Name,
		topology.Module.Namespace,
		topology.Module.ServiceEntry,
	)
	encoded, err := yaml.Marshal(route)
	if err != nil {
		return err
	}
	return os.WriteFile(routePath, encoded, 0o644)
}

func rewriteGeneratedRouteStrings(
	value any,
	previousName,
	previousNamespace,
	moduleName,
	namespace,
	serviceEntry string,
) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			rewriteGeneratedRouteStrings(item, previousName, previousNamespace, moduleName, namespace, serviceEntry)
		}
	case map[string]any:
		for key, item := range typed {
			text, ok := item.(string)
			if !ok {
				rewriteGeneratedRouteStrings(item, previousName, previousNamespace, moduleName, namespace, serviceEntry)
				continue
			}
			if text == previousName {
				typed[key] = moduleName
				continue
			}
			suffix := "." + previousNamespace + ".svc.cluster.local"
			if strings.HasSuffix(text, suffix) {
				typed[key] = serviceEntry + "." + namespace + ".svc.cluster.local"
			}
		}
	}
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
		if skipGitOps && (rel == gitOpsRelativeDir ||
			strings.HasPrefix(rel, gitOpsRelativeDir+string(filepath.Separator))) {
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
