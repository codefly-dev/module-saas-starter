package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const moduleBundleSchema = "codefly.dev/module-bundle/v1"

var (
	dnsLabelPattern   = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
	unresolvedPattern = regexp.MustCompile(`(?i)REPLACE_ME|saas-starter|\$\{[^}]+\}|<[^>]*replace[^>]*>`)
)

type moduleManifest struct {
	Name         string             `yaml:"name"`
	ServiceEntry string             `yaml:"service-entry,omitempty"`
	Services     []serviceReference `yaml:"services"`
}

type workspaceManifest struct {
	Name         string               `yaml:"name"`
	Environments []*environmentConfig `yaml:"environments,omitempty"`
	root         string
}

type environmentConfig struct {
	Name            string                          `yaml:"name"`
	Namespace       string                          `yaml:"namespace"`
	Cluster         environmentCluster              `yaml:"cluster"`
	Ingress         []environmentIngressRoute       `yaml:"ingress,omitempty"`
	ManagedServices map[string]managedServiceConfig `yaml:"managed-services,omitempty"`
}

type environmentCluster struct {
	Kind string `yaml:"kind"`
}

type environmentIngressRoute struct {
	Name     string   `yaml:"name"`
	Service  string   `yaml:"service"`
	Endpoint string   `yaml:"endpoint"`
	Hosts    []string `yaml:"hosts"`
}

type serviceReference struct {
	Name string  `yaml:"name"`
	Path *string `yaml:"path,omitempty"`
}

type serviceDefinition struct {
	name      string
	directory string
}

// moduleBundle is the typed, transport-neutral output the module plugin emits.
// It carries the module identity and, per declared environment, the
// module-owned Kubernetes overlay path plus the topology and placement metadata
// a promotion driver needs to compose repository transport (Argo, Flux, or
// otherwise) on its own. The plugin never records repositories, revisions, or
// Argo resources.
type moduleBundle struct {
	SchemaVersion string              `json:"schemaVersion"`
	Module        string              `json:"module"`
	Namespace     string              `json:"namespace"`
	ServiceEntry  string              `json:"serviceEntry"`
	Environments  []bundleEnvironment `json:"environments"`
}

type bundleEnvironment struct {
	Name                   string                  `json:"name"`
	Namespace              string                  `json:"namespace"`
	Cluster                string                  `json:"cluster"`
	ResourcePath           string                  `json:"resourcePath"`
	Services               []string                `json:"services"`
	Ingress                []bundleIngressRoute    `json:"ingress"`
	ManagedServiceHandoffs []managedServiceHandoff `json:"managedServiceHandoffs,omitempty"`
}

type bundleIngressRoute struct {
	Name     string   `json:"name"`
	Service  string   `json:"service"`
	Endpoint string   `json:"endpoint"`
	Port     uint32   `json:"port"`
	Hosts    []string `json:"hosts"`
}

type managedServiceHandoff struct {
	Service          string   `json:"service"`
	AWSKind          string   `json:"awsKind"`
	ExternalName     string   `json:"externalName"`
	SecretReferences []string `json:"secretReferences,omitempty"`
}

type managedServiceConfig struct {
	Kind             string                   `yaml:"kind"`
	ExternalName     string                   `yaml:"external-name"`
	EgressCIDRs      []string                 `yaml:"egress-cidrs,omitempty"`
	SecretReferences []managedSecretReference `yaml:"secret-references,omitempty"`
}

type managedSecretReference struct {
	Name        string         `yaml:"name"`
	RemoteKey   string         `yaml:"remote-key"`
	SecretStore secretStoreRef `yaml:"secret-store"`
}

type secretStoreRef struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

type objectMeta struct {
	Name       string            `yaml:"name"`
	Namespace  string            `yaml:"namespace,omitempty"`
	Labels     map[string]string `yaml:"labels,omitempty"`
	Finalizers []string          `yaml:"finalizers,omitempty"`
}

type kubeObject struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Spec       any        `yaml:"spec,omitempty"`
}

type kustomization struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Resources  []string `yaml:"resources"`
}

type environmentPlan struct {
	environment *environmentConfig
	cluster     string
	services    []string
	ingress     []ingressRoutePlan
	handoffs    []managedServiceHandoff
	managed     map[string]managedServiceConfig
}

type ingressRoutePlan struct {
	name     string
	service  string
	endpoint string
	port     uint32
	hosts    []string
}

type deploymentTopology struct {
	Version   string              `yaml:"version"`
	Module    topologyModule      `yaml:"module"`
	Interface []topologyInterface `yaml:"interface"`
	Services  []topologyService   `yaml:"services"`
}

type topologyModule struct {
	Name         string         `yaml:"name"`
	Namespace    string         `yaml:"namespace"`
	ServiceEntry string         `yaml:"service_entry"`
	Description  string         `yaml:"description"`
	Agent        map[string]any `yaml:"agent,omitempty"`
}

type topologyInterface struct {
	Service    string `yaml:"service"`
	Endpoint   string `yaml:"endpoint"`
	Visibility string `yaml:"visibility"`
}

type topologyService struct {
	Name                               string                               `yaml:"name"`
	Version                            string                               `yaml:"version,omitempty"`
	Description                        string                               `yaml:"description,omitempty"`
	Agent                              map[string]any                       `yaml:"agent,omitempty"`
	WorkspaceConfigurationDependencies []string                             `yaml:"workspace_configuration_dependencies,omitempty"`
	SecretServiceConfigurations        []topologySecretServiceConfiguration `yaml:"secret_service_configurations,omitempty"`
	Endpoints                          []topologyEndpoint                   `yaml:"endpoints"`
	BootstrapJobEndpoints              []string                             `yaml:"bootstrap_job_endpoints,omitempty"`
	Dependencies                       []topologyDependency                 `yaml:"dependencies,omitempty"`
	PublicEgressPorts                  []uint32                             `yaml:"public_egress_ports,omitempty"`
	Spec                               map[string]any                       `yaml:"spec,omitempty"`
}

type topologySecretServiceConfiguration struct {
	Name    string                                    `yaml:"name"`
	Entries []topologySecretServiceConfigurationEntry `yaml:"entries"`
}

type topologySecretServiceConfigurationEntry struct {
	Key string `yaml:"key"`
}

type topologyEndpoint struct {
	Name       string `yaml:"name"`
	API        string `yaml:"api,omitempty"`
	Visibility string `yaml:"visibility"`
	Port       uint32 `yaml:"port"`
}

type topologyDependency struct {
	Service   string   `yaml:"service"`
	Endpoints []string `yaml:"endpoints"`
}

func loadWorkspaceManifest(root string) (*workspaceManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, workspaceYamlPath))
	if err != nil {
		return nil, err
	}
	var workspace workspaceManifest
	if err := yaml.Unmarshal(data, &workspace); err != nil {
		return nil, err
	}
	workspace.root = root
	return &workspace, nil
}

