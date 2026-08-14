package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"accounts/pkg/business"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

const deploymentTopologySchemaVersion = "saas.deployment.topology.v1"

var configurationKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

type deploymentBindings struct {
	Version   string                       `yaml:"version"`
	Module    deploymentModuleBinding      `yaml:"module"`
	Interface []deploymentInterfaceBinding `yaml:"interface"`
	Services  []deploymentServiceBinding   `yaml:"services"`
}

type deploymentModuleBinding struct {
	Name         string                  `yaml:"name"`
	Namespace    string                  `yaml:"namespace"`
	ServiceEntry string                  `yaml:"service_entry"`
	Description  string                  `yaml:"description"`
	Agent        *deploymentAgentBinding `yaml:"agent,omitempty"`
}

type deploymentInterfaceBinding struct {
	Service    string `yaml:"service"`
	Endpoint   string `yaml:"endpoint"`
	Visibility string `yaml:"visibility"`
}

type deploymentServiceBinding struct {
	Name                               string                        `yaml:"name"`
	Version                            string                        `yaml:"version"`
	Description                        string                        `yaml:"description,omitempty"`
	Agent                              deploymentAgentBinding        `yaml:"agent"`
	Kubernetes                         *kubernetesIdentityBinding    `yaml:"kubernetes,omitempty"`
	WorkspaceConfigurationDependencies []string                      `yaml:"workspace_configuration_dependencies,omitempty"`
	SecretServiceConfigurations        []secretServiceConfiguration  `yaml:"secret_service_configurations,omitempty"`
	Endpoints                          []deploymentEndpointBinding   `yaml:"endpoints"`
	BootstrapJobEndpoints              []string                      `yaml:"bootstrap_job_endpoints,omitempty"`
	Dependencies                       []deploymentDependencyBinding `yaml:"dependencies,omitempty"`
	PublicEgressPorts                  []uint32                      `yaml:"public_egress_ports,omitempty"`
	Spec                               map[string]any                `yaml:"spec,omitempty"`
}

type kubernetesIdentityBinding struct {
	ServiceName string `yaml:"service_name"`
	AppLabel    string `yaml:"app_label"`
}

type deploymentAgentBinding struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Version   string `yaml:"version"`
	Publisher string `yaml:"publisher"`
}

type secretServiceConfiguration struct {
	Name    string                            `yaml:"name"`
	Entries []secretServiceConfigurationEntry `yaml:"entries"`
}

type secretServiceConfigurationEntry struct {
	Key string `yaml:"key"`
}

type deploymentEndpointBinding struct {
	Name       string `yaml:"name"`
	API        string `yaml:"api"`
	Visibility string `yaml:"visibility"`
	Port       uint32 `yaml:"port"`
}

type deploymentDependencyBinding struct {
	Service   string   `yaml:"service"`
	Endpoints []string `yaml:"endpoints"`
}

// DeploymentArtifacts contains every checked-in projection of the topology
// binding. The actual Codefly manifests are outputs, not parallel sources.
type DeploymentArtifacts struct {
	Catalog          *catalogv1.DeploymentCatalog
	CatalogJSON      []byte
	ModuleManifest   []byte
	ServiceManifests map[string][]byte
	NetworkPolicy    []byte
}

// BuildDeploymentArtifacts validates the descriptor-owned accounts API and a
// strict module topology before rendering Codefly and Kubernetes consumers.
func BuildDeploymentArtifacts(serviceDocument, bindingDocument []byte) (*DeploymentArtifacts, error) {
	serviceCatalog := &catalogv1.ServiceCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, serviceCatalog); err != nil {
		return nil, fmt.Errorf("decode service catalog: %w", err)
	}
	if err := business.ValidateServiceCatalog(serviceCatalog); err != nil {
		return nil, fmt.Errorf("validate service catalog: %w", err)
	}

	bindings, err := decodeDeploymentBindings(bindingDocument)
	if err != nil {
		return nil, err
	}
	if err := validateDeploymentBindings(serviceCatalog, bindings); err != nil {
		return nil, err
	}

	catalog, err := buildDeploymentCatalog(bindings)
	if err != nil {
		return nil, err
	}
	catalogJSON, err := renderDeploymentCatalogJSON(catalog)
	if err != nil {
		return nil, err
	}
	moduleManifest, err := renderModuleManifest(bindings)
	if err != nil {
		return nil, err
	}
	serviceManifests := make(map[string][]byte, len(bindings.Services))
	for _, service := range bindings.Services {
		manifest, err := renderServiceManifest(service)
		if err != nil {
			return nil, err
		}
		serviceManifests[service.Name] = manifest
	}

	return &DeploymentArtifacts{
		Catalog:          catalog,
		CatalogJSON:      catalogJSON,
		ModuleManifest:   moduleManifest,
		ServiceManifests: serviceManifests,
		NetworkPolicy:    renderNetworkPolicy(bindings),
	}, nil
}

