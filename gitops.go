package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"gopkg.in/yaml.v3"
)

const (
	argoNamespace              = "argocd"
	cliRenderInventoryFilename = ".codefly-render.json"
	cliRenderInventorySchema   = 2
	coreManifestContract       = "codefly.dev/kubernetes-manifest/v1"
	inClusterServer            = "https://kubernetes.default.svc"
	gitOpsInventorySchema      = "codefly.dev/module-gitops/v3"
)

var (
	fullCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dnsLabelPattern   = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
	sha256Pattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
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

type workspaceGitOps struct {
	RepoURL      string `yaml:"repo-url"`
	FetchRepoURL string `yaml:"fetch-repo-url,omitempty"`
	Path         string `yaml:"path"`
	Branch       string `yaml:"branch"`
	Revision     string `yaml:"revision"`
	Checkout     string `yaml:"checkout,omitempty"`
	Inventory    string `yaml:"inventory"`
}

type environmentConfig struct {
	Name            string                          `yaml:"name"`
	Namespace       string                          `yaml:"namespace"`
	Cluster         environmentCluster              `yaml:"cluster"`
	Gitops          *workspaceGitOps                `yaml:"gitops,omitempty"`
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

type cliRenderInventory struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Module        string                   `json:"module"`
	Environment   string                   `json:"environment"`
	AppProject    string                   `json:"appProject"`
	OwnedPath     string                   `json:"ownedPath"`
	ServiceGraph  []cliRenderService       `json:"serviceGraph"`
	Files         []cliRenderInventoryFile `json:"files"`
	Digest        string                   `json:"digest"`
}

type cliRenderService struct {
	Module  string               `json:"module"`
	Service string               `json:"service"`
	Path    string               `json:"path,omitempty"`
	Managed bool                 `json:"managed,omitempty"`
	Output  *cliKubernetesOutput `json:"output,omitempty"`
}

type cliKubernetesOutput struct {
	Kind            string                   `json:"kind"`
	Profile         string                   `json:"profile"`
	ContractVersion string                   `json:"contractVersion"`
	Validation      *cliKubernetesValidation `json:"validation"`
}

type cliKubernetesValidation struct {
	StaticValidation     string   `json:"staticValidation"`
	ServerSideValidation string   `json:"serverSideValidation"`
	Promotable           bool     `json:"promotable"`
	Violations           []string `json:"violations"`
}

type cliRenderInventoryFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type gitOpsInventory struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Workspace     string                 `json:"workspace"`
	Module        string                 `json:"module"`
	OwnedPath     string                 `json:"ownedPath"`
	Environments  []environmentInventory `json:"environments"`
}