// generateDeploymentBundle renders the module-owned Kubernetes overlay for every
// declared environment and writes a typed, transport-neutral bundle manifest.
// It requires no Git binary, checkout, network, or cluster access.
func generateDeploymentBundle(moduleDir string, workspace *workspaceManifest) error {
	manifest, err := loadModuleManifest(moduleDir)
	if err != nil {
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
	environments, err := selectEnvironments(workspace)
	if err != nil {
		return err
	}

	root := filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir))
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return fmt.Errorf("create deployment directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), ".kustomize-stage-*")
	if err != nil {
		return fmt.Errorf("create bundle staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	bundle := moduleBundle{
		SchemaVersion: moduleBundleSchema,
		Module:        manifest.Name,
		Namespace:     topology.Module.Namespace,
		ServiceEntry:  topology.Module.ServiceEntry,
	}
	for _, environment := range environments {
		_, aws, kind, err := classifyEnvironment(environment)
		if err != nil {
			return err
		}
		plan, err := planEnvironment(environment, serviceNames(services), topology, kind, aws)
		if err != nil {
			return err
		}
		if err := renderEnvironment(stage, workspace.Name, manifest.Name, topology, plan); err != nil {
			return err
		}
		bundle.Environments = append(bundle.Environments, bundleEnvironment{
			Name:                   plan.environment.Name,
			Namespace:              plan.environment.Namespace,
			Cluster:                plan.cluster,
			ResourcePath:           path.Join("overlays", plan.environment.Name),
			Services:               append([]string(nil), plan.services...),
			Ingress:                bundleIngress(plan.ingress),
			ManagedServiceHandoffs: append([]managedServiceHandoff(nil), plan.handoffs...),
		})
	}
	if err := writeJSON(filepath.Join(stage, "bundle.json"), bundle); err != nil {
		return err
	}
	if err := validateGeneratedBundle(stage, bundle); err != nil {
		return err
	}
	return replaceGeneratedTree(stage, root)
}

func bundleIngress(routes []ingressRoutePlan) []bundleIngressRoute {
	result := make([]bundleIngressRoute, 0, len(routes))
	for _, route := range routes {
		result = append(result, bundleIngressRoute{
			Name:     route.name,
			Service:  route.service,
			Endpoint: route.endpoint,
			Port:     route.port,
			Hosts:    append([]string(nil), route.hosts...),
		})
	}
	return result
}

func replaceGeneratedTree(stage, root string) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, root); err != nil {
			return fmt.Errorf("install generated bundle directory: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect generated bundle directory: %w", err)
	}

	backup, err := os.MkdirTemp(filepath.Dir(root), ".kustomize-backup-*")
	if err != nil {
		return fmt.Errorf("create bundle backup path: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("prepare bundle backup path: %w", err)
	}
	if err := os.Rename(root, backup); err != nil {
		return fmt.Errorf("back up generated bundle directory: %w", err)
	}
	if err := os.Rename(stage, root); err != nil {
		if rollbackErr := os.Rename(backup, root); rollbackErr != nil {
			return fmt.Errorf("install generated bundle directory: %w (rollback failed: %v; previous tree remains at %s)", err, rollbackErr, backup)
		}
		return fmt.Errorf("install generated bundle directory: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove previous bundle directory: %w", err)
	}
	return nil
}

func loadModuleManifest(moduleDir string) (moduleManifest, error) {
	data, err := os.ReadFile(filepath.Join(moduleDir, moduleYamlPath))
	if err != nil {
		return moduleManifest{}, fmt.Errorf("read %s: %w", moduleYamlPath, err)
	}
	var manifest moduleManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return moduleManifest{}, fmt.Errorf("parse %s: %w", moduleYamlPath, err)
	}
	if err := validateDNSLabel("module name", manifest.Name); err != nil {
		return moduleManifest{}, err
	}
	if len(manifest.Services) == 0 {
		return moduleManifest{}, fmt.Errorf("module %q declares no services", manifest.Name)
	}
	return manifest, nil
}

func validateServiceInventory(moduleDir string, references []serviceReference) ([]serviceDefinition, error) {
	declared := make(map[string]string, len(references))
	declaredPaths := make(map[string]string, len(references))
	services := make([]serviceDefinition, 0, len(references))
	serviceRoot := filepath.Join(moduleDir, "services")
	for _, reference := range references {
		if err := validateDNSLabel("service name", reference.Name); err != nil {
			return nil, err
		}
		if _, exists := declared[reference.Name]; exists {
			return nil, fmt.Errorf("module declares service %q more than once", reference.Name)
		}
		servicePath := filepath.Join(serviceRoot, reference.Name)
		displayPath := reference.Name
		if reference.Path != nil {
			override := *reference.Path
			if strings.ContainsRune(override, '\x00') {
				return nil, fmt.Errorf("service %q path contains NUL", reference.Name)
			}
			if filepath.IsAbs(override) {
				servicePath = filepath.Clean(override)
				displayPath = servicePath
			} else {
				relative, err := cleanRelativeFilesystemPath(override)
				if err != nil {
					return nil, fmt.Errorf("service %q path: %w", reference.Name, err)
				}
				servicePath = filepath.Join(serviceRoot, relative)
				displayPath = filepath.ToSlash(relative)
			}
		}
		canonicalPath, err := filepath.Abs(servicePath)
		if err != nil {
			return nil, fmt.Errorf("service %q path: %w", reference.Name, err)
		}
		if owner, exists := declaredPaths[canonicalPath]; exists {
			return nil, fmt.Errorf("services %q and %q declare the same path %q", owner, reference.Name, displayPath)
		}
		declared[reference.Name] = canonicalPath
		declaredPaths[canonicalPath] = reference.Name
		services = append(services, serviceDefinition{name: reference.Name, directory: canonicalPath})

		if !filepath.IsAbs(valueOrEmpty(reference.Path)) {
			current := serviceRoot
			relative, err := filepath.Rel(serviceRoot, canonicalPath)
			if err != nil {
				return nil, fmt.Errorf("service %q path: %w", reference.Name, err)
			}
			for _, element := range strings.Split(relative, string(filepath.Separator)) {
				current = filepath.Join(current, element)
				info, statErr := os.Lstat(current)
				if statErr != nil {
					return nil, fmt.Errorf("declared service %q path %q is missing: %w", reference.Name, displayPath, statErr)
				}
				if !info.IsDir() {
					return nil, fmt.Errorf("declared service %q path %q is not a directory", reference.Name, displayPath)
				}
			}
		} else {
			info, statErr := os.Stat(canonicalPath)
			if statErr != nil {
				return nil, fmt.Errorf("declared service %q path %q is missing: %w", reference.Name, displayPath, statErr)
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("declared service %q path %q is not a directory", reference.Name, displayPath)
			}
		}
	}

	expectedPaths := make(map[string]struct{}, len(declared))
	for _, absolute := range declared {
		relative, err := filepath.Rel(serviceRoot, absolute)
		if err == nil && filepath.IsLocal(relative) {
			expectedPaths[relative] = struct{}{}
		}
	}
	if _, err := os.Stat(serviceRoot); errors.Is(err, os.ErrNotExist) && len(expectedPaths) == 0 {
		return services, nil
	}
	if err := filepath.WalkDir(serviceRoot, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || file == serviceRoot {
			return nil
		}
		relative, err := filepath.Rel(serviceRoot, file)
		if err != nil {
			return err
		}
		if _, exists := expectedPaths[relative]; exists {
			return filepath.SkipDir
		}
		prefix := relative + string(filepath.Separator)
		for expected := range expectedPaths {
			if strings.HasPrefix(expected, prefix) {
				return nil
			}
		}
		return fmt.Errorf("service path %q has no module service declaration", filepath.ToSlash(relative))
	}); err != nil {
		return nil, fmt.Errorf("validate service paths: %w", err)
	}
	return services, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func serviceNames(services []serviceDefinition) []string {
	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.name)
	}
	return names
}

