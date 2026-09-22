package cataloggen

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// The manifests are the model. module.codefly.yaml and every
// services/<name>/service.codefly.yaml are authored; the deployment projections
// this package renders (the DeploymentCatalog, the NetworkPolicy and mesh policy
// baselines) are assembled from them and nothing else. Deployment facts a
// Codefly manifest field cannot express — endpoint ports, egress, cluster-internal
// HTTP routes, bootstrap Jobs, Kubernetes identity — live in each service's own
// manifest under spec.deployment, next to the service they describe.
const (
	moduleManifestName  = "module.codefly.yaml"
	serviceManifestName = "service.codefly.yaml"
	// deployJobsManifestPath carries store-writing deploy steps the module bundle
	// emits. A core JobReference names a runnable job.codefly.yaml (an agent, an
	// execution); a deploy step runs a service's image once with a command, so it
	// has no Codefly manifest field and keeps this one file of its own.
	deployJobsManifestPath = "deployment/jobs.codefly.yaml"
	deploymentSpecKey      = "deployment"
	// topologySource names what the generated projections are derived from, for
	// the header each of them carries.
	topologySource = "module.codefly.yaml and services/*/service.codefly.yaml"
)

// DeploymentDocuments is the authored input: the module manifest and each
// service manifest it declares, plus the optional deploy-jobs file.
type DeploymentDocuments struct {
	Module   []byte
	Services map[string][]byte
	Jobs     []byte
}

type moduleManifestDocument struct {
	Kind         string                  `yaml:"kind"`
	Name         string                  `yaml:"name"`
	Description  string                  `yaml:"description"`
	ServiceEntry string                  `yaml:"service-entry"`
	Agent        *deploymentAgentBinding `yaml:"agent,omitempty"`
	Interface    struct {
		Endpoints []deploymentInterfaceBinding `yaml:"endpoints"`
	} `yaml:"interface"`
	Services []struct {
		Name string `yaml:"name"`
		Path string `yaml:"path,omitempty"`
	} `yaml:"services"`
}

type serviceManifestDocument struct {
	Name                               string                       `yaml:"name"`
	Version                            string                       `yaml:"version"`
	Description                        string                       `yaml:"description,omitempty"`
	Agent                              deploymentAgentBinding       `yaml:"agent"`
	ServiceDependencies                []manifestServiceDependency  `yaml:"service-dependencies,omitempty"`
	WorkspaceConfigurationDependencies []string                     `yaml:"workspace-configuration-dependencies,omitempty"`
	SecretServiceConfigurations        []secretServiceConfiguration `yaml:"secret-service-configurations,omitempty"`
	Endpoints                          []manifestEndpoint           `yaml:"endpoints"`
	Spec                               map[string]any               `yaml:"spec,omitempty"`
}

// deploymentSpec is the spec.deployment block of a service manifest: the facts
// the Kubernetes and mesh projections need that no other manifest field states.
// Decoded strictly, so a misspelt key fails here rather than rendering nothing.
type deploymentSpec struct {
	Kubernetes            *kubernetesIdentityBinding    `yaml:"kubernetes,omitempty"`
	EndpointPorts         map[string]uint32             `yaml:"endpoint-ports"`
	PublicEgressPorts     []uint32                      `yaml:"public-egress-ports,omitempty"`
	InternalHTTPRoutes    []deploymentInternalHTTPRoute `yaml:"internal-http-routes,omitempty"`
	BootstrapJobEndpoints []string                      `yaml:"bootstrap-job-endpoints,omitempty"`
}

type deployJobsDocument struct {
	Version    string                       `yaml:"version"`
	DeployJobs []deploymentDeployJobBinding `yaml:"deploy_jobs"`
}

// LoadDeploymentDocuments reads the module manifest and the manifest of every
// service it declares from a module tree.
func LoadDeploymentDocuments(moduleDir string) (DeploymentDocuments, error) {
	documents := DeploymentDocuments{Services: make(map[string][]byte)}
	module, err := os.ReadFile(filepath.Join(moduleDir, moduleManifestName))
	if err != nil {
		return DeploymentDocuments{}, fmt.Errorf("read %s: %w", moduleManifestName, err)
	}
	documents.Module = module
	var manifest moduleManifestDocument
	if err := yaml.Unmarshal(module, &manifest); err != nil {
		return DeploymentDocuments{}, fmt.Errorf("decode %s: %w", moduleManifestName, err)
	}
	for _, reference := range manifest.Services {
		directory := reference.Name
		if reference.Path != "" {
			directory = reference.Path
		}
		path := filepath.Join(moduleDir, "services", directory, serviceManifestName)
		if filepath.IsAbs(directory) {
			path = filepath.Join(directory, serviceManifestName)
		}
		document, err := os.ReadFile(path)
		if err != nil {
			return DeploymentDocuments{}, fmt.Errorf("read service %q manifest: %w", reference.Name, err)
		}
		documents.Services[reference.Name] = document
	}
	jobs, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(deployJobsManifestPath)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return DeploymentDocuments{}, fmt.Errorf("read %s: %w", deployJobsManifestPath, err)
	}
	documents.Jobs = jobs
	return documents, nil
}