type environmentInventory struct {
	Name                   string                  `json:"name"`
	Namespace              string                  `json:"namespace"`
	Repository             string                  `json:"repository"`
	Revision               string                  `json:"revision"`
	Services               []string                `json:"services"`
	ServicePaths           map[string]string       `json:"servicePaths"`
	ManagedServiceHandoffs []managedServiceHandoff `json:"managedServiceHandoffs,omitempty"`
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

type appProjectSpec struct {
	Description                string              `yaml:"description"`
	SourceRepos                []string            `yaml:"sourceRepos"`
	Destinations               []argoDestination   `yaml:"destinations"`
	NamespaceResourceWhitelist []argoResourceAllow `yaml:"namespaceResourceWhitelist"`
	ClusterResourceWhitelist   []argoResourceAllow `yaml:"clusterResourceWhitelist,omitempty"`
}

type argoDestination struct {
	Namespace string `yaml:"namespace"`
	Server    string `yaml:"server"`
}

type argoResourceAllow struct {
	Group string `yaml:"group"`
	Kind  string `yaml:"kind"`
	Name  string `yaml:"name,omitempty"`
}

type applicationSpec struct {
	Project     string          `yaml:"project"`
	Source      applicationGit  `yaml:"source"`
	Destination argoDestination `yaml:"destination"`
	SyncPolicy  applicationSync `yaml:"syncPolicy"`
}

type applicationGit struct {
	RepoURL        string `yaml:"repoURL"`
	TargetRevision string `yaml:"targetRevision"`
	Path           string `yaml:"path"`
}

type applicationSync struct {
	Automated   automatedSync `yaml:"automated"`
	SyncOptions []string      `yaml:"syncOptions"`
}

type automatedSync struct {
	Prune    bool `yaml:"prune"`
	SelfHeal bool `yaml:"selfHeal"`
}

type environmentPlan struct {
	environment  *environmentConfig
	revision     string
	services     []string
	servicePaths map[string]string
	ingress      []ingressRoutePlan
	handoffs     []managedServiceHandoff
	managed      map[string]managedServiceConfig
	allowlist    []argoResourceAllow
	project      string
}

type gitOpsContract struct {
	repository      string
	fetchRepository string
	inventoryPath   string
	checkout        string
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
	Name         string `yaml:"name"`
	Namespace    string `yaml:"namespace"`
	ServiceEntry string `yaml:"service_entry"`
	Description  string `yaml:"description"`
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

func generateGitOps(ctx context.Context, moduleDir string, workspace *workspaceManifest) error {
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
	environments, err := validateGitOpsEnvironments(workspace)
	if err != nil {
		return err
	}

	root := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir))
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return fmt.Errorf("create deployment directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), ".kustomize-stage-*")
	if err != nil {
		return fmt.Errorf("create GitOps staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	inventory := gitOpsInventory{
		SchemaVersion: gitOpsInventorySchema,
		Workspace:     workspace.Name,
		Module:        manifest.Name,
	}
	for _, environment := range environments {
		local, aws, err := classifyEnvironment(environment)
		if err != nil {
			return err
		}
		contract, err := validateGitOpsContract(workspace, environment, local)
		if err != nil {
			return err
		}
		plan, err := planEnvironment(
			ctx,
			environment,
			manifest.Name,
			serviceNames(services),
			topology,
			contract.checkout,
			local,
			aws,
		)
		if err != nil {
			return err
		}
		renderInventory, err := inspectGitOpsSnapshot(
			ctx,
			contract,
			manifest.Name,
			serviceNames(services),
			&plan,
		)
		if err != nil {
			return err
		}
		if inventory.OwnedPath == "" {
			inventory.OwnedPath = renderInventory.OwnedPath
		} else if inventory.OwnedPath != renderInventory.OwnedPath {
			return fmt.Errorf(
				"environment %q owns path %q, want shared module path %q",
				environment.Name,
				renderInventory.OwnedPath,
				inventory.OwnedPath,
			)
		}
		if err := renderEnvironment(
			stage,
			workspace.Name,
			manifest.Name,
			contract.fetchRepository,
			topology,
			plan,
		); err != nil {
			return err
		}
		inventory.Environments = append(inventory.Environments, environmentInventory{
			Name:                   plan.environment.Name,
			Namespace:              plan.environment.Namespace,
			Repository:             contract.fetchRepository,
			Revision:               plan.revision,
			Services:               append([]string(nil), plan.services...),
			ServicePaths:           cloneStringMap(plan.servicePaths),
			ManagedServiceHandoffs: append([]managedServiceHandoff(nil), plan.handoffs...),
		})
	}
	if err := writeJSON(filepath.Join(stage, "inventory.json"), inventory); err != nil {
		return err
	}
	if err := validateGeneratedGitOps(stage, inventory); err != nil {
		return err
	}
	if err := replaceGeneratedTree(stage, root); err != nil {
		return err
	}
	return nil
}

func replaceGeneratedTree(stage, root string) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, root); err != nil {
			return fmt.Errorf("install generated GitOps directory: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect generated GitOps directory: %w", err)
	}

	backup, err := os.MkdirTemp(filepath.Dir(root), ".kustomize-backup-*")
	if err != nil {
		return fmt.Errorf("create GitOps backup path: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("prepare GitOps backup path: %w", err)
	}
	if err := os.Rename(root, backup); err != nil {
		return fmt.Errorf("back up generated GitOps directory: %w", err)
	}
	if err := os.Rename(stage, root); err != nil {
		if rollbackErr := os.Rename(backup, root); rollbackErr != nil {
			return fmt.Errorf("install generated GitOps directory: %w (rollback failed: %v; previous tree remains at %s)", err, rollbackErr, backup)
		}
		return fmt.Errorf("install generated GitOps directory: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove previous GitOps directory: %w", err)
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

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func validateGitOpsEnvironments(workspace *workspaceManifest) ([]*environmentConfig, error) {
	if len(workspace.Environments) == 0 {
		return nil, fmt.Errorf("workspace %q must declare GitOps environments", workspace.Name)
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
		if environment.Gitops == nil {
			return nil, fmt.Errorf("environment %q must declare its GitOps publication contract", environment.Name)
		}
		if err := validateDNSLabel("environment namespace", environment.Namespace); err != nil {
			return nil, fmt.Errorf("environment %q: %w", environment.Name, err)
		}
	}
	return workspace.Environments, nil
}

func validateGitOpsContract(
	workspace *workspaceManifest,
	environment *environmentConfig,
	local bool,
) (gitOpsContract, error) {
	if err := validateDNSLabel("workspace name", workspace.Name); err != nil {
		return gitOpsContract{}, err
	}
	config := environment.Gitops
	repository := strings.TrimSpace(config.RepoURL)
	if err := validatePublicationRepository(repository, local); err != nil {
		return gitOpsContract{}, fmt.Errorf("environment %q: %w", environment.Name, err)
	}
	fetchRepository := strings.TrimSpace(config.FetchRepoURL)
	if local {
		if err := validateLocalFetchRepository(fetchRepository); err != nil {
			return gitOpsContract{}, fmt.Errorf("environment %q: %w", environment.Name, err)
		}
	} else {
		if fetchRepository == "" {
			fetchRepository = repository
		}
		if err := validateRemoteRepository(fetchRepository); err != nil {
			return gitOpsContract{}, fmt.Errorf("workspace gitops fetch-repo-url: %w", err)
		}
		if canonicalRepository(fetchRepository) != canonicalRepository(repository) {
			return gitOpsContract{}, fmt.Errorf("workspace gitops fetch-repo-url must identify repo-url")
		}
	}
	base, err := cleanOptionalRelativePath(config.Path)
	if err != nil {
		return gitOpsContract{}, fmt.Errorf("workspace gitops path: %w", err)
	}
	inventoryPath, err := cleanRelativePath(config.Inventory)
	if err != nil {
		return gitOpsContract{}, fmt.Errorf("workspace gitops inventory: %w", err)
	}
	if base != "" && inventoryPath != base && !strings.HasPrefix(inventoryPath, base+"/") {
		return gitOpsContract{}, fmt.Errorf("workspace gitops inventory %q is outside path %q", inventoryPath, base)
	}
	if strings.TrimSpace(config.Branch) == "" {
		return gitOpsContract{}, fmt.Errorf("workspace gitops branch must name the publish branch")
	}
	if strings.TrimSpace(config.Revision) == "" {
		return gitOpsContract{}, fmt.Errorf("workspace gitops revision must select the immutable rendered snapshot")
	}
	checkout := workspace.root
	if strings.TrimSpace(config.Checkout) != "" {
		if filepath.IsAbs(config.Checkout) {
			checkout = filepath.Clean(config.Checkout)
		} else {
			relative, pathErr := cleanRelativeFilesystemPath(config.Checkout)
			if pathErr != nil {
				return gitOpsContract{}, fmt.Errorf("workspace gitops checkout: %w", pathErr)
			}
			checkout = filepath.Join(workspace.root, relative)
		}
	}
	info, err := os.Stat(checkout)
	if err != nil || !info.IsDir() {
		return gitOpsContract{}, fmt.Errorf("workspace gitops checkout %q is not a directory", checkout)
	}
	return gitOpsContract{
		repository:      repository,
		fetchRepository: fetchRepository,
		inventoryPath:   inventoryPath,
		checkout:        checkout,
	}, nil
}

func validatePublicationRepository(repository string, local bool) error {
	lowerRepository := strings.ToLower(repository)
	if repository == "" || strings.Contains(repository, "*") ||
		strings.Contains(lowerRepository, "replace_me") || strings.ContainsAny(repository, "\r\n\t ") {
		return fmt.Errorf("workspace gitops repo-url must be an exact repository URL")
	}
	if strings.HasPrefix(repository, "file://") {
		if !local {
			return fmt.Errorf("workspace gitops repo-url file transport is only allowed for local qualification")
		}
		parsed, err := url.Parse(repository)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.User != nil ||
			!filepath.IsAbs(filepath.FromSlash(parsed.Path)) || parsed.Path == "/" ||
			!strings.HasSuffix(parsed.Path, ".git") ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("workspace gitops repo-url must be an absolute credential-free file URL")
		}
		return nil
	}
	if err := validateRemoteRepository(repository); err != nil {
		return fmt.Errorf("workspace gitops repo-url: %w", err)
	}
	return nil
}

func validateRemoteRepository(repository string) error {
	if strings.Contains(repository, "://") {
		parsed, err := url.Parse(repository)
		if err != nil || parsed.Host == "" || parsed.Path == "" ||
			(parsed.Scheme != "https" && parsed.Scheme != "ssh") ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("must be an exact HTTPS or SSH repository URL")
		}
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password ||
				parsed.Scheme == "https" || parsed.User.Username() != "git" {
				return fmt.Errorf("must not contain credentials")
			}
		}
		return nil
	}
	if !validSCPRepository(repository) {
		return fmt.Errorf("must be an exact HTTPS or SSH repository URL")
	}
	return nil
}