func selectEnvironments(workspace *workspaceManifest) ([]*environmentConfig, error) {
	if err := validateDNSLabel("workspace name", workspace.Name); err != nil {
		return nil, err
	}
	if len(workspace.Environments) == 0 {
		return nil, fmt.Errorf("workspace %q declares no deployment environments", workspace.Name)
	}
	seen := make(map[string]struct{}, len(workspace.Environments))
	for _, environment := range workspace.Environments {
		if environment == nil {
			return nil, fmt.Errorf("workspace contains an empty environment")
		}
		if err := validateDNSLabel("environment name", environment.Name); err != nil {
			return nil, err
		}
		if _, exists := seen[environment.Name]; exists {
			return nil, fmt.Errorf("workspace declares environment %q more than once", environment.Name)
		}
		seen[environment.Name] = struct{}{}
		if err := validateDNSLabel("environment namespace", environment.Namespace); err != nil {
			return nil, fmt.Errorf("environment %q: %w", environment.Name, err)
		}
	}
	return workspace.Environments, nil
}

func planEnvironment(
	environment *environmentConfig,
	services []string,
	topology deploymentTopology,
	cluster string,
	aws bool,
) (environmentPlan, error) {
	plan := environmentPlan{
		environment: environment,
		cluster:     cluster,
		managed:     make(map[string]managedServiceConfig),
	}
	if err := validateManagedServices(environment, services, topology, aws, &plan); err != nil {
		return environmentPlan{}, err
	}
	for _, service := range services {
		if _, managed := plan.managed[service]; !managed {
			plan.services = append(plan.services, service)
		}
	}
	slices.Sort(plan.services)
	if err := validateIngressRoutes(environment, topology, &plan); err != nil {
		return environmentPlan{}, err
	}
	return plan, nil
}

func validateIngressRoutes(
	environment *environmentConfig,
	topology deploymentTopology,
	plan *environmentPlan,
) error {
	// Public ingress is optional: an environment may front the mesh with a
	// gateway this module does not own, or expose nothing publicly yet. Such an
	// environment renders module-owned baseline resources without a Gateway.
	if len(environment.Ingress) == 0 {
		return nil
	}

	routeNames := make(map[string]struct{}, len(environment.Ingress))
	hosts := make(map[string]string)
	for _, route := range environment.Ingress {
		if err := validateDNSLabel("ingress route name", route.Name); err != nil {
			return fmt.Errorf("environment %q: %w", environment.Name, err)
		}
		if _, duplicate := routeNames[route.Name]; duplicate {
			return fmt.Errorf("environment %q repeats ingress route %q", environment.Name, route.Name)
		}
		routeNames[route.Name] = struct{}{}
		if !slices.Contains(plan.services, route.Service) {
			return fmt.Errorf(
				"environment %q ingress route %q targets service %q outside its in-cluster inventory",
				environment.Name,
				route.Name,
				route.Service,
			)
		}
		service, exists := topologyServiceByName(topology, route.Service)
		if !exists {
			return fmt.Errorf(
				"environment %q ingress route %q targets unknown service %q",
				environment.Name,
				route.Name,
				route.Service,
			)
		}
		endpoint, exists := topologyEndpointByName(service, route.Endpoint)
		if !exists || !topologyExposesPublicEndpoint(topology, route.Service, route.Endpoint) {
			return fmt.Errorf(
				"environment %q ingress route %q target %s/%s is not a public module interface",
				environment.Name,
				route.Name,
				route.Service,
				route.Endpoint,
			)
		}
		if len(route.Hosts) == 0 {
			return fmt.Errorf("environment %q ingress route %q declares no exact hosts", environment.Name, route.Name)
		}
		planned := ingressRoutePlan{
			name:     route.Name,
			service:  route.Service,
			endpoint: route.Endpoint,
			port:     endpoint.Port,
		}
		for _, host := range route.Hosts {
			host = strings.TrimSpace(host)
			if !validExternalName(host) {
				return fmt.Errorf(
					"environment %q ingress route %q host %q is not an exact DNS name",
					environment.Name,
					route.Name,
					host,
				)
			}
			if owner, duplicate := hosts[host]; duplicate {
				return fmt.Errorf(
					"environment %q ingress host %q is declared by routes %q and %q",
					environment.Name,
					host,
					owner,
					route.Name,
				)
			}
			hosts[host] = route.Name
			planned.hosts = append(planned.hosts, host)
		}
		plan.ingress = append(plan.ingress, planned)
	}
	return nil
}

func topologyExposesPublicEndpoint(topology deploymentTopology, service, endpoint string) bool {
	return slices.ContainsFunc(topology.Interface, func(exposed topologyInterface) bool {
		return exposed.Service == service &&
			exposed.Endpoint == endpoint &&
			exposed.Visibility == "public"
	})
}

func validateManagedServices(
	environment *environmentConfig,
	services []string,
	topology deploymentTopology,
	aws bool,
	plan *environmentPlan,
) error {
	if !aws && len(environment.ManagedServices) > 0 {
		return fmt.Errorf("environment %q declares managed services for an in-cluster environment", environment.Name)
	}
	declared := make(map[string]struct{}, len(services))
	for _, service := range services {
		declared[service] = struct{}{}
	}
	for service, config := range environment.ManagedServices {
		_, exists := declared[service]
		if !exists {
			return fmt.Errorf("environment %q declares unexpected managed service %q", environment.Name, service)
		}
		switch config.Kind {
		case "elasticache", "rds-postgresql", "s3", "secrets-manager":
		default:
			return fmt.Errorf("environment %q managed service %q kind %q is not supported", environment.Name, service, config.Kind)
		}
		if !validExternalName(config.ExternalName) {
			return fmt.Errorf("environment %q managed service %q external-name %q is not an exact DNS name", environment.Name, service, config.ExternalName)
		}
		for _, cidr := range config.EgressCIDRs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("environment %q managed service %q egress CIDR %q is invalid", environment.Name, service, cidr)
			}
		}
		referenceNames := make(map[string]struct{}, len(config.SecretReferences))
		handoff := managedServiceHandoff{
			Service:      service,
			AWSKind:      config.Kind,
			ExternalName: config.ExternalName,
		}
		for _, reference := range config.SecretReferences {
			if err := validateDNSLabel("managed secret reference name", reference.Name); err != nil {
				return fmt.Errorf("environment %q managed service %q: %w", environment.Name, service, err)
			}
			if _, duplicate := referenceNames[reference.Name]; duplicate {
				return fmt.Errorf("environment %q managed service %q repeats secret reference %q", environment.Name, service, reference.Name)
			}
			referenceNames[reference.Name] = struct{}{}
			if strings.TrimSpace(reference.RemoteKey) == "" ||
				strings.ContainsAny(reference.RemoteKey, "\r\n") ||
				strings.TrimSpace(reference.SecretStore.Name) == "" ||
				(reference.SecretStore.Kind != "SecretStore" && reference.SecretStore.Kind != "ClusterSecretStore") {
				return fmt.Errorf("environment %q managed service %q secret reference %q is incomplete", environment.Name, service, reference.Name)
			}
			handoff.SecretReferences = append(handoff.SecretReferences, reference.Name)
		}
		plan.managed[service] = config
		plan.handoffs = append(plan.handoffs, handoff)
	}
	slices.SortFunc(plan.handoffs, func(left, right managedServiceHandoff) int {
		return strings.Compare(left.Service, right.Service)
	})
	return validateManagedDependencyCIDRs(environment.Name, topology, plan.managed)
}

