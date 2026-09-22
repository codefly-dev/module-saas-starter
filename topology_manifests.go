package main

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
// services/<name>/service.codefly.yaml are authored, and the deployment
// topology the GitOps renderer works from is assembled from them here. A
// deployment fact no Codefly manifest field carries — endpoint ports, public
// egress, cluster-internal HTTP routes, bootstrap Jobs, Kubernetes identity —
// lives in the service's own manifest under spec.deployment.
const (
	deploymentSpecKey = "deployment"
	// deployJobsManifestPath carries store-writing deploy steps. A core
	// JobReference names a runnable job.codefly.yaml with an agent and an
	// execution; a deploy step runs a service's image once with a command, which
	// no manifest field expresses, so the steps keep this one file of their own.
	deployJobsManifestPath = "deployment/jobs.codefly.yaml"
)

type moduleTopologyManifest struct {
	Name         string         `yaml:"name"`
	Description  string         `yaml:"description"`
	ServiceEntry string         `yaml:"service-entry"`
	Agent        map[string]any `yaml:"agent,omitempty"`
	Interface    struct {
		Endpoints []topologyInterface `yaml:"endpoints"`
	} `yaml:"interface"`
}

type serviceTopologyManifest struct {
	Name                string         `yaml:"name"`
	Version             string         `yaml:"version"`
	Description         string         `yaml:"description"`
	Agent               map[string]any `yaml:"agent"`
	ServiceDependencies []struct {
		Name      string `yaml:"name"`
		Module    string `yaml:"module"`
		Endpoints []struct {
			Name string `yaml:"name"`
		} `yaml:"endpoints"`
	} `yaml:"service-dependencies"`
	WorkspaceConfigurationDependencies []string                             `yaml:"workspace-configuration-dependencies"`
	SecretServiceConfigurations        []topologySecretServiceConfiguration `yaml:"secret-service-configurations"`
	Endpoints                          []struct {
		Name       string `yaml:"name"`
		Visibility string `yaml:"visibility"`
		API        string `yaml:"api"`
	} `yaml:"endpoints"`
	Spec map[string]any `yaml:"spec"`
}

// deploymentSpecManifest is the spec.deployment block of a service manifest,
// decoded strictly so a misspelt key fails the compose instead of rendering a
// policy that gates nothing.
type deploymentSpecManifest struct {
	Kubernetes            *topologyKubernetesIdentity `yaml:"kubernetes,omitempty"`
	EndpointPorts         map[string]uint32           `yaml:"endpoint-ports"`
	PublicEgressPorts     []uint32                    `yaml:"public-egress-ports,omitempty"`
	InternalHTTPRoutes    []topologyInternalHTTPRoute `yaml:"internal-http-routes,omitempty"`
	BootstrapJobEndpoints []string                    `yaml:"bootstrap-job-endpoints,omitempty"`
}

type deployJobsManifest struct {
	Version    string              `yaml:"version"`
	DeployJobs []topologyDeployJob `yaml:"deploy_jobs"`
}