func validateLocalFetchRepository(repository string) error {
	parsed, err := url.Parse(repository)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" ||
		!strings.HasSuffix(parsed.Path, ".git") ||
		parsed.Hostname() != "host.k3d.internal" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		strings.Contains(repository, "*") {
		return fmt.Errorf("workspace gitops fetch-repo-url must be an exact credential-free HTTP(S) URL on host.k3d.internal")
	}
	return nil
}

func validSCPRepository(value string) bool {
	if !strings.HasPrefix(value, "git@") {
		return false
	}
	hostAndPath := strings.TrimPrefix(value, "git@")
	host, repositoryPath, found := strings.Cut(hostAndPath, ":")
	return found && host != "" && !strings.ContainsAny(host, "/@") &&
		repositoryPath != "" && !strings.ContainsAny(repositoryPath, "?#") &&
		!strings.HasPrefix(repositoryPath, "/") && path.Clean(repositoryPath) == repositoryPath &&
		repositoryPath != ".." && !strings.HasPrefix(repositoryPath, "../")
}

func planEnvironment(
	ctx context.Context,
	environment *environmentConfig,
	moduleName string,
	services []string,
	topology deploymentTopology,
	checkout string,
	local, aws bool,
) (environmentPlan, error) {
	revision, err := resolveRevision(ctx, checkout, environment.Gitops.Revision, local)
	if err != nil {
		return environmentPlan{}, fmt.Errorf("environment %q: %w", environment.Name, err)
	}
	plan := environmentPlan{
		environment:  environment,
		revision:     revision,
		managed:      make(map[string]managedServiceConfig),
		servicePaths: make(map[string]string),
	}
	if err := validateManagedServices(environment, services, topology, aws, &plan); err != nil {
		return environmentPlan{}, err
	}
	for _, service := range services {
		if _, managed := plan.managed[service]; !managed {
			plan.services = append(plan.services, service)
		}
	}
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
	if len(environment.Ingress) == 0 {
		return fmt.Errorf("environment %q must declare at least one exact ingress route", environment.Name)
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

func classifyEnvironment(environment *environmentConfig) (local bool, aws bool, err error) {
	kind := strings.TrimSpace(environment.Cluster.Kind)
	switch kind {
	case "k3d", "kind", "minikube":
		return true, false, nil
	case "eks":
		return false, true, nil
	case "":
		if strings.HasPrefix(environment.Name, "local") {
			return true, false, nil
		}
	}
	return false, false, fmt.Errorf("environment %q cluster kind %q is not supported by the SaaS GitOps generator", environment.Name, kind)
}

func resolveRevision(ctx context.Context, workspaceRoot, configured string, local bool) (string, error) {
	revision := strings.TrimSpace(configured)
	if fullCommitPattern.MatchString(revision) {
		resolved, err := gitRevision(ctx, workspaceRoot, revision+"^{commit}")
		if err != nil || resolved != revision {
			return "", fmt.Errorf("configured commit %q does not exist in the GitOps repository checkout", revision)
		}
		if local {
			head, err := gitRevision(ctx, workspaceRoot, "HEAD")
			if err != nil {
				return "", fmt.Errorf("resolve local harness snapshot: %w", err)
			}
			if revision != head {
				return "", fmt.Errorf("local revision %s is not the harness snapshot %s", revision, head)
			}
		}
		return revision, nil
	}
	if local {
		selected, err := gitRevision(ctx, workspaceRoot, revision+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("resolve local harness branch %q: %w", revision, err)
		}
		head, err := gitRevision(ctx, workspaceRoot, "HEAD")
		if err != nil {
			return "", fmt.Errorf("resolve local harness snapshot: %w", err)
		}
		if selected != head {
			return "", fmt.Errorf("local branch %q resolves to %s, not harness snapshot %s", revision, selected, head)
		}
		return head, nil
	}

	tag := strings.TrimPrefix(revision, "refs/tags/")
	verify := exec.CommandContext(ctx, "git", "-C", workspaceRoot, "verify-tag", tag)
	if err := verify.Run(); err != nil {
		return "", fmt.Errorf("production revision must be a full commit SHA or a signed tag")
	}
	commit, err := gitRevision(ctx, workspaceRoot, "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve signed production tag %q: %w", tag, err)
	}
	return commit, nil
}

func gitRevision(ctx context.Context, root, revision string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", revision)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(output))
	if !fullCommitPattern.MatchString(commit) {
		return "", fmt.Errorf("git resolved invalid commit %q", commit)
	}
	return commit, nil
}

func inspectGitOpsSnapshot(
	ctx context.Context,
	contract gitOpsContract,
	moduleName string,
	services []string,
	plan *environmentPlan,
) (cliRenderInventory, error) {
	if err := validateCheckoutRepository(ctx, contract.checkout, contract.repository); err != nil {
		return cliRenderInventory{}, err
	}
	data, err := gitFile(ctx, contract.checkout, plan.revision, contract.inventoryPath)
	if err != nil {
		return cliRenderInventory{}, fmt.Errorf(
			"environment %q immutable revision has no CLI render inventory %q: %w",
			plan.environment.Name,
			contract.inventoryPath,
			err,
		)
	}
	inventory, canonical, err := loadCLIRenderInventory(
		data,
		contract.inventoryPath,
		moduleName,
		services,
		plan,
	)
	if err != nil {
		return cliRenderInventory{}, fmt.Errorf("environment %q: %w", plan.environment.Name, err)
	}
	snapshot, err := archiveGitOpsSnapshot(ctx, contract.checkout, plan.revision, inventory.OwnedPath)
	if err != nil {
		return cliRenderInventory{}, fmt.Errorf("environment %q: %w", plan.environment.Name, err)
	}
	defer func() { _ = os.RemoveAll(snapshot) }()
	if err := validateSnapshotInventory(snapshot, inventory, canonical); err != nil {
		return cliRenderInventory{}, fmt.Errorf("environment %q: %w", plan.environment.Name, err)
	}
	if err := inspectEnvironmentSnapshot(snapshot, inventory, plan); err != nil {
		return cliRenderInventory{}, fmt.Errorf("environment %q: %w", plan.environment.Name, err)
	}
	return inventory, nil
}

func gitFile(ctx context.Context, checkout, revision, file string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", "-C", checkout, "show", revision+":"+file)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}

func loadCLIRenderInventory(
	data []byte,
	inventoryPath,
	moduleName string,
	services []string,
	plan *environmentPlan,
) (cliRenderInventory, []byte, error) {
	var inventory cliRenderInventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return cliRenderInventory{}, nil, fmt.Errorf("decode CLI render inventory: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return cliRenderInventory{}, nil, fmt.Errorf("CLI render inventory contains trailing data")
	}
	canonical, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return cliRenderInventory{}, nil, fmt.Errorf("encode CLI render inventory: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		return cliRenderInventory{}, nil, fmt.Errorf("CLI render inventory is not canonical")
	}
	if inventory.SchemaVersion != cliRenderInventorySchema {
		return cliRenderInventory{}, nil, fmt.Errorf(
			"CLI render inventory schema is %d, want %d",
			inventory.SchemaVersion,
			cliRenderInventorySchema,
		)
	}
	if inventory.Module != moduleName || inventory.Environment != plan.environment.Name {
		return cliRenderInventory{}, nil, fmt.Errorf(
			"CLI render inventory targets module %q environment %q, want module %q environment %q",
			inventory.Module,
			inventory.Environment,
			moduleName,
			plan.environment.Name,
		)
	}
	if err := validateDNSLabel("CLI render inventory AppProject", inventory.AppProject); err != nil {
		return cliRenderInventory{}, nil, err
	}
	plan.project = inventory.AppProject
	ownedPath, err := cleanRelativePath(inventory.OwnedPath)
	if err != nil || ownedPath != inventory.OwnedPath {
		return cliRenderInventory{}, nil, fmt.Errorf("CLI render inventory ownedPath must be a canonical relative repository path")
	}
	if inventoryPath != path.Join(ownedPath, cliRenderInventoryFilename) {
		return cliRenderInventory{}, nil, fmt.Errorf(
			"CLI render inventory path %q must equal owned path inventory %q",
			inventoryPath,
			path.Join(ownedPath, cliRenderInventoryFilename),
		)
	}
	if err := validateCLIRenderGraph(&inventory, moduleName, services, plan); err != nil {
		return cliRenderInventory{}, nil, err
	}
	if err := validateCLIRenderFiles(inventory); err != nil {
		return cliRenderInventory{}, nil, err
	}
	return inventory, canonical, nil
}

func validateCLIRenderGraph(
	inventory *cliRenderInventory,
	moduleName string,
	services []string,
	plan *environmentPlan,
) error {
	expected := make(map[string]struct{}, len(services))
	for _, service := range services {
		expected[service] = struct{}{}
	}
	seen := make(map[string]struct{}, len(inventory.ServiceGraph))
	paths := make(map[string]string, len(plan.services))
	previous := ""
	for _, service := range inventory.ServiceGraph {
		if service.Module != moduleName {
			return fmt.Errorf(
				"CLI render inventory service %q belongs to module %q, want %q",
				service.Service,
				service.Module,
				moduleName,
			)
		}
		if _, exists := expected[service.Service]; !exists {
			return fmt.Errorf("CLI render inventory contains extra service %q", service.Service)
		}
		if _, exists := seen[service.Service]; exists {
			return fmt.Errorf("CLI render inventory repeats service %q", service.Service)
		}
		if previous != "" && service.Service <= previous {
			return fmt.Errorf("CLI render inventory serviceGraph is not sorted by service")
		}
		previous = service.Service
		seen[service.Service] = struct{}{}
		_, managed := plan.managed[service.Service]
		if service.Managed != managed {
			return fmt.Errorf(
				"CLI render inventory service %q managed=%t, want %t",
				service.Service,
				service.Managed,
				managed,
			)
		}
		if managed {
			if service.Path != "" || service.Output != nil {
				return fmt.Errorf("managed service %q must not contain in-cluster render output", service.Service)
			}
			continue
		}
		servicePath, err := cleanRelativePath(service.Path)
		if err != nil || servicePath != service.Path {
			return fmt.Errorf("CLI render inventory service %q path must be canonical and relative", service.Service)
		}
		expectedPath := path.Join("services", service.Service)
		if servicePath != expectedPath {
			return fmt.Errorf(
				"CLI render inventory service %q path is %q, want %q",
				service.Service,
				servicePath,
				expectedPath,
			)
		}
		if owner, exists := paths[servicePath]; exists {
			return fmt.Errorf("CLI render inventory services %q and %q share path %q", owner, service.Service, servicePath)
		}
		for existing, owner := range paths {
			if strings.HasPrefix(servicePath, existing+"/") || strings.HasPrefix(existing, servicePath+"/") {
				return fmt.Errorf(
					"CLI render inventory services %q and %q have overlapping paths %q and %q",
					owner,
					service.Service,
					existing,
					servicePath,
				)
			}
		}
		paths[servicePath] = service.Service
		if err := validateCorePromotableOutput(service.Service, service.Output); err != nil {
			return err
		}
		plan.servicePaths[service.Service] = path.Join(
			inventory.OwnedPath,
			servicePath,
			"overlays",
			plan.environment.Name,
		)
	}
	for _, service := range services {
		if _, exists := seen[service]; !exists {
			return fmt.Errorf("CLI render inventory is missing service %q", service)
		}
	}
	return nil
}

func validateCorePromotableOutput(service string, output *cliKubernetesOutput) error {
	if output == nil || output.Validation == nil {
		return fmt.Errorf("CLI render inventory service %q has no Core Kubernetes output evidence", service)
	}
	if output.Kind != builderv0.KubernetesDeploymentOutput_KUSTOMIZE.String() {
		return fmt.Errorf("CLI render inventory service %q output kind %q is not KUSTOMIZE", service, output.Kind)
	}
	wantProfile := builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_PROMOTABLE_GITOPS_V1.String()
	if output.Profile != wantProfile {
		return fmt.Errorf(
			"CLI render inventory service %q output profile %q is not %s",
			service,
			output.Profile,
			wantProfile,
		)
	}
	if output.ContractVersion != coreManifestContract {
		return fmt.Errorf(
			"CLI render inventory service %q Core contract is %q, want %q",
			service,
			output.ContractVersion,
			coreManifestContract,
		)
	}
	passed := builderv0.KubernetesManifestValidation_STATUS_PASSED.String()
	if output.Validation.StaticValidation != passed ||
		output.Validation.ServerSideValidation != passed ||
		!output.Validation.Promotable ||
		len(output.Validation.Violations) != 0 {
		return fmt.Errorf("CLI render inventory service %q Core output is not promotable", service)
	}
	if output.Validation.Violations == nil {
		return fmt.Errorf("CLI render inventory service %q Core output has no recorded violations result", service)
	}
	return nil
}

func validateCLIRenderFiles(inventory cliRenderInventory) error {
	graphPaths := make(map[string]string)
	for _, service := range inventory.ServiceGraph {
		if !service.Managed {
			graphPaths[service.Path] = service.Service
		}
	}
	covered := make(map[string]bool, len(graphPaths))
	digest := sha256.New()
	previous := ""
	for _, file := range inventory.Files {
		clean, err := cleanRelativePath(file.Path)
		if err != nil || clean != file.Path {
			return fmt.Errorf("CLI render inventory file path %q is not canonical and relative", file.Path)
		}
		if file.Path == cliRenderInventoryFilename {
			return fmt.Errorf("CLI render inventory must not include itself in files")
		}
		if previous != "" && file.Path <= previous {
			return fmt.Errorf("CLI render inventory files are not strictly sorted by path")
		}
		previous = file.Path
		if !sha256Pattern.MatchString(file.SHA256) || file.Size < 0 {
			return fmt.Errorf("CLI render inventory file %q has invalid digest or size", file.Path)
		}
		owner := ""
		for servicePath, service := range graphPaths {
			if file.Path == servicePath || strings.HasPrefix(file.Path, servicePath+"/") {
				if owner != "" {
					return fmt.Errorf("CLI render inventory file %q belongs to overlapping service paths", file.Path)
				}
				owner = service
				covered[servicePath] = true
			}
		}
		if owner == "" {
			return fmt.Errorf("CLI render inventory file %q is outside the exact service graph", file.Path)
		}
		if _, err := fmt.Fprintf(digest, "%s\x00%s\x00%d\n", file.Path, file.SHA256, file.Size); err != nil {
			return fmt.Errorf("digest CLI render inventory: %w", err)
		}
	}
	for servicePath, service := range graphPaths {
		if !covered[servicePath] {
			return fmt.Errorf("CLI render inventory service %q path %q contains no files", service, servicePath)
		}
	}
	wantDigest := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if inventory.Digest != wantDigest {
		return fmt.Errorf("CLI render inventory digest is %q, want %q", inventory.Digest, wantDigest)
	}
	return nil
}

func validateCheckoutRepository(ctx context.Context, checkout, repository string) error {
	command := exec.CommandContext(ctx, "git", "-C", checkout, "remote", "get-url", "--all", "origin")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("GitOps checkout %q has no origin remote", checkout)
	}
	want := canonicalRepository(repository)
	for _, remote := range strings.Fields(string(output)) {
		if canonicalRepository(remote) == want {
			return nil
		}
	}
	return fmt.Errorf("GitOps checkout %q origin does not match repository %q", checkout, repository)
}

func canonicalRepository(repository string) string {
	value := strings.TrimSuffix(strings.TrimSpace(repository), "/")
	value = strings.TrimSuffix(value, ".git")
	if strings.HasPrefix(value, "git@") {
		value = strings.TrimPrefix(value, "git@")
		value = strings.Replace(value, ":", "/", 1)
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host + parsed.Path
	}
	return strings.ToLower(strings.TrimSuffix(value, "/"))
}

func archiveGitOpsSnapshot(ctx context.Context, checkout, revision, ownedPath string) (string, error) {
	snapshot, err := os.MkdirTemp("", "saas-gitops-snapshot-*")
	if err != nil {
		return "", fmt.Errorf("create GitOps snapshot: %w", err)
	}
	archive := exec.CommandContext(ctx, "git", "-C", checkout, "archive", "--format=tar", revision, "--", ownedPath)
	extract := exec.CommandContext(ctx, "tar", "-x", "-C", snapshot)
	reader, err := archive.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(snapshot)
		return "", fmt.Errorf("read GitOps snapshot: %w", err)
	}
	extract.Stdin = reader
	var archiveError bytes.Buffer
	var extractError bytes.Buffer
	archive.Stderr = &archiveError
	extract.Stderr = &extractError
	if err := extract.Start(); err != nil {
		_ = os.RemoveAll(snapshot)
		return "", fmt.Errorf("extract GitOps snapshot: %w", err)
	}
	if err := archive.Run(); err != nil {
		_ = extract.Wait()
		_ = os.RemoveAll(snapshot)
		return "", fmt.Errorf("revision %s does not contain owned path %s: %s", revision, ownedPath, strings.TrimSpace(archiveError.String()))
	}
	if err := extract.Wait(); err != nil {
		_ = os.RemoveAll(snapshot)
		return "", fmt.Errorf("extract GitOps snapshot: %w: %s", err, strings.TrimSpace(extractError.String()))
	}
	return snapshot, nil
}