func validateManagedDependencyCIDRs(environment string, topology deploymentTopology, managed map[string]managedServiceConfig) error {
	for _, service := range topology.Services {
		for _, dependency := range service.Dependencies {
			config, exists := managed[dependency.Service]
			if exists && len(config.EgressCIDRs) == 0 {
				return fmt.Errorf(
					"environment %q managed service %q requires at least one exact egress CIDR for caller %q",
					environment,
					dependency.Service,
					service.Name,
				)
			}
		}
	}
	return nil
}

func validExternalName(value string) bool {
	name := strings.TrimSuffix(strings.TrimSpace(value), ".")
	if name == "" || net.ParseIP(name) != nil || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 || !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func classifyEnvironment(environment *environmentConfig) (local bool, aws bool, cluster string, err error) {
	kind := strings.TrimSpace(environment.Cluster.Kind)
	switch kind {
	case "k3d":
		return true, false, kind, nil
	case "eks":
		return false, true, kind, nil
	case "":
		if strings.HasPrefix(environment.Name, "local") {
			return true, false, "k3d", nil
		}
	}
	return false, false, "", fmt.Errorf("environment %q cluster kind %q is not supported by the SaaS deployment generator", environment.Name, kind)
}

func renderEnvironment(
	root,
	workspaceName,
	moduleName string,
	topology deploymentTopology,
	plan environmentPlan,
) error {
	environmentRoot := filepath.Join(root, "overlays", plan.environment.Name)
	resourceRoot := filepath.Join(environmentRoot, "resources")
	if err := os.MkdirAll(resourceRoot, 0o755); err != nil {
		return err
	}

	identityLabels := map[string]string{
		"app.kubernetes.io/managed-by": "codefly",
		"app.kubernetes.io/part-of":    kubernetesName(workspaceName, moduleName),
		"codefly.dev/workspace":        workspaceName,
		"codefly.dev/module":           moduleName,
		"codefly.dev/environment":      plan.environment.Name,
	}
	namespace := kubeObject{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: objectMeta{
			Name: plan.environment.Namespace,
			Labels: mergeLabels(identityLabels, map[string]string{
				"istio.io/dataplane-mode":                    "ambient",
				"pod-security.kubernetes.io/enforce":         "baseline",
				"pod-security.kubernetes.io/enforce-version": "latest",
				"pod-security.kubernetes.io/audit":           "restricted",
				"pod-security.kubernetes.io/audit-version":   "latest",
				"pod-security.kubernetes.io/warn":            "restricted",
				"pod-security.kubernetes.io/warn-version":    "latest",
				"kubernetes.io/metadata.name":                plan.environment.Namespace,
			}),
		},
	}
	if err := writeYAML(filepath.Join(resourceRoot, "namespace.yaml"), namespace); err != nil {
		return err
	}
	sharedPaths, err := renderSharedResources(resourceRoot, plan.environment.Namespace, identityLabels, topology, plan)
	if err != nil {
		return err
	}

	resourcePaths := []string{
		"resources/namespace.yaml",
		"resources/resource-quota.yaml",
		"resources/limit-range.yaml",
	}
	resourcePaths = append(resourcePaths, sharedPaths...)
	if err := writeYAML(filepath.Join(environmentRoot, "kustomization.yaml"), kustomization{
		APIVersion: "kustomize.config.k8s.io/v1beta1",
		Kind:       "Kustomization",
		Resources:  resourcePaths,
	}); err != nil {
		return err
	}
	return nil
}

func renderSharedResources(
	root,
	namespace string,
	labels map[string]string,
	topology deploymentTopology,
	plan environmentPlan,
) ([]string, error) {
	name := labels["app.kubernetes.io/part-of"]
	quota := kubeObject{
		APIVersion: "v1",
		Kind:       "ResourceQuota",
		Metadata:   objectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: map[string]any{"hard": map[string]string{
			"configmaps":             "60",
			"limits.cpu":             "16",
			"limits.memory":          "32Gi",
			"persistentvolumeclaims": "20",
			"pods":                   "60",
			"requests.cpu":           "8",
			"requests.memory":        "16Gi",
			"requests.storage":       "100Gi",
			"secrets":                "60",
			"services":               "30",
		}},
	}
	if err := writeYAML(filepath.Join(root, "resource-quota.yaml"), quota); err != nil {
		return nil, err
	}
	limit := kubeObject{
		APIVersion: "v1",
		Kind:       "LimitRange",
		Metadata:   objectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: map[string]any{"limits": []any{
			map[string]any{
				"type":           "Container",
				"default":        map[string]string{"cpu": "500m", "memory": "512Mi"},
				"defaultRequest": map[string]string{"cpu": "100m", "memory": "128Mi"},
				"max":            map[string]string{"cpu": "4", "memory": "8Gi"},
				"min":            map[string]string{"cpu": "10m", "memory": "16Mi"},
			},
			map[string]any{
				"type": "PersistentVolumeClaim",
				"max":  map[string]string{"storage": "50Gi"},
				"min":  map[string]string{"storage": "1Gi"},
			},
		}},
	}
	if err := writeYAML(filepath.Join(root, "limit-range.yaml"), limit); err != nil {
		return nil, err
	}
	networkPolicies, err := topologyNetworkPolicies(topology, plan, namespace, labels)
	if err != nil {
		return nil, err
	}
	if err := writeYAMLDocuments(filepath.Join(root, "network-policy.yaml"), networkPolicies); err != nil {
		return nil, err
	}
	istio, gateway, err := topologyIstioResources(topology, plan, namespace, labels)
	if err != nil {
		return nil, err
	}
	if err := writeYAMLDocuments(filepath.Join(root, "istio-mtls.yaml"), istio); err != nil {
		return nil, err
	}
	paths := []string{"resources/network-policy.yaml", "resources/istio-mtls.yaml"}
	if len(gateway) > 0 {
		if err := writeYAMLDocuments(filepath.Join(root, "istio-gateway.yaml"), gateway); err != nil {
			return nil, err
		}
		paths = append(paths, "resources/istio-gateway.yaml")
	}
	handoffPaths, err := renderManagedServiceHandoffs(root, namespace, labels, topology, plan)
	if err != nil {
		return nil, err
	}
	paths = append(paths, handoffPaths...)
	return paths, nil
}

func topologyIstioResources(
	topology deploymentTopology,
	plan environmentPlan,
	namespace string,
	labels map[string]string,
) ([]kubeObject, []kubeObject, error) {
	name := labels["app.kubernetes.io/part-of"]
	istio := []kubeObject{
		{
			APIVersion: "security.istio.io/v1",
			Kind:       "PeerAuthentication",
			Metadata:   objectMeta{Name: name + "-mtls", Namespace: namespace, Labels: labels},
			Spec:       map[string]any{"mtls": map[string]string{"mode": "STRICT"}},
		},
		{
			APIVersion: "security.istio.io/v1",
			Kind:       "AuthorizationPolicy",
			Metadata:   objectMeta{Name: "default-deny", Namespace: namespace, Labels: labels},
			Spec:       map[string]any{},
		},
	}
	istio = append(istio, topologyInternalAuthorizationPolicies(topology, plan, namespace, labels)...)
	if len(plan.ingress) == 0 {
		// No public ingress: emit the mTLS baseline only, no Gateway.
		return istio, nil, nil
	}
	ingressPorts := make(map[string]map[uint32]struct{})
	for _, route := range plan.ingress {
		if ingressPorts[route.service] == nil {
			ingressPorts[route.service] = make(map[uint32]struct{})
		}
		ingressPorts[route.service][route.port] = struct{}{}
	}
	ingressServices := make([]string, 0, len(ingressPorts))
	for service := range ingressPorts {
		ingressServices = append(ingressServices, service)
	}
	slices.Sort(ingressServices)
	for _, service := range ingressServices {
		ports := make([]uint32, 0, len(ingressPorts[service]))
		for port := range ingressPorts[service] {
			ports = append(ports, port)
		}
		slices.Sort(ports)
		renderedPorts := make([]string, 0, len(ports))
		for _, port := range ports {
			renderedPorts = append(renderedPorts, strconv.FormatUint(uint64(port), 10))
		}
		istio = append(istio, kubeObject{
			APIVersion: "security.istio.io/v1",
			Kind:       "AuthorizationPolicy",
			Metadata: objectMeta{
				Name:      "allow-istio-ingress-to-" + service,
				Namespace: namespace,
				Labels:    labels,
			},
			Spec: map[string]any{
				"selector": map[string]any{"matchLabels": map[string]string{"app": service}},
				"rules": []any{map[string]any{
					"from": []any{map[string]any{"source": map[string]any{
						"principals": []string{"cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account"},
					}}},
					"to": []any{map[string]any{"operation": map[string]any{
						"ports": renderedPorts,
					}}},
				}},
			},
		})
	}

	gatewayHosts := []string{"*"}
	var httpRoutes []any
	if len(plan.ingress[0].hosts) == 0 {
		route := plan.ingress[0]
		httpRoutes = []any{map[string]any{
			"name":  route.name,
			"match": []any{map[string]any{"uri": map[string]string{"prefix": "/"}}},
			"route": []any{map[string]any{"destination": map[string]any{
				"host": route.service + "." + namespace + ".svc.cluster.local",
				"port": map[string]any{"number": route.port},
			}}},
			"timeout": "30s",
		}}
	} else {
		gatewayHosts = nil
		hostSet := make(map[string]struct{})
		for _, route := range plan.ingress {
			matches := make([]any, 0, len(route.hosts))
			for _, host := range route.hosts {
				if _, exists := hostSet[host]; !exists {
					hostSet[host] = struct{}{}
					gatewayHosts = append(gatewayHosts, host)
				}
				matches = append(matches, map[string]any{
					"authority": map[string]string{
						"regex": "^" + regexp.QuoteMeta(host) + "(:[0-9]+)?$",
					},
				})
			}
			httpRoutes = append(httpRoutes, map[string]any{
				"name":  route.name,
				"match": matches,
				"route": []any{map[string]any{"destination": map[string]any{
					"host": route.service + "." + namespace + ".svc.cluster.local",
					"port": map[string]any{"number": route.port},
				}}},
				"timeout": "30s",
			})
		}
		slices.Sort(gatewayHosts)
	}
	gatewayName := topology.Module.Name
	gateway := []kubeObject{
		{
			APIVersion: "networking.istio.io/v1",
			Kind:       "Gateway",
			Metadata:   objectMeta{Name: gatewayName, Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"selector": map[string]string{"istio": "ingressgateway"},
				"servers": []any{map[string]any{
					"port":  map[string]any{"number": 80, "name": "http", "protocol": "HTTP"},
					"hosts": gatewayHosts,
				}},
			},
		},
		{
			APIVersion: "networking.istio.io/v1",
			Kind:       "VirtualService",
			Metadata:   objectMeta{Name: name, Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"hosts":    gatewayHosts,
				"gateways": []string{gatewayName},
				"http":     httpRoutes,
			},
		},
	}
	for _, service := range plan.services {
		gateway = append(gateway, kubeObject{
			APIVersion: "networking.istio.io/v1",
			Kind:       "DestinationRule",
			Metadata: objectMeta{
				Name:      kubernetesName(name, service, "defaults"),
				Namespace: namespace,
				Labels:    labels,
			},
			Spec: map[string]any{
				"host":     service + "." + namespace + ".svc.cluster.local",
				"exportTo": []string{"."},
				"trafficPolicy": map[string]any{
					"connectionPool": map[string]any{
						"tcp":  map[string]any{"maxConnections": 100},
						"http": map[string]any{"http2MaxRequests": 1000, "maxRequestsPerConnection": 10, "maxRetries": 3},
					},
					"outlierDetection": map[string]any{
						"consecutive5xxErrors": 5,
						"interval":             "10s",
						"baseEjectionTime":     "30s",
						"maxEjectionPercent":   50,
					},
				},
			},
		})
	}
	return istio, gateway, nil
}

func topologyInternalAuthorizationPolicies(
	topology deploymentTopology,
	plan environmentPlan,
	namespace string,
	labels map[string]string,
) []kubeObject {
	inCluster := make(map[string]struct{}, len(plan.services))
	for _, service := range plan.services {
		inCluster[service] = struct{}{}
	}
	targetPorts := make(map[string]map[uint32]struct{})
	addPort := func(service string, port uint32) {
		if targetPorts[service] == nil {
			targetPorts[service] = make(map[uint32]struct{})
		}
		targetPorts[service][port] = struct{}{}
	}
	for _, caller := range topology.Services {
		if _, exists := inCluster[caller.Name]; !exists {
			continue
		}
		for _, dependency := range caller.Dependencies {
			if _, exists := inCluster[dependency.Service]; !exists {
				continue
			}
			target, _ := topologyServiceByName(topology, dependency.Service)
			for _, endpointName := range dependency.Endpoints {
				endpoint, _ := topologyEndpointByName(target, endpointName)
				addPort(target.Name, endpoint.Port)
			}
		}
	}
	for _, service := range topology.Services {
		if _, exists := inCluster[service.Name]; !exists {
			continue
		}
		for _, endpointName := range service.BootstrapJobEndpoints {
			endpoint, _ := topologyEndpointByName(service, endpointName)
			addPort(service.Name, endpoint.Port)
		}
	}

	targets := make([]string, 0, len(targetPorts))
	for target := range targetPorts {
		targets = append(targets, target)
	}
	slices.Sort(targets)
	policies := make([]kubeObject, 0, len(targets))
	principal := "cluster.local/ns/" + namespace + "/sa/default"
	for _, target := range targets {
		ports := make([]uint32, 0, len(targetPorts[target]))
		for port := range targetPorts[target] {
			ports = append(ports, port)
		}
		slices.Sort(ports)
		renderedPorts := make([]string, 0, len(ports))
		for _, port := range ports {
			renderedPorts = append(renderedPorts, strconv.FormatUint(uint64(port), 10))
		}
		policies = append(policies, kubeObject{
			APIVersion: "security.istio.io/v1",
			Kind:       "AuthorizationPolicy",
			Metadata: objectMeta{
				Name:      "allow-" + target + "-from-internal-topology",
				Namespace: namespace,
				Labels:    labels,
			},
			Spec: map[string]any{
				"selector": map[string]any{"matchLabels": map[string]string{"app": target}},
				"rules": []any{map[string]any{
					"from": []any{map[string]any{"source": map[string]any{
						"principals": []string{principal},
					}}},
					"to": []any{map[string]any{"operation": map[string]any{
						"ports": renderedPorts,
					}}},
				}},
			},
		})
	}
	return policies
}

func loadDeploymentTopology(moduleDir, moduleName string, services []serviceDefinition) (deploymentTopology, error) {
	file := filepath.Join(moduleDir, "deployment", "topology.bindings.codefly.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		return deploymentTopology{}, fmt.Errorf("read deployment topology: %w", err)
	}
	var topology deploymentTopology
	if err := yaml.Unmarshal(data, &topology); err != nil {
		return deploymentTopology{}, fmt.Errorf("parse deployment topology: %w", err)
	}
	if topology.Version != "v1" {
		return deploymentTopology{}, fmt.Errorf("deployment topology version %q is not supported", topology.Version)
	}
	if topology.Module.Name != moduleName || topology.Module.Namespace != moduleName {
		return deploymentTopology{}, fmt.Errorf(
			"deployment topology identity %q/%q does not match module %q",
			topology.Module.Name,
			topology.Module.Namespace,
			moduleName,
		)
	}
	declared := make(map[string]struct{}, len(services))
	for _, service := range services {
		declared[service.name] = struct{}{}
	}
	topologyServices := make(map[string]topologyService, len(topology.Services))
	for _, service := range topology.Services {
		if _, exists := topologyServices[service.Name]; exists {
			return deploymentTopology{}, fmt.Errorf("deployment topology repeats service %q", service.Name)
		}
		if _, exists := declared[service.Name]; !exists {
			return deploymentTopology{}, fmt.Errorf("deployment topology contains undeclared service %q", service.Name)
		}
		if len(service.Endpoints) == 0 {
			return deploymentTopology{}, fmt.Errorf("deployment topology service %q declares no endpoints", service.Name)
		}
		endpoints := make(map[string]struct{}, len(service.Endpoints))
		for _, endpoint := range service.Endpoints {
			if endpoint.Port == 0 || endpoint.Port > 65535 {
				return deploymentTopology{}, fmt.Errorf("deployment topology endpoint %s/%s has invalid port", service.Name, endpoint.Name)
			}
			if _, exists := endpoints[endpoint.Name]; exists {
				return deploymentTopology{}, fmt.Errorf("deployment topology repeats endpoint %s/%s", service.Name, endpoint.Name)
			}
			endpoints[endpoint.Name] = struct{}{}
		}
		for _, endpoint := range service.BootstrapJobEndpoints {
			if _, exists := endpoints[endpoint]; !exists {
				return deploymentTopology{}, fmt.Errorf("deployment topology service %q bootstrap Job references missing endpoint %q", service.Name, endpoint)
			}
		}
		topologyServices[service.Name] = service
	}
	for service := range declared {
		if _, exists := topologyServices[service]; !exists {
			return deploymentTopology{}, fmt.Errorf("deployment topology is missing declared service %q", service)
		}
	}
	if _, exists := topologyServices[topology.Module.ServiceEntry]; !exists {
		return deploymentTopology{}, fmt.Errorf("deployment topology service entry %q is not declared", topology.Module.ServiceEntry)
	}
	for _, service := range topology.Services {
		for _, dependency := range service.Dependencies {
			target, exists := topologyServices[dependency.Service]
			if !exists {
				return deploymentTopology{}, fmt.Errorf("deployment topology service %q references undeclared dependency %q", service.Name, dependency.Service)
			}
			if len(dependency.Endpoints) == 0 {
				return deploymentTopology{}, fmt.Errorf(
					"deployment topology service %q dependency %q declares no endpoints",
					service.Name,
					dependency.Service,
				)
			}
			for _, endpoint := range dependency.Endpoints {
				if _, exists := topologyEndpointByName(target, endpoint); !exists {
					return deploymentTopology{}, fmt.Errorf("deployment topology service %q references missing endpoint %s/%s", service.Name, dependency.Service, endpoint)
				}
			}
		}
	}
	return topology, nil
}

func topologyServiceByName(topology deploymentTopology, name string) (topologyService, bool) {
	for _, service := range topology.Services {
		if service.Name == name {
			return service, true
		}
	}
	return topologyService{}, false
}

func topologyEndpointByName(service topologyService, name string) (topologyEndpoint, bool) {
	for _, endpoint := range service.Endpoints {
		if endpoint.Name == name {
			return endpoint, true
		}
	}
	return topologyEndpoint{}, false
}

func topologyNetworkPolicies(
	topology deploymentTopology,
	plan environmentPlan,
	namespace string,
	labels map[string]string,
) ([]kubeObject, error) {
	policies := []kubeObject{
		{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
			Metadata:   objectMeta{Name: "default-deny-all", Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"podSelector": map[string]any{},
				"policyTypes": []string{"Ingress", "Egress"},
			},
		},
		{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
			Metadata:   objectMeta{Name: "allow-dns-egress", Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"podSelector": map[string]any{},
				"policyTypes": []string{"Egress"},
				"egress": []any{map[string]any{
					"to": []any{map[string]any{
						"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
						"podSelector":       map[string]any{"matchLabels": map[string]string{"k8s-app": "kube-dns"}},
					}},
					"ports": []any{
						map[string]any{"protocol": "UDP", "port": 53},
						map[string]any{"protocol": "TCP", "port": 53},
					},
				}},
			},
		},
		{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
			Metadata:   objectMeta{Name: "allow-istio-control-plane-egress", Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"podSelector": map[string]any{},
				"policyTypes": []string{"Egress"},
				"egress": []any{map[string]any{
					"to": []any{map[string]any{
						"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "istio-system"}},
						"podSelector":       map[string]any{"matchLabels": map[string]string{"app": "istiod"}},
					}},
					"ports": []any{
						map[string]any{"protocol": "TCP", "port": 15012},
						map[string]any{"protocol": "TCP", "port": 15014},
					},
				}},
			},
		},
		{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
			Metadata:   objectMeta{Name: "allow-ambient-hbone-transport", Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"podSelector": map[string]any{},
				"policyTypes": []string{"Ingress", "Egress"},
				"ingress": []any{map[string]any{
					"ports": []any{
						map[string]any{"protocol": "TCP", "port": 15008},
					},
				}},
				"egress": []any{map[string]any{
					"ports": []any{
						map[string]any{"protocol": "TCP", "port": 15008},
					},
				}},
			},
		},
	}
	inCluster := make(map[string]struct{}, len(plan.services))
	for _, service := range plan.services {
		inCluster[service] = struct{}{}
	}
	type edge struct {
		caller string
		target string
		ports  []uint32
	}
	var edges []edge
	for _, caller := range topology.Services {
		if _, exists := inCluster[caller.Name]; !exists {
			continue
		}
		for _, dependency := range caller.Dependencies {
			target, _ := topologyServiceByName(topology, dependency.Service)
			portSet := make(map[uint32]struct{})
			for _, endpointName := range dependency.Endpoints {
				endpoint, _ := topologyEndpointByName(target, endpointName)
				portSet[endpoint.Port] = struct{}{}
			}
			ports := make([]uint32, 0, len(portSet))
			for port := range portSet {
				ports = append(ports, port)
			}
			slices.Sort(ports)
			edges = append(edges, edge{caller: caller.Name, target: target.Name, ports: ports})
		}
	}
	for _, current := range edges {
		if _, exists := inCluster[current.target]; exists {
			policies = append(policies,
				dependencyIngressPolicy(namespace, labels, current.target, current.caller, current.ports),
				dependencyEgressPolicy(namespace, labels, current.caller, current.target, current.ports),
			)
			continue
		}
		config, managed := plan.managed[current.target]
		if !managed {
			return nil, fmt.Errorf("dependency %s -> %s has no in-cluster service or managed handoff", current.caller, current.target)
		}
		policies = append(policies, managedEgressPolicy(namespace, labels, current.caller, current.target, current.ports, config.EgressCIDRs))
	}
	for _, service := range topology.Services {
		if len(service.BootstrapJobEndpoints) == 0 {
			continue
		}
		if _, exists := inCluster[service.Name]; !exists {
			continue
		}
		portSet := make(map[uint32]struct{}, len(service.BootstrapJobEndpoints))
		for _, endpointName := range service.BootstrapJobEndpoints {
			endpoint, _ := topologyEndpointByName(service, endpointName)
			portSet[endpoint.Port] = struct{}{}
		}
		ports := make([]uint32, 0, len(portSet))
		for port := range portSet {
			ports = append(ports, port)
		}
		slices.Sort(ports)
		policies = append(policies,
			bootstrapJobIngressPolicy(namespace, labels, service.Name, ports),
			bootstrapJobEgressPolicy(namespace, labels, service.Name, ports),
		)
	}
	ingressPorts := make(map[string]map[uint32]struct{})
	for _, route := range plan.ingress {
		if ingressPorts[route.service] == nil {
			ingressPorts[route.service] = make(map[uint32]struct{})
		}
		ingressPorts[route.service][route.port] = struct{}{}
	}
	ingressServices := make([]string, 0, len(ingressPorts))
	for service := range ingressPorts {
		ingressServices = append(ingressServices, service)
	}
	slices.Sort(ingressServices)
	for _, service := range ingressServices {
		ports := make([]uint32, 0, len(ingressPorts[service]))
		for port := range ingressPorts[service] {
			ports = append(ports, port)
		}
		slices.Sort(ports)
		policies = append(policies, kubeObject{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
			Metadata:   objectMeta{Name: "allow-istio-ingress-to-" + service, Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"podSelector": map[string]any{"matchLabels": map[string]string{"app": service}},
				"policyTypes": []string{"Ingress"},
				"ingress": []any{map[string]any{
					"from": []any{map[string]any{
						"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "istio-system"}},
						"podSelector":       map[string]any{"matchLabels": map[string]string{"istio": "ingressgateway"}},
					}},
					"ports": networkPorts(ports),
				}},
			},
		})
	}
	for _, service := range topology.Services {
		if len(service.PublicEgressPorts) == 0 || !slices.Contains(plan.services, service.Name) {
			continue
		}
		policies = append(policies, publicEgressPolicy(namespace, labels, service.Name, service.PublicEgressPorts))
	}
	return policies, nil
}

func dependencyIngressPolicy(namespace string, labels map[string]string, target, caller string, ports []uint32) kubeObject {
	return kubeObject{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata:   objectMeta{Name: kubernetesName("allow", target, "from", caller), Namespace: namespace, Labels: labels},
		Spec: map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{"app": target}},
			"policyTypes": []string{"Ingress"},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app": caller}}}},
				"ports": networkPorts(ports),
			}},
		},
	}
}

