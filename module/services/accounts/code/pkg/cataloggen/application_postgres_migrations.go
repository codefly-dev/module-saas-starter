package cataloggen

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const applicationPostgresMigrationSchemaVersion = "v1"

var postgresMigrationSourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type applicationDeploymentBindings struct {
	Version                  string                               `yaml:"version"`
	ModuleName               string                               `yaml:"module_name,omitempty"`
	PostgresMigrationSources []applicationPostgresMigrationSource `yaml:"postgres_migration_sources"`
}

type applicationPostgresMigrationSource struct {
	Service string `yaml:"service"`
	Name    string `yaml:"name"`
	Path    string `yaml:"path"`
}

// BuildDeploymentArtifactsWithApplicationBindings layers the two explicit
// application composition inputs over immutable Starter topology: frontend
// plugin service bindings and named Postgres migration sources. The latter is
// deliberately narrow; applications cannot replace arbitrary service specs.
func BuildDeploymentArtifactsWithApplicationBindings(
	serviceDocument, bindingDocument, allowlistDocument, applicationDocument []byte,
) (*DeploymentArtifacts, error) {
	artifacts, err := BuildDeploymentArtifactsWithFrontendPluginAllowlist(
		serviceDocument,
		bindingDocument,
		allowlistDocument,
	)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(applicationDocument)) == 0 {
		return artifacts, nil
	}

	bindings, err := decodeDeploymentBindings(bindingDocument)
	if err != nil {
		return nil, err
	}
	moduleName, sources, err := decodeApplicationDeploymentBindings(applicationDocument, bindings)
	if err != nil {
		return nil, err
	}
	if moduleName != "" {
		bindings.Module.Name = moduleName
		artifacts.ModuleManifest, err = renderModuleManifest(bindings)
		if err != nil {
			return nil, err
		}
	}
	for _, serviceName := range sortedApplicationServiceNames(sources) {
		serviceSources := sources[serviceName]
		for index := range bindings.Services {
			service := &bindings.Services[index]
			if service.Name != serviceName {
				continue
			}
			if service.Spec == nil {
				service.Spec = make(map[string]any)
			}
			if _, exists := service.Spec["migration-sources"]; exists {
				return nil, fmt.Errorf("postgres service %q already declares migration-sources in Starter topology", serviceName)
			}
			values := make([]any, 0, len(serviceSources))
			for _, source := range serviceSources {
				values = append(values, map[string]any{"name": source.Name, "path": source.Path})
			}
			service.Spec["migration-sources"] = values
			manifest, renderErr := renderServiceManifest(*service)
			if renderErr != nil {
				return nil, renderErr
			}
			artifacts.ServiceManifests[serviceName] = manifest
			break
		}
	}
	return artifacts, nil
}

func decodeApplicationDeploymentBindings(
	document []byte,
	bindings deploymentBindings,
) (string, map[string][]applicationPostgresMigrationSource, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	var application applicationDeploymentBindings
	if err := decoder.Decode(&application); err != nil {
		return "", nil, fmt.Errorf("decode application deployment bindings: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", nil, fmt.Errorf("decode application deployment bindings: trailing document")
	}
	if application.Version != applicationPostgresMigrationSchemaVersion {
		return "", nil, fmt.Errorf("unsupported application deployment bindings version %q", application.Version)
	}
	if application.ModuleName != "" && !endpointNamePattern.MatchString(application.ModuleName) {
		return "", nil, fmt.Errorf("application module name %q is invalid", application.ModuleName)
	}

	services := make(map[string]deploymentServiceBinding, len(bindings.Services))
	for _, service := range bindings.Services {
		services[service.Name] = service
	}
	result := make(map[string][]applicationPostgresMigrationSource)
	previous := ""
	for _, source := range application.PostgresMigrationSources {
		key := source.Service + "/" + source.Name
		if previous != "" && key <= previous {
			return "", nil, fmt.Errorf("application postgres migration sources are duplicate or unsorted at %q", key)
		}
		previous = key
		service, exists := services[source.Service]
		if !exists || service.Agent.Name != "postgres" {
			return "", nil, fmt.Errorf("application migration source %q targets unknown or non-Postgres service %q", key, source.Service)
		}
		if !postgresMigrationSourceNamePattern.MatchString(source.Name) {
			return "", nil, fmt.Errorf("application migration source %q has an unsafe name", key)
		}
		if err := validateApplicationMigrationPath(source.Path); err != nil {
			return "", nil, fmt.Errorf("application migration source %q: %w", key, err)
		}
		result[source.Service] = append(result[source.Service], source)
	}
	return application.ModuleName, result, nil
}

func validateApplicationMigrationPath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\\") {
		return fmt.Errorf("migration path must be a non-empty portable relative path")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean != path {
		return fmt.Errorf("migration path must already be clean")
	}
	return nil
}

func sortedApplicationServiceNames(sources map[string][]applicationPostgresMigrationSource) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