func validateSnapshotInventory(snapshot string, inventory cliRenderInventory, canonical []byte) error {
	ownedRoot := filepath.Join(snapshot, filepath.FromSlash(inventory.OwnedPath))
	expected := make(map[string]cliRenderInventoryFile, len(inventory.Files))
	for _, file := range inventory.Files {
		expected[file.Path] = file
	}
	foundInventory := false
	err := filepath.WalkDir(ownedRoot, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("immutable owned tree contains symbolic link %q", file)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("immutable owned tree contains non-regular file %q", file)
		}
		relative, err := filepath.Rel(ownedRoot, file)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if relative == cliRenderInventoryFilename {
			foundInventory = true
			if !bytes.Equal(data, canonical) {
				return fmt.Errorf("immutable CLI render inventory differs from selected revision evidence")
			}
			return nil
		}
		record, exists := expected[relative]
		if !exists {
			return fmt.Errorf("immutable snapshot contains file %q outside the CLI render inventory", relative)
		}
		sum := sha256.Sum256(data)
		if record.Size != int64(len(data)) || record.SHA256 != "sha256:"+hex.EncodeToString(sum[:]) {
			return fmt.Errorf("immutable snapshot file %q differs from the CLI render inventory", relative)
		}
		delete(expected, relative)
		return nil
	})
	if err != nil {
		return err
	}
	if !foundInventory {
		return fmt.Errorf("immutable owned tree is missing %s", cliRenderInventoryFilename)
	}
	if len(expected) != 0 {
		missing := make([]string, 0, len(expected))
		for file := range expected {
			missing = append(missing, file)
		}
		sort.Strings(missing)
		return fmt.Errorf("immutable snapshot is missing inventoried file %q", missing[0])
	}
	return nil
}