// assembleDeploymentTopology reads the module manifest and each declared
// service manifest and returns the topology the renderer validates and
// consumes. Services are ordered by name.
func assembleDeploymentTopology(moduleDir, moduleName string, services []serviceDefinition) (deploymentTopology, error) {
	data, err := os.ReadFile(filepath.Join(moduleDir, moduleYamlPath))
	if err != nil {
		return deploymentTopology{}, fmt.Errorf("read %s: %w", moduleYamlPath, err)
	}
	var module moduleTopologyManifest
	if err := yaml.Unmarshal(data, &module); err != nil {
		return deploymentTopology{}, fmt.Errorf("parse %s: %w", moduleYamlPath, err)
	}
	topology := deploymentTopology{
		Version: "v1",
		Module: topologyModule{
			Name:         module.Name,
			Namespace:    module.Name,
			ServiceEntry: module.ServiceEntry,
			Description:  module.Description,
			Agent:        module.Agent,
		},
		Interface: module.Interface.Endpoints,
	}
	if topology.Module.Name != moduleName {
		return deploymentTopology{}, fmt.Errorf("%s declares module %q, expected %q", moduleYamlPath, topology.Module.Name, moduleName)
	}
	ordered := append([]serviceDefinition(nil), services...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	for _, service := range ordered {
		entry, err := loadServiceTopology(moduleName, service)
		if err != nil {
			return deploymentTopology{}, err
		}
		topology.Services = append(topology.Services, entry)
	}
	jobs, err := loadDeployJobs(moduleDir)
	if err != nil {
		return deploymentTopology{}, err
	}
	topology.DeployJobs = jobs
	return topology, nil
}

func loadServiceTopology(moduleName string, service serviceDefinition) (topologyService, error) {
	data, err := os.ReadFile(filepath.Join(service.directory, "service.codefly.yaml"))
	if err != nil {
		return topologyService{}, fmt.Errorf("read service %q manifest: %w", service.name, err)
	}
	var manifest serviceTopologyManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return topologyService{}, fmt.Errorf("parse service %q manifest: %w", service.name, err)
	}
	if manifest.Name != service.name {
		return topologyService{}, fmt.Errorf("service %q manifest declares name %q", service.name, manifest.Name)
	}
	spec, err := decodeDeploymentSpecManifest(service.name, manifest.Spec)
	if err != nil {
		return topologyService{}, err
	}
	entry := topologyService{
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
			return topologyService{}, fmt.Errorf("service %q endpoint %q has no port under spec.%s.endpoint-ports", service.name, endpoint.Name, deploymentSpecKey)
		}
		seen[endpoint.Name] = true
		entry.Endpoints = append(entry.Endpoints, topologyEndpoint{Name: endpoint.Name, API: api, Visibility: visibility, Port: port})
	}
	for endpoint := range spec.EndpointPorts {
		if !seen[endpoint] {
			return topologyService{}, fmt.Errorf("service %q spec.%s.endpoint-ports names unknown endpoint %q", service.name, deploymentSpecKey, endpoint)
		}
	}
	for _, dependency := range manifest.ServiceDependencies {
		// A dependency on another module is a consumer composition's addition
		// (the frontend's plugin services); the module's own topology has no
		// edge to render for it.
		if dependency.Module != "" && dependency.Module != moduleName {
			continue
		}
		edge := topologyDependency{Service: dependency.Name}
		for _, endpoint := range dependency.Endpoints {
			edge.Endpoints = append(edge.Endpoints, endpoint.Name)
		}
		entry.Dependencies = append(entry.Dependencies, edge)
	}
	return entry, nil
}

func decodeDeploymentSpecManifest(service string, spec map[string]any) (deploymentSpecManifest, error) {
	raw, exists := spec[deploymentSpecKey]
	if !exists {
		return deploymentSpecManifest{}, fmt.Errorf("service %q manifest has no spec.%s block", service, deploymentSpecKey)
	}
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return deploymentSpecManifest{}, fmt.Errorf("service %q spec.%s: %w", service, deploymentSpecKey, err)
	}
	var decoded deploymentSpecManifest
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&decoded); err != nil {
		return deploymentSpecManifest{}, fmt.Errorf("service %q spec.%s: %w", service, deploymentSpecKey, err)
	}
	return decoded, nil
}

func loadDeployJobs(moduleDir string) ([]topologyDeployJob, error) {
	data, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(deployJobsManifestPath)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", deployJobsManifestPath, err)
	}
	var manifest deployJobsManifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", deployJobsManifestPath, err)
	}
	if manifest.Version != "v1" {
		return nil, fmt.Errorf("%s version %q is not supported", deployJobsManifestPath, manifest.Version)
	}
	return manifest.DeployJobs, nil
}