func decodeDeploymentBindings(document []byte) (deploymentBindings, error) {
	var bindings deploymentBindings
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	if err := decoder.Decode(&bindings); err != nil {
		return deploymentBindings{}, fmt.Errorf("decode deployment topology bindings: %w", err)
	}
	if bindings.Version != "v1" {
		return deploymentBindings{}, fmt.Errorf("unsupported deployment topology bindings version %q", bindings.Version)
	}
	return bindings, nil
}

func validateDeploymentBindings(serviceCatalog *catalogv1.ServiceCatalog, bindings deploymentBindings) error {
	if !endpointNamePattern.MatchString(bindings.Module.Name) || !endpointNamePattern.MatchString(bindings.Module.Namespace) || bindings.Module.Description == "" {
		return fmt.Errorf("deployment module identity is incomplete or invalid")
	}
	if serviceCatalog.GetOwner().GetModule() != bindings.Module.Name {
		return fmt.Errorf("service catalog module %q does not match deployment module %q", serviceCatalog.GetOwner().GetModule(), bindings.Module.Name)
	}
	if bindings.Module.Agent != nil {
		moduleAgentFields := []string{
			bindings.Module.Agent.Kind,
			bindings.Module.Agent.Name,
			bindings.Module.Agent.Version,
			bindings.Module.Agent.Publisher,
		}
		var moduleAgentValues int
		for _, value := range moduleAgentFields {
			if value != "" {
				moduleAgentValues++
			}
		}
		if moduleAgentValues != len(moduleAgentFields) {
			return fmt.Errorf("deployment module agent identity is incomplete")
		}
	}
	if len(bindings.Services) == 0 {
		return fmt.Errorf("deployment topology contains no services")
	}

	services := make(map[string]deploymentServiceBinding, len(bindings.Services))
	kubernetesServices := make(map[string]string, len(bindings.Services))
	kubernetesApps := make(map[string]string, len(bindings.Services))
	previousService := ""
	for _, service := range bindings.Services {
		if !endpointNamePattern.MatchString(service.Name) || service.Version == "" || service.Agent.Kind == "" ||
			service.Agent.Name == "" || service.Agent.Version == "" || service.Agent.Publisher == "" || len(service.Endpoints) == 0 {
			return fmt.Errorf("service %q manifest identity is incomplete", service.Name)
		}
		if previousService != "" && service.Name <= previousService {
			return fmt.Errorf("deployment services are not strictly sorted at %q", service.Name)
		}
		previousService = service.Name
		services[service.Name] = service
		kubernetesService := kubernetesServiceName(service)
		kubernetesApp := kubernetesAppLabel(service)
		if len(kubernetesService) > 63 || !endpointNamePattern.MatchString(kubernetesService) ||
			len(kubernetesApp) > 63 || !endpointNamePattern.MatchString(kubernetesApp) {
			return fmt.Errorf("service %q Kubernetes identity is incomplete or invalid", service.Name)
		}
		if owner, exists := kubernetesServices[kubernetesService]; exists {
			return fmt.Errorf("services %q and %q share Kubernetes service name %q", owner, service.Name, kubernetesService)
		}
		kubernetesServices[kubernetesService] = service.Name
		if owner, exists := kubernetesApps[kubernetesApp]; exists {
			return fmt.Errorf("services %q and %q share Kubernetes app label %q", owner, service.Name, kubernetesApp)
		}
		kubernetesApps[kubernetesApp] = service.Name

		previousConfiguration := ""
		for _, configuration := range service.WorkspaceConfigurationDependencies {
			if !endpointNamePattern.MatchString(configuration) || (previousConfiguration != "" && configuration <= previousConfiguration) {
				return fmt.Errorf("service %q workspace configuration dependencies are invalid or unsorted", service.Name)
			}
			previousConfiguration = configuration
		}

		previousSecretConfiguration := ""
		for _, configuration := range service.SecretServiceConfigurations {
			if !endpointNamePattern.MatchString(configuration.Name) || len(configuration.Entries) == 0 ||
				(previousSecretConfiguration != "" && configuration.Name <= previousSecretConfiguration) {
				return fmt.Errorf("service %q secret service configurations are invalid or unsorted at %q", service.Name, configuration.Name)
			}
			previousSecretConfiguration = configuration.Name
			previousEntry := ""
			for _, entry := range configuration.Entries {
				if !configurationKeyPattern.MatchString(entry.Key) || (previousEntry != "" && entry.Key <= previousEntry) {
					return fmt.Errorf("service %q secret service configuration %q entries are invalid or unsorted", service.Name, configuration.Name)
				}
				previousEntry = entry.Key
			}
		}

		previousEndpoint := ""
		for _, endpoint := range service.Endpoints {
			if !endpointNamePattern.MatchString(endpoint.Name) || endpoint.Port == 0 || endpoint.Port > 65535 {
				return fmt.Errorf("service %q endpoint %q is incomplete", service.Name, endpoint.Name)
			}
			if previousEndpoint != "" && endpoint.Name <= previousEndpoint {
				return fmt.Errorf("service %q endpoints are not strictly sorted at %q", service.Name, endpoint.Name)
			}
			previousEndpoint = endpoint.Name
			if _, err := codeflyAPI(endpoint.API); err != nil {
				return fmt.Errorf("service %q endpoint %q: %w", service.Name, endpoint.Name, err)
			}
			if _, err := endpointVisibility(endpoint.Visibility); err != nil {
				return fmt.Errorf("service %q endpoint %q: %w", service.Name, endpoint.Name, err)
			}
		}
		knownEndpoints := endpointBindingsByName(service.Endpoints)
		previousBootstrapEndpoint := ""
		for _, endpoint := range service.BootstrapJobEndpoints {
			if !endpointNamePattern.MatchString(endpoint) || (previousBootstrapEndpoint != "" && endpoint <= previousBootstrapEndpoint) {
				return fmt.Errorf("service %q bootstrap Job endpoints are invalid or unsorted", service.Name)
			}
			if _, exists := knownEndpoints[endpoint]; !exists {
				return fmt.Errorf("service %q bootstrap Job references unknown endpoint %q", service.Name, endpoint)
			}
			previousBootstrapEndpoint = endpoint
		}

		previousDependency := ""
		for _, dependency := range service.Dependencies {
			if !endpointNamePattern.MatchString(dependency.Service) || dependency.Service == service.Name || len(dependency.Endpoints) == 0 ||
				(previousDependency != "" && dependency.Service <= previousDependency) {
				return fmt.Errorf("service %q dependencies are invalid or unsorted at %q", service.Name, dependency.Service)
			}
			previousDependency = dependency.Service
			previousReference := ""
			for _, endpoint := range dependency.Endpoints {
				if !endpointNamePattern.MatchString(endpoint) || (previousReference != "" && endpoint <= previousReference) {
					return fmt.Errorf("service %q dependency %q endpoints are invalid or unsorted", service.Name, dependency.Service)
				}
				previousReference = endpoint
			}
		}

		previousPort := uint32(0)
		for _, port := range service.PublicEgressPorts {
			if port == 0 || port > 65535 || (previousPort != 0 && port <= previousPort) {
				return fmt.Errorf("service %q public egress ports are invalid or unsorted", service.Name)
			}
			previousPort = port
		}
	}
	if !endpointNamePattern.MatchString(bindings.Module.ServiceEntry) {
		return fmt.Errorf("deployment module service entry is incomplete or invalid")
	}
	if _, exists := services[bindings.Module.ServiceEntry]; !exists {
		return fmt.Errorf("deployment module service entry references unknown service %q", bindings.Module.ServiceEntry)
	}

	for _, service := range bindings.Services {
		for _, dependency := range service.Dependencies {
			target, exists := services[dependency.Service]
			if !exists {
				return fmt.Errorf("service %q depends on unknown service %q", service.Name, dependency.Service)
			}
			knownEndpoints := endpointBindingsByName(target.Endpoints)
			for _, endpoint := range dependency.Endpoints {
				if _, exists := knownEndpoints[endpoint]; !exists {
					return fmt.Errorf("service %q depends on unknown endpoint %q of %q", service.Name, endpoint, dependency.Service)
				}
			}
		}
	}
	if err := rejectDependencyCycles(bindings.Services); err != nil {
		return err
	}

	previousInterface := ""
	for _, exposed := range bindings.Interface {
		key := exposed.Service + "/" + exposed.Endpoint
		if previousInterface != "" && key <= previousInterface {
			return fmt.Errorf("module interface endpoints are not strictly sorted at %q", key)
		}
		previousInterface = key
		service, exists := services[exposed.Service]
		if !exists {
			return fmt.Errorf("module interface references unknown service %q", exposed.Service)
		}
		endpoint, exists := endpointBindingsByName(service.Endpoints)[exposed.Endpoint]
		if !exists {
			return fmt.Errorf("module interface references unknown endpoint %q of %q", exposed.Endpoint, exposed.Service)
		}
		visibility, err := endpointVisibility(exposed.Visibility)
		if err != nil || (visibility != catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_MODULE && visibility != catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_PUBLIC) {
			return fmt.Errorf("module interface endpoint %q has invalid visibility %q", key, exposed.Visibility)
		}
		if endpoint.Visibility != exposed.Visibility {
			return fmt.Errorf("module interface endpoint %q visibility disagrees with service endpoint", key)
		}
	}

	owner, exists := services[serviceCatalog.GetOwner().GetService()]
	if !exists {
		return fmt.Errorf("deployment topology omits catalog owner service %q", serviceCatalog.GetOwner().GetService())
	}
	requiredAPIs := make(map[catalogv1.CodeflyAPI]bool)
	for _, method := range serviceCatalog.GetMethods() {
		for _, protocol := range method.GetProtocols() {
			switch protocol {
			case catalogv1.Protocol_PROTOCOL_GRPC:
				requiredAPIs[catalogv1.CodeflyAPI_CODEFLY_API_GRPC] = true
			case catalogv1.Protocol_PROTOCOL_CONNECT:
				requiredAPIs[catalogv1.CodeflyAPI_CODEFLY_API_CONNECT] = true
			case catalogv1.Protocol_PROTOCOL_REST:
				requiredAPIs[catalogv1.CodeflyAPI_CODEFLY_API_REST] = true
			}
		}
	}
	availableAPIs := make(map[catalogv1.CodeflyAPI]bool)
	for _, endpoint := range owner.Endpoints {
		api, _ := codeflyAPI(endpoint.API)
		availableAPIs[api] = true
	}
	for api := range requiredAPIs {
		if !availableAPIs[api] {
			return fmt.Errorf("catalog owner service %q has no endpoint for required API %s", owner.Name, api)
		}
	}
	return nil
}