func inspectEnvironmentSnapshot(
	snapshot string,
	inventory cliRenderInventory,
	plan *environmentPlan,
) error {
	ownedRoot := filepath.Join(snapshot, filepath.FromSlash(inventory.OwnedPath))
	if err := scanOwnedTree(ownedRoot); err != nil {
		return err
	}

	allow := make(map[string]argoResourceAllow)
	for _, service := range plan.services {
		overlay := filepath.Join(snapshot, filepath.FromSlash(plan.servicePaths[service]))
		objects, err := renderOwnedKustomization(overlay)
		if err != nil {
			return fmt.Errorf("service %q immutable overlay is missing or invalid: %w", service, err)
		}
		if len(objects) == 0 {
			return fmt.Errorf("service %q immutable overlay renders no resources", service)
		}
		for _, object := range objects {
			resource, err := validateOwnedObject(object, plan.environment.Namespace)
			if err != nil {
				return fmt.Errorf("service %q: %w", service, err)
			}
			allow[resource.Group+"\x00"+resource.Kind] = resource
		}
	}
	plan.allowlist = make([]argoResourceAllow, 0, len(allow))
	for _, resource := range allow {
		plan.allowlist = append(plan.allowlist, resource)
	}
	sort.Slice(plan.allowlist, func(i, j int) bool {
		if plan.allowlist[i].Group != plan.allowlist[j].Group {
			return plan.allowlist[i].Group < plan.allowlist[j].Group
		}
		return plan.allowlist[i].Kind < plan.allowlist[j].Kind
	})
	return nil
}