// assembleDeploymentBindings turns the authored manifests into the one in-memory
// topology the projections are rendered from. Every field comes from exactly one
// manifest; there is no second document to disagree with.
func assembleDeploymentBindings(documents DeploymentDocuments) (deploymentBindings, error) {
	var module moduleManifestDocument
	if err := yaml.Unmarshal(documents.Module, &module); err != nil {
		return deploymentBindings{}, fmt.Errorf("decode %s: %w", moduleManifestName, err)
	}
	if module.Kind != "module" {
		return deploymentBindings{}, fmt.Errorf("%s kind %q is not a module", moduleManifestName, module.Kind)
	}
	bindings := deploymentBindings{
		Version: "v1",
		Module: deploymentModuleBinding{
			Name:         module.Name,
			Namespace:    module.Name,
			ServiceEntry: module.ServiceEntry,
			Description:  module.Description,
			Agent:        module.Agent,
		},
		Interface: module.Interface.Endpoints,
	}
	declared := make(map[string]bool, len(module.Services))
	for _, reference := range module.Services {
		if declared[reference.Name] {
			return deploymentBindings{}, fmt.Errorf("%s declares service %q twice", moduleManifestName, reference.Name)
		}
		declared[reference.Name] = true
		document, exists := documents.Services[reference.Name]
		if !exists {
			return deploymentBindings{}, fmt.Errorf("service %q is declared by %s but has no %s", reference.Name, moduleManifestName, serviceManifestName)
		}
		service, err := assembleServiceBinding(module.Name, reference.Name, document)
		if err != nil {
			return deploymentBindings{}, err
		}
		bindings.Services = append(bindings.Services, service)
	}
	undeclared := make([]string, 0)
	for name := range documents.Services {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return deploymentBindings{}, fmt.Errorf("services %v carry a manifest but are not declared by %s", undeclared, moduleManifestName)
	}
	if len(bytes.TrimSpace(documents.Jobs)) > 0 {
		var jobs deployJobsDocument
		decoder := yaml.NewDecoder(bytes.NewReader(documents.Jobs))
		decoder.KnownFields(true)
		if err := decoder.Decode(&jobs); err != nil {
			return deploymentBindings{}, fmt.Errorf("decode %s: %w", deployJobsManifestPath, err)
		}
		if jobs.Version != "v1" {
			return deploymentBindings{}, fmt.Errorf("unsupported %s version %q", deployJobsManifestPath, jobs.Version)
		}
		bindings.DeployJobs = jobs.DeployJobs
	}
	return bindings, nil
}

func assembleServiceBinding(moduleName, name string, document []byte) (deploymentServiceBinding, error) {
	var manifest serviceManifestDocument
	if err := yaml.Unmarshal(document, &manifest); err != nil {
		return deploymentServiceBinding{}, fmt.Errorf("decode service %q manifest: %w", name, err)
	}
	if manifest.Name != name {
		return deploymentServiceBinding{}, fmt.Errorf("service %q manifest declares name %q", name, manifest.Name)
	}
	spec, err := decodeDeploymentSpec(name, manifest.Spec)
	if err != nil {
		return deploymentServiceBinding{}, err
	}
	service := deploymentServiceBinding{
		Name:                               manifest.Name,
		Version:                            manifest.Version,
		Description:                        manifest.Description,
		Agent:                              manifest.Agent,
		Kubernetes:                         spec.Kubernetes,
		WorkspaceConfigurationDependencies: manifest.WorkspaceConfigurationDependencies,
		SecretServiceConfigurations:        manifest.SecretServiceConfigurations,
		BootstrapJobEndpoints:              spec.BootstrapJobEndpoints,
		InternalHTTPRoutes:                 spec.InternalHTTPRoutes,
		PublicEgressPorts:                  spec.PublicEgressPorts,
		Spec:                               manifest.Spec,
	}
	seen := make(map[string]bool, len(manifest.Endpoints))
	for _, endpoint := range manifest.Endpoints {
		// The manifest's own defaults: an endpoint's API is its name unless it
		// says otherwise, and an endpoint is private unless it says otherwise.
		api := endpoint.API
		if api == "" {
			api = endpoint.Name
		}
		visibility := endpoint.Visibility
		if visibility == "" {
			visibility = "private"
		}
		port, declared := spec.EndpointPorts[endpoint.Name]
		if !declared {
			return deploymentServiceBinding{}, fmt.Errorf("service %q endpoint %q has no port under spec.%s.endpoint-ports", name, endpoint.Name, deploymentSpecKey)
		}
		seen[endpoint.Name] = true
		service.Endpoints = append(service.Endpoints, deploymentEndpointBinding{
			Name: endpoint.Name, API: api, Visibility: visibility, Port: port,
		})
	}
	for endpoint := range spec.EndpointPorts {
		if !seen[endpoint] {
			return deploymentServiceBinding{}, fmt.Errorf("service %q spec.%s.endpoint-ports names unknown endpoint %q", name, deploymentSpecKey, endpoint)
		}
	}
	for _, dependency := range manifest.ServiceDependencies {
		// A dependency on another module is a consumer composition's addition
		// (the frontend's plugin services); the module's own topology has no
		// edge to render for it.
		if dependency.Module != "" && dependency.Module != moduleName {
			continue
		}
		entry := deploymentDependencyBinding{Service: dependency.Name}
		for _, endpoint := range dependency.Endpoints {
			entry.Endpoints = append(entry.Endpoints, endpoint.Name)
		}
		service.Dependencies = append(service.Dependencies, entry)
	}
	return service, nil
}

func decodeDeploymentSpec(service string, spec map[string]any) (deploymentSpec, error) {
	raw, exists := spec[deploymentSpecKey]
	if !exists {
		return deploymentSpec{}, fmt.Errorf("service %q manifest has no spec.%s block", service, deploymentSpecKey)
	}
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return deploymentSpec{}, fmt.Errorf("service %q spec.%s: %w", service, deploymentSpecKey, err)
	}
	var decoded deploymentSpec
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&decoded); err != nil {
		return deploymentSpec{}, fmt.Errorf("service %q spec.%s: %w", service, deploymentSpecKey, err)
	}
	return decoded, nil
}