func rejectDependencyCycles(services []deploymentServiceBinding) error {
	state := make(map[string]uint8, len(services))
	dependencies := make(map[string][]string, len(services))
	for _, service := range services {
		for _, dependency := range service.Dependencies {
			dependencies[service.Name] = append(dependencies[service.Name], dependency.Service)
		}
	}
	var visit func(string) error
	visit = func(service string) error {
		switch state[service] {
		case 1:
			return fmt.Errorf("deployment dependency graph contains a cycle at %q", service)
		case 2:
			return nil
		}
		state[service] = 1
		for _, dependency := range dependencies[service] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[service] = 2
		return nil
	}
	for _, service := range services {
		if err := visit(service.Name); err != nil {
			return err
		}
	}
	return nil
}

func endpointBindingsByName(endpoints []deploymentEndpointBinding) map[string]deploymentEndpointBinding {
	out := make(map[string]deploymentEndpointBinding, len(endpoints))
	for _, endpoint := range endpoints {
		out[endpoint.Name] = endpoint
	}
	return out
}

func codeflyAPI(value string) (catalogv1.CodeflyAPI, error) {
	switch value {
	case "grpc":
		return catalogv1.CodeflyAPI_CODEFLY_API_GRPC, nil
	case "rest":
		return catalogv1.CodeflyAPI_CODEFLY_API_REST, nil
	case "connect":
		return catalogv1.CodeflyAPI_CODEFLY_API_CONNECT, nil
	case "http":
		return catalogv1.CodeflyAPI_CODEFLY_API_HTTP, nil
	case "tcp":
		return catalogv1.CodeflyAPI_CODEFLY_API_TCP, nil
	default:
		return catalogv1.CodeflyAPI_CODEFLY_API_UNSPECIFIED, fmt.Errorf("unsupported Codefly API %q", value)
	}
}