func scanOwnedTree(root string) error {
	return filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		if strings.Contains(filepath.Base(file), ".secret.") {
			return fmt.Errorf("immutable owned tree contains secret file %q", filepath.ToSlash(relative))
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if unresolvedPattern.Match(data) {
			return fmt.Errorf("immutable owned tree file %q contains an unresolved placeholder or starter identity", filepath.ToSlash(relative))
		}
		if filepath.Ext(file) != ".yaml" && filepath.Ext(file) != ".yml" {
			return nil
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var document map[string]any
			if err := decoder.Decode(&document); err == io.EOF {
				break
			} else if err != nil {
				return fmt.Errorf("parse immutable owned tree file %q: %w", filepath.ToSlash(relative), err)
			}
			if document["kind"] == "Secret" {
				return fmt.Errorf("immutable owned tree contains Kubernetes Secret %q", filepath.ToSlash(relative))
			}
		}
		return nil
	})
}

func renderOwnedKustomization(root string) ([]map[string]any, error) {
	command := exec.Command("kubectl", "kustomize", root)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitError.Stderr)))
		}
		return nil, err
	}
	return decodeYAMLBytes(output, root)
}

func validateOwnedObject(object map[string]any, namespace string) (argoResourceAllow, error) {
	kind, _ := object["kind"].(string)
	apiVersion, _ := object["apiVersion"].(string)
	metadata, _ := object["metadata"].(map[string]any)
	if kind == "" || apiVersion == "" || metadata == nil {
		return argoResourceAllow{}, fmt.Errorf("rendered an incomplete Kubernetes object")
	}
	if kind == "Secret" {
		return argoResourceAllow{}, fmt.Errorf("immutable overlay contains a Kubernetes Secret")
	}
	if isClusterScopedKind(apiVersion, kind) {
		return argoResourceAllow{}, fmt.Errorf("immutable overlay contains cluster-scoped %s %q", kind, metadata["name"])
	}
	if renderedNamespace, _ := metadata["namespace"].(string); renderedNamespace != "" && renderedNamespace != namespace {
		return argoResourceAllow{}, fmt.Errorf("%s %q targets namespace %q, want %q", kind, metadata["name"], renderedNamespace, namespace)
	}
	group := ""
	if prefix, _, found := strings.Cut(apiVersion, "/"); found {
		group = prefix
	}
	return argoResourceAllow{Group: group, Kind: kind}, nil
}