func dependencyEgressPolicy(namespace string, labels map[string]string, caller, target string, ports []uint32) kubeObject {
	return kubeObject{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata:   objectMeta{Name: kubernetesName("allow", caller, "to", target), Namespace: namespace, Labels: labels},
		Spec: map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{"app": caller}},
			"policyTypes": []string{"Egress"},
			"egress": []any{map[string]any{
				"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app": target}}}},
				"ports": networkPorts(ports),
			}},
		},
	}
}

func bootstrapJobIngressPolicy(namespace string, labels map[string]string, service string, ports []uint32) kubeObject {
	return kubeObject{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata:   objectMeta{Name: kubernetesName("allow", service, "from", "bootstrap"), Namespace: namespace, Labels: labels},
		Spec: map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{"app": service}},
			"policyTypes": []string{"Ingress"},
			"ingress": []any{map[string]any{
				"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{
					"codefly.dev/bootstrap-service": service,
					"job-name":                      service,
				}}}},
				"ports": networkPorts(ports),
			}},
		},
	}
}

func bootstrapJobEgressPolicy(namespace string, labels map[string]string, service string, ports []uint32) kubeObject {
	return kubeObject{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata:   objectMeta{Name: kubernetesName("allow", service, "bootstrap", "to", service), Namespace: namespace, Labels: labels},
		Spec: map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{
				"codefly.dev/bootstrap-service": service,
				"job-name":                      service,
			}},
			"policyTypes": []string{"Egress"},
			"egress": []any{map[string]any{
				"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app": service}}}},
				"ports": networkPorts(ports),
			}},
		},
	}
}