func endpointVisibility(value string) (catalogv1.EndpointVisibility, error) {
	switch value {
	case "private":
		return catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_PRIVATE, nil
	case "module":
		return catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_MODULE, nil
	case "public":
		return catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_PUBLIC, nil
	case "external":
		return catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_EXTERNAL, nil
	default:
		return catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_UNSPECIFIED, fmt.Errorf("unsupported endpoint visibility %q", value)
	}
}

func buildDeploymentCatalog(bindings deploymentBindings) (*catalogv1.DeploymentCatalog, error) {
	catalog := &catalogv1.DeploymentCatalog{
		SchemaVersion: deploymentTopologySchemaVersion,
		Module:        bindings.Module.Name,
		Namespace:     bindings.Module.Namespace,
	}
	for _, service := range bindings.Services {
		entry := &catalogv1.DeploymentService{Name: service.Name}
		for _, endpoint := range service.Endpoints {
			api, _ := codeflyAPI(endpoint.API)
			visibility, _ := endpointVisibility(endpoint.Visibility)
			entry.Endpoints = append(entry.Endpoints, &catalogv1.CodeflyEndpoint{
				Name: endpoint.Name, Api: api, Visibility: visibility, Port: endpoint.Port,
			})
		}
		for _, dependency := range service.Dependencies {
			entry.Dependencies = append(entry.Dependencies, &catalogv1.ServiceDependency{
				Service: dependency.Service, Endpoints: append([]string(nil), dependency.Endpoints...),
			})
		}
		catalog.Services = append(catalog.Services, entry)
		if len(service.PublicEgressPorts) > 0 {
			catalog.PublicEgress = append(catalog.PublicEgress, &catalogv1.PublicEgress{
				Service: service.Name, Ports: append([]uint32(nil), service.PublicEgressPorts...),
			})
		}
	}
	for _, exposed := range bindings.Interface {
		visibility, _ := endpointVisibility(exposed.Visibility)
		catalog.InterfaceEndpoints = append(catalog.InterfaceEndpoints, &catalogv1.ModuleEndpoint{
			Service: exposed.Service, Endpoint: exposed.Endpoint, Visibility: visibility,
		})
	}
	if err := ValidateDeploymentCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

// ValidateDeploymentCatalog checks the normalized topology without repairing
// consumer-unsafe ordering, references, enum values, or cycles.
func ValidateDeploymentCatalog(catalog *catalogv1.DeploymentCatalog) error {
	if catalog == nil || catalog.GetSchemaVersion() != deploymentTopologySchemaVersion {
		return fmt.Errorf("unsupported deployment topology schema version")
	}
	if !endpointNamePattern.MatchString(catalog.GetModule()) || !endpointNamePattern.MatchString(catalog.GetNamespace()) || len(catalog.GetServices()) == 0 {
		return fmt.Errorf("deployment topology identity is incomplete")
	}
	services := make(map[string]*catalogv1.DeploymentService, len(catalog.GetServices()))
	previousService := ""
	for _, service := range catalog.GetServices() {
		if service == nil || !endpointNamePattern.MatchString(service.GetName()) || len(service.GetEndpoints()) == 0 ||
			(previousService != "" && service.GetName() <= previousService) {
			return fmt.Errorf("deployment services are incomplete or unsorted")
		}
		previousService = service.GetName()
		services[service.GetName()] = service
		previousEndpoint := ""
		for _, endpoint := range service.GetEndpoints() {
			if endpoint == nil || !endpointNamePattern.MatchString(endpoint.GetName()) || endpoint.GetPort() == 0 || endpoint.GetPort() > 65535 ||
				endpoint.GetApi() == catalogv1.CodeflyAPI_CODEFLY_API_UNSPECIFIED || endpoint.GetVisibility() == catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_UNSPECIFIED ||
				(previousEndpoint != "" && endpoint.GetName() <= previousEndpoint) {
				return fmt.Errorf("service %q endpoints are incomplete or unsorted", service.GetName())
			}
			previousEndpoint = endpoint.GetName()
		}
		previousDependency := ""
		for _, dependency := range service.GetDependencies() {
			if dependency == nil || dependency.GetService() == "" || len(dependency.GetEndpoints()) == 0 ||
				(previousDependency != "" && dependency.GetService() <= previousDependency) {
				return fmt.Errorf("service %q dependencies are incomplete or unsorted", service.GetName())
			}
			previousDependency = dependency.GetService()
			if !strictlySortedStrings(dependency.GetEndpoints()) {
				return fmt.Errorf("service %q dependency endpoints are not strictly sorted", service.GetName())
			}
		}
	}
	for _, service := range catalog.GetServices() {
		for _, dependency := range service.GetDependencies() {
			target := services[dependency.GetService()]
			if target == nil || target.GetName() == service.GetName() {
				return fmt.Errorf("service %q has an invalid dependency %q", service.GetName(), dependency.GetService())
			}
			known := make(map[string]bool, len(target.GetEndpoints()))
			for _, endpoint := range target.GetEndpoints() {
				known[endpoint.GetName()] = true
			}
			for _, endpoint := range dependency.GetEndpoints() {
				if !known[endpoint] {
					return fmt.Errorf("service %q references unknown endpoint %q of %q", service.GetName(), endpoint, target.GetName())
				}
			}
		}
	}
	previousInterface := ""
	for _, exposed := range catalog.GetInterfaceEndpoints() {
		key := exposed.GetService() + "/" + exposed.GetEndpoint()
		if exposed == nil || (previousInterface != "" && key <= previousInterface) {
			return fmt.Errorf("module interface endpoints are incomplete or unsorted")
		}
		previousInterface = key
		service := services[exposed.GetService()]
		if service == nil {
			return fmt.Errorf("module interface references unknown service %q", exposed.GetService())
		}
		matched := false
		for _, endpoint := range service.GetEndpoints() {
			if endpoint.GetName() == exposed.GetEndpoint() && endpoint.GetVisibility() == exposed.GetVisibility() &&
				(exposed.GetVisibility() == catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_MODULE || exposed.GetVisibility() == catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_PUBLIC) {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("module interface endpoint %q does not match its service endpoint", key)
		}
	}
	previousEgress := ""
	for _, egress := range catalog.GetPublicEgress() {
		if egress == nil || services[egress.GetService()] == nil || len(egress.GetPorts()) == 0 ||
			(previousEgress != "" && egress.GetService() <= previousEgress) || !strictlySortedPorts(egress.GetPorts()) {
			return fmt.Errorf("public egress entries are incomplete or unsorted")
		}
		previousEgress = egress.GetService()
	}
	return nil
}

func strictlySortedStrings(values []string) bool {
	previous := ""
	for _, value := range values {
		if value == "" || (previous != "" && value <= previous) {
			return false
		}
		previous = value
	}
	return true
}

func strictlySortedPorts(values []uint32) bool {
	previous := uint32(0)
	for _, value := range values {
		if value == 0 || value > 65535 || (previous != 0 && value <= previous) {
			return false
		}
		previous = value
	}
	return true
}

func renderDeploymentCatalogJSON(catalog *catalogv1.DeploymentCatalog) ([]byte, error) {
	document, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("render deployment topology catalog: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, document, "", "  "); err != nil {
		return nil, fmt.Errorf("format deployment topology catalog: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

type moduleManifest struct {
	Kind         string                  `yaml:"kind"`
	Name         string                  `yaml:"name"`
	Description  string                  `yaml:"description"`
	Agent        *deploymentAgentBinding `yaml:"agent,omitempty"`
	ServiceEntry string                  `yaml:"service-entry"`
	Interface    moduleManifestInterface `yaml:"interface"`
	Services     []manifestServiceRef    `yaml:"services"`
}

type moduleManifestInterface struct {
	Endpoints []manifestInterfaceEndpoint `yaml:"endpoints"`
}

type manifestInterfaceEndpoint struct {
	Service    string `yaml:"service"`
	Endpoint   string `yaml:"endpoint"`
	Visibility string `yaml:"visibility"`
}

type manifestServiceRef struct {
	Name string `yaml:"name"`
}

type serviceManifest struct {
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

type manifestServiceDependency struct {
	Name      string                      `yaml:"name"`
	Module    string                      `yaml:"module,omitempty"`
	Endpoints []manifestEndpointReference `yaml:"endpoints"`
}

type manifestEndpointReference struct {
	Name string `yaml:"name"`
}

type manifestEndpoint struct {
	Name       string `yaml:"name"`
	Visibility string `yaml:"visibility,omitempty"`
	API        string `yaml:"api,omitempty"`
}

func renderModuleManifest(bindings deploymentBindings) ([]byte, error) {
	manifest := moduleManifest{
		Kind: "module", Name: bindings.Module.Name, Description: bindings.Module.Description,
		Agent: bindings.Module.Agent, ServiceEntry: bindings.Module.ServiceEntry,
	}
	for _, exposed := range bindings.Interface {
		manifest.Interface.Endpoints = append(manifest.Interface.Endpoints, manifestInterfaceEndpoint(exposed))
	}
	for _, service := range bindings.Services {
		manifest.Services = append(manifest.Services, manifestServiceRef{Name: service.Name})
	}
	return marshalGeneratedYAML("deployment/topology.bindings.codefly.yaml", manifest)
}

func renderServiceManifest(service deploymentServiceBinding) ([]byte, error) {
	return renderServiceManifestWithExternalDependencies(
		service,
		nil,
		"deployment/topology.bindings.codefly.yaml",
	)
}

func renderServiceManifestWithExternalDependencies(
	service deploymentServiceBinding,
	external []manifestServiceDependency,
	source string,
) ([]byte, error) {
	manifest := serviceManifest{
		Name: service.Name, Version: service.Version, Description: service.Description, Agent: service.Agent,
		WorkspaceConfigurationDependencies: append([]string(nil), service.WorkspaceConfigurationDependencies...),
		SecretServiceConfigurations:        append([]secretServiceConfiguration(nil), service.SecretServiceConfigurations...),
		Spec:                               service.Spec,
	}
	for _, dependency := range service.Dependencies {
		entry := manifestServiceDependency{Name: dependency.Service}
		for _, endpoint := range dependency.Endpoints {
			entry.Endpoints = append(entry.Endpoints, manifestEndpointReference{Name: endpoint})
		}
		manifest.ServiceDependencies = append(manifest.ServiceDependencies, entry)
	}
	manifest.ServiceDependencies = append(manifest.ServiceDependencies, external...)
	for _, endpoint := range service.Endpoints {
		entry := manifestEndpoint{Name: endpoint.Name}
		if endpoint.API != endpoint.Name {
			entry.API = endpoint.API
		}
		if endpoint.Visibility != "private" {
			entry.Visibility = endpoint.Visibility
		}
		manifest.Endpoints = append(manifest.Endpoints, entry)
	}
	return marshalGeneratedYAML(source, manifest)
}

func marshalGeneratedYAML(source string, value any) ([]byte, error) {
	document, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("render generated YAML: %w", err)
	}
	header := fmt.Sprintf("# Code generated from %s. DO NOT EDIT.\n", source)
	return append([]byte(header), document...), nil
}

func kubernetesServiceName(service deploymentServiceBinding) string {
	if service.Kubernetes == nil {
		return service.Name
	}
	return service.Kubernetes.ServiceName
}

func kubernetesAppLabel(service deploymentServiceBinding) string {
	if service.Kubernetes == nil {
		return service.Name
	}
	return service.Kubernetes.AppLabel
}

type networkEdge struct {
	Caller    string
	CallerApp string
	Target    string
	TargetApp string
	Ports     []uint32
}

func deploymentNetworkEdges(bindings deploymentBindings) []networkEdge {
	services := make(map[string]deploymentServiceBinding, len(bindings.Services))
	for _, service := range bindings.Services {
		services[service.Name] = service
	}
	var edges []networkEdge
	for _, service := range bindings.Services {
		for _, dependency := range service.Dependencies {
			endpoints := endpointBindingsByName(services[dependency.Service].Endpoints)
			portSet := make(map[uint32]bool)
			for _, endpoint := range dependency.Endpoints {
				portSet[endpoints[endpoint].Port] = true
			}
			ports := make([]uint32, 0, len(portSet))
			for port := range portSet {
				ports = append(ports, port)
			}
			sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
			target := services[dependency.Service]
			edges = append(edges, networkEdge{
				Caller:    service.Name,
				CallerApp: kubernetesAppLabel(service),
				Target:    dependency.Service,
				TargetApp: kubernetesAppLabel(target),
				Ports:     ports,
			})
		}
	}
	return edges
}

func renderNetworkPolicy(bindings deploymentBindings) []byte {
	edges := deploymentNetworkEdges(bindings)
	byCaller := make(map[string][]networkEdge)
	byTarget := make(map[string][]networkEdge)
	for _, edge := range edges {
		byCaller[edge.Caller] = append(byCaller[edge.Caller], edge)
		byTarget[edge.Target] = append(byTarget[edge.Target], edge)
	}

	var source strings.Builder
	source.WriteString("# Code generated from deployment/topology.bindings.codefly.yaml. DO NOT EDIT.\n")
	writeStaticNetworkPolicies(&source, bindings)
	for _, target := range sortedMapKeys(byTarget) {
		writeDependencyIngressPolicy(&source, bindings.Module.Namespace, target, byTarget[target][0].TargetApp, byTarget[target])
	}
	for _, caller := range sortedMapKeys(byCaller) {
		writeDependencyEgressPolicy(&source, bindings.Module.Namespace, caller, byCaller[caller][0].CallerApp, byCaller[caller])
	}
	for _, service := range bindings.Services {
		if len(service.BootstrapJobEndpoints) == 0 {
			continue
		}
		endpoints := endpointBindingsByName(service.Endpoints)
		ports := make([]uint32, 0, len(service.BootstrapJobEndpoints))
		for _, endpoint := range service.BootstrapJobEndpoints {
			ports = append(ports, endpoints[endpoint].Port)
		}
		writeBootstrapJobPolicies(&source, bindings.Module.Namespace, service.Name, kubernetesAppLabel(service), ports)
	}
	for _, service := range bindings.Services {
		if len(service.PublicEgressPorts) > 0 {
			writePublicEgressPolicy(&source, bindings.Module.Namespace, service.Name, kubernetesAppLabel(service), service.PublicEgressPorts)
		}
	}
	return []byte(source.String())
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeStaticNetworkPolicies(source *strings.Builder, bindings deploymentBindings) {
	namespace := bindings.Module.Namespace
	fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
  namespace: %s
spec:
  podSelector: {}
  policyTypes:
    - Ingress
    - Egress
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-dns-egress
  namespace: %s
spec:
  podSelector: {}
  policyTypes:
    - Egress
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-istio-control-plane-egress
  namespace: %s
spec:
  podSelector: {}
  policyTypes:
    - Egress
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: istio-system
          podSelector:
            matchLabels:
              app: istiod
      ports:
        - protocol: TCP
          port: 15012
        - protocol: TCP
          port: 15014
`, namespace, namespace, namespace)

	for _, exposed := range bindings.Interface {
		if exposed.Visibility != "public" {
			continue
		}
		var port uint32
		var appLabel string
		for _, service := range bindings.Services {
			if service.Name == exposed.Service {
				port = endpointBindingsByName(service.Endpoints)[exposed.Endpoint].Port
				appLabel = kubernetesAppLabel(service)
			}
		}
		fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-istio-ingress-to-%s
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app: %s
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: istio-system
          podSelector:
            matchLabels:
              istio: ingressgateway
      ports:
        - protocol: TCP
          port: %d
`, exposed.Service, namespace, appLabel, port)
	}
}

func writeDependencyIngressPolicy(source *strings.Builder, namespace, target, targetApp string, edges []networkEdge) {
	fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-%s-from-dependents
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app: %s
  policyTypes:
    - Ingress
  ingress:
`, target, namespace, targetApp)
	for _, edge := range edges {
		fmt.Fprintf(source, "    - from:\n        - podSelector:\n            matchLabels:\n              app: %s\n      ports:\n", edge.CallerApp)
		writeNetworkPorts(source, edge.Ports, 8)
	}
}

func writeDependencyEgressPolicy(source *strings.Builder, namespace, caller, callerApp string, edges []networkEdge) {
	fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-%s-to-dependencies
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app: %s
  policyTypes:
    - Egress
  egress:
`, caller, namespace, callerApp)
	for _, edge := range edges {
		fmt.Fprintf(source, "    - to:\n        - podSelector:\n            matchLabels:\n              app: %s\n      ports:\n", edge.TargetApp)
		writeNetworkPorts(source, edge.Ports, 8)
	}
}

func writeBootstrapJobPolicies(source *strings.Builder, namespace, service, serviceApp string, ports []uint32) {
	fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-%s-from-bootstrap
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app: %s
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              codefly.dev/bootstrap-service: %s
              job-name: %s
      ports:
`, service, namespace, serviceApp, service, service)
	writeNetworkPorts(source, ports, 8)
	fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-%s-bootstrap-to-%s
  namespace: %s
spec:
  podSelector:
    matchLabels:
      codefly.dev/bootstrap-service: %s
      job-name: %s
  policyTypes:
    - Egress
  egress:
    - to:
        - podSelector:
            matchLabels:
              app: %s
      ports:
`, service, service, namespace, service, service, serviceApp)
	writeNetworkPorts(source, ports, 8)
}

func writeNetworkPorts(source *strings.Builder, ports []uint32, indent int) {
	padding := strings.Repeat(" ", indent)
	for _, port := range ports {
		fmt.Fprintf(source, "%s- protocol: TCP\n%s  port: %d\n", padding, padding, port)
	}
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

func writePublicEgressPolicy(source *strings.Builder, namespace, service, serviceApp string, ports []uint32) {
	fmt.Fprintf(source, `---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-%s-public-egress
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app: %s
  policyTypes:
    - Egress
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
`, service, namespace, serviceApp)
	for _, cidr := range publicIPv4Exceptions {
		fmt.Fprintf(source, "              - %s\n", cidr)
	}
	source.WriteString("        - ipBlock:\n            cidr: ::/0\n            except:\n")
	for _, cidr := range publicIPv6Exceptions {
		fmt.Fprintf(source, "              - %s\n", cidr)
	}
	source.WriteString("      ports:\n")
	writeNetworkPorts(source, ports, 8)
}