func isClusterScopedKind(apiVersion, kind string) bool {
	key := apiVersion + "/" + kind
	switch key {
	case "v1/Namespace", "v1/Node", "v1/PersistentVolume",
		"apiextensions.k8s.io/v1/CustomResourceDefinition",
		"rbac.authorization.k8s.io/v1/ClusterRole",
		"rbac.authorization.k8s.io/v1/ClusterRoleBinding",
		"storage.k8s.io/v1/StorageClass":
		return true
	default:
		return false
	}
}

func renderEnvironment(
	root,
	workspaceName,
	moduleName,
	repository string,
	topology deploymentTopology,
	plan environmentPlan,
) error {
	environmentRoot := filepath.Join(root, "overlays", plan.environment.Name)
	resourceRoot := filepath.Join(environmentRoot, "resources")
	applicationRoot := filepath.Join(environmentRoot, "applications")
	if err := os.MkdirAll(resourceRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(applicationRoot, 0o755); err != nil {
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
				"istio-injection":                            "enabled",
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

	project := kubeObject{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "AppProject",
		Metadata: objectMeta{
			Name:       plan.project,
			Namespace:  argoNamespace,
			Labels:     identityLabels,
			Finalizers: []string{"resources-finalizer.argocd.argoproj.io"},
		},
		Spec: appProjectSpec{
			Description:                fmt.Sprintf("%s/%s %s GitOps boundary", workspaceName, moduleName, plan.environment.Name),
			SourceRepos:                []string{repository},
			Destinations:               []argoDestination{{Namespace: plan.environment.Namespace, Server: inClusterServer}},
			NamespaceResourceWhitelist: plan.allowlist,
		},
	}
	if err := writeYAML(filepath.Join(resourceRoot, "project.yaml"), project); err != nil {
		return err
	}
	sharedPaths, err := renderSharedResources(resourceRoot, plan.environment.Namespace, identityLabels, topology, plan)
	if err != nil {
		return err
	}

	resourcePaths := []string{
		"resources/namespace.yaml",
		"resources/project.yaml",
		"resources/resource-quota.yaml",
		"resources/limit-range.yaml",
	}
	resourcePaths = append(resourcePaths, sharedPaths...)
	for _, service := range plan.services {
		application := kubeObject{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Metadata: objectMeta{
				Name:       kubernetesName(workspaceName, moduleName, service, plan.environment.Name),
				Namespace:  argoNamespace,
				Labels:     mergeLabels(identityLabels, map[string]string{"codefly.dev/service": service}),
				Finalizers: []string{"resources-finalizer.argocd.argoproj.io"},
			},
			Spec: applicationSpec{
				Project: plan.project,
				Source: applicationGit{
					RepoURL:        repository,
					TargetRevision: plan.revision,
					Path:           plan.servicePaths[service],
				},
				Destination: argoDestination{Namespace: plan.environment.Namespace, Server: inClusterServer},
				SyncPolicy: applicationSync{
					Automated:   automatedSync{Prune: true, SelfHeal: true},
					SyncOptions: []string{"CreateNamespace=false"},
				},
			},
		}
		relative := path.Join("applications", service+".yaml")
		if err := writeYAML(filepath.Join(environmentRoot, filepath.FromSlash(relative)), application); err != nil {
			return err
		}
		resourcePaths = append(resourcePaths, relative)
	}
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
	if len(plan.ingress) == 0 {
		return nil, nil, fmt.Errorf("environment %q has no ingress plan", plan.environment.Name)
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
		{
			APIVersion: "networking.istio.io/v1",
			Kind:       "DestinationRule",
			Metadata:   objectMeta{Name: name + "-defaults", Namespace: namespace, Labels: labels},
			Spec: map[string]any{
				"host":     "*." + namespace + ".svc.cluster.local",
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
		},
	}
	return istio, gateway, nil
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
	"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32",
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

func validateGeneratedGitOps(root string, inventory gitOpsInventory) error {
	for _, environment := range inventory.Environments {
		rendered, err := renderKustomization(filepath.Join(root, "overlays", environment.Name))
		if err != nil {
			return fmt.Errorf("render environment %q: %w", environment.Name, err)
		}
		if err := validateRenderedObjects(rendered, inventory, environment); err != nil {
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

func validateRenderedObjects(objects []map[string]any, inventory gitOpsInventory, environment environmentInventory) error {
	expectedServices := make(map[string]struct{}, len(environment.Services))
	for _, service := range environment.Services {
		expectedServices[service] = struct{}{}
		if environment.ServicePaths[service] == "" {
			return fmt.Errorf("service %q has no exact Application path", service)
		}
	}
	if len(environment.ServicePaths) != len(expectedServices) {
		return fmt.Errorf("service path inventory does not match in-cluster service inventory")
	}
	applications := make(map[string]struct{}, len(environment.Services))
	projectCount := 0
	namespaceCount := 0
	for _, object := range objects {
		kind, _ := object["kind"].(string)
		metadata, _ := object["metadata"].(map[string]any)
		switch kind {
		case "Secret":
			return fmt.Errorf("GitOps output contains a Kubernetes Secret")
		case "Namespace":
			namespaceCount++
			if metadata["name"] != environment.Namespace {
				return fmt.Errorf("namespace resource targets %v", metadata["name"])
			}
		case "AppProject":
			projectCount++
			spec, _ := object["spec"].(map[string]any)
			if containsWildcard(spec["sourceRepos"]) ||
				containsWildcard(spec["destinations"]) ||
				containsWildcard(spec["namespaceResourceWhitelist"]) ||
				containsWildcard(spec["clusterResourceWhitelist"]) {
				return fmt.Errorf("app project contains wildcard authority")
			}
			repositories, _ := spec["sourceRepos"].([]any)
			if len(repositories) != 1 || repositories[0] != environment.Repository {
				return fmt.Errorf("app project source repository is not exact")
			}
		case "Application":
			spec, _ := object["spec"].(map[string]any)
			source, _ := spec["source"].(map[string]any)
			revision, _ := source["targetRevision"].(string)
			if !fullCommitPattern.MatchString(revision) {
				return fmt.Errorf("application revision %q is mutable", revision)
			}
			if source["repoURL"] != environment.Repository {
				return fmt.Errorf("application repository does not match workspace contract")
			}
			labels, _ := metadata["labels"].(map[string]any)
			service, _ := labels["codefly.dev/service"].(string)
			if _, exists := expectedServices[service]; !exists {
				return fmt.Errorf("application references undeclared service %q", service)
			}
			expectedPath := environment.ServicePaths[service]
			if expectedPath == "" {
				return fmt.Errorf("application service %q has no inventoried path", service)
			}
			if source["path"] != expectedPath {
				return fmt.Errorf("application service %q path is %v, want %s", service, source["path"], expectedPath)
			}
			if _, exists := applications[service]; exists {
				return fmt.Errorf("service %q has more than one Application", service)
			}
			applications[service] = struct{}{}
		}
	}
	if namespaceCount != 1 || projectCount != 1 {
		return fmt.Errorf("render must contain one Namespace and one AppProject")
	}
	if len(applications) != len(expectedServices) {
		return fmt.Errorf("rendered %d Applications for %d in-cluster services", len(applications), len(expectedServices))
	}
	return nil
}

func containsWildcard(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed == "*"
	case []any:
		for _, item := range typed {
			if containsWildcard(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsWildcard(item) {
				return true
			}
		}
	}
	return false
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

func cleanOptionalRelativePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return cleanRelativePath(value)
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