func managedEgressPolicy(
	namespace string,
	labels map[string]string,
	caller,
	target string,
	ports []uint32,
	cidrs []string,
) kubeObject {
	destinations := make([]any, 0, len(cidrs))
	for _, cidr := range cidrs {
		destinations = append(destinations, map[string]any{"ipBlock": map[string]string{"cidr": cidr}})
	}
	return kubeObject{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata:   objectMeta{Name: kubernetesName("allow", caller, "to", target), Namespace: namespace, Labels: labels},
		Spec: map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{"app": caller}},
			"policyTypes": []string{"Egress"},
			"egress":      []any{map[string]any{"to": destinations, "ports": networkPorts(ports)}},
		},
	}
}

func networkPorts(ports []uint32) []any {
	result := make([]any, 0, len(ports))
	for _, port := range ports {
		result = append(result, map[string]any{"protocol": "TCP", "port": port})
	}
	return result
}

var publicIPv4Exceptions = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
}

var publicIPv6Exceptions = []string{
	"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32",
	"2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
}

func publicEgressPolicy(namespace string, labels map[string]string, service string, ports []uint32) kubeObject {
	return kubeObject{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata:   objectMeta{Name: "allow-" + service + "-public-egress", Namespace: namespace, Labels: labels},
		Spec: map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{"app": service}},
			"policyTypes": []string{"Egress"},
			"egress": []any{map[string]any{
				"to": []any{
					map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0", "except": publicIPv4Exceptions}},
					map[string]any{"ipBlock": map[string]any{"cidr": "::/0", "except": publicIPv6Exceptions}},
				},
				"ports": networkPorts(ports),
			}},
		},
	}
}

func renderManagedServiceHandoffs(
	root,
	namespace string,
	labels map[string]string,
	topology deploymentTopology,
	plan environmentPlan,
) ([]string, error) {
	var paths []string
	for _, handoff := range plan.handoffs {
		config := plan.managed[handoff.Service]
		service, _ := topologyServiceByName(topology, handoff.Service)
		ports := make([]any, 0, len(service.Endpoints))
		for _, endpoint := range service.Endpoints {
			ports = append(ports, map[string]any{
				"name":       endpoint.Name,
				"port":       endpoint.Port,
				"targetPort": endpoint.Port,
			})
		}
		objects := []kubeObject{{
			APIVersion: "v1",
			Kind:       "Service",
			Metadata:   objectMeta{Name: handoff.Service, Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"type":         "ExternalName",
				"externalName": config.ExternalName,
				"ports":        ports,
			},
		}}
		for _, reference := range config.SecretReferences {
			objects = append(objects, kubeObject{
				APIVersion: "external-secrets.io/v1",
				Kind:       "ExternalSecret",
				Metadata:   objectMeta{Name: reference.Name, Namespace: namespace, Labels: labels},
				Spec: map[string]any{
					"refreshInterval": "1h",
					"secretStoreRef": map[string]string{
						"name": reference.SecretStore.Name,
						"kind": reference.SecretStore.Kind,
					},
					"target":   map[string]string{"name": reference.Name, "creationPolicy": "Owner"},
					"dataFrom": []any{map[string]any{"extract": map[string]string{"key": reference.RemoteKey}}},
				},
			})
		}
		relative := path.Join("resources", "handoffs", handoff.Service+".yaml")
		if err := writeYAMLDocuments(filepath.Join(filepath.Dir(root), filepath.FromSlash(relative)), objects); err != nil {
			return nil, err
		}
		paths = append(paths, relative)
	}
	return paths, nil
}

func validateGeneratedBundle(root string, bundle moduleBundle) error {
	for _, environment := range bundle.Environments {
		rendered, err := renderKustomization(filepath.Join(root, "overlays", environment.Name))
		if err != nil {
			return fmt.Errorf("render environment %q: %w", environment.Name, err)
		}
		if err := validateRenderedObjects(rendered, environment); err != nil {
			return fmt.Errorf("validate environment %q: %w", environment.Name, err)
		}
	}
	return filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if unresolvedPattern.Match(data) {
			return fmt.Errorf("%s contains an unresolved placeholder or starter identity", file)
		}
		return nil
	})
}

func renderKustomization(root string) ([]map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(root, "kustomization.yaml"))
	if err != nil {
		return nil, err
	}
	var configuration kustomization
	if err := yaml.Unmarshal(data, &configuration); err != nil {
		return nil, err
	}
	if configuration.APIVersion != "kustomize.config.k8s.io/v1beta1" || configuration.Kind != "Kustomization" {
		return nil, fmt.Errorf("invalid kustomization type")
	}
	var objects []map[string]any
	for _, relative := range configuration.Resources {
		clean, err := cleanRelativePath(relative)
		if err != nil {
			return nil, err
		}
		file := filepath.Join(root, filepath.FromSlash(clean))
		documents, err := decodeYAMLDocuments(file)
		if err != nil {
			return nil, err
		}
		objects = append(objects, documents...)
	}
	return objects, nil
}

// moduleOwnedAPIVersions is the closed set of apiVersions the module plugin is
// allowed to emit. Any object outside it — a repository-transport resource such
// as an Argo or Flux kind, for example — fails generation. Expressing the
// boundary as a positive allowlist keeps repository-transport identifiers out
// of runtime code entirely (see TestRuntimePluginOwnsNoGitOrArgoTransport).
var moduleOwnedAPIVersions = map[string]struct{}{
	"v1":                     {},
	"networking.k8s.io/v1":   {},
	"security.istio.io/v1":   {},
	"networking.istio.io/v1": {},
	"external-secrets.io/v1": {},
}

// validateRenderedObjects proves the generated overlay is module-owned only: no
// Secret material and no object outside the module-owned apiVersion allowlist.
func validateRenderedObjects(objects []map[string]any, environment bundleEnvironment) error {
	namespaceCount := 0
	for _, object := range objects {
		kind, _ := object["kind"].(string)
		apiVersion, _ := object["apiVersion"].(string)
		metadata, _ := object["metadata"].(map[string]any)
		if kind == "Secret" {
			return fmt.Errorf("bundle output contains a Kubernetes Secret")
		}
		if _, owned := moduleOwnedAPIVersions[apiVersion]; !owned {
			return fmt.Errorf("bundle output contains non-module-owned resource %s/%s", apiVersion, kind)
		}
		if kind == "Namespace" {
			namespaceCount++
			if metadata["name"] != environment.Namespace {
				return fmt.Errorf("namespace resource targets %v", metadata["name"])
			}
		}
	}
	if namespaceCount != 1 {
		return fmt.Errorf("render must contain exactly one Namespace")
	}
	return nil
}

func decodeYAMLDocuments(file string) ([]map[string]any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return decodeYAMLBytes(data, file)
}

func decodeYAMLBytes(data []byte, source string) ([]map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []map[string]any
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", source, err)
		}
		if len(document) == 0 {
			continue
		}
		if document["apiVersion"] == nil || document["kind"] == nil || document["metadata"] == nil {
			return nil, fmt.Errorf("%s contains an incomplete Kubernetes object", source)
		}
		documents = append(documents, document)
	}
	return documents, nil
}

func cleanRelativeFilesystemPath(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\\") || !filepath.IsLocal(value) {
		return "", fmt.Errorf("%q is not a local relative path", value)
	}
	clean := filepath.Clean(value)
	if clean != value {
		return "", fmt.Errorf("%q is not a canonical local relative path", value)
	}
	return clean, nil
}

func writeYAML(file string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return writeFile(file, data)
}

func writeYAMLDocuments(file string, values []kubeObject) error {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return writeFile(file, output.Bytes())
}

func writeJSON(file string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFile(file, data)
}

func writeFile(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

func cleanRelativePath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || path.IsAbs(value) {
		return "", fmt.Errorf("%q is not a relative repository path", value)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", fmt.Errorf("%q is not a canonical relative repository path", value)
	}
	return clean, nil
}

func validateDNSLabel(field, value string) error {
	if len(value) == 0 || len(value) > 63 || !dnsLabelPattern.MatchString(value) {
		return fmt.Errorf("%s %q must be a Kubernetes DNS label", field, value)
	}
	return nil
}

func kubernetesName(parts ...string) string {
	joined := strings.Join(parts, "-")
	if len(joined) <= 63 {
		return joined
	}
	digest := sha256.Sum256([]byte(joined))
	suffix := hex.EncodeToString(digest[:])[:10]
	return strings.TrimRight(joined[:52], "-") + "-" + suffix
}

func mergeLabels(sets ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, set := range sets {
		for key, value := range set {
			result[key] = value
		}
	}
	return result
}
