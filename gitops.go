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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	argoNamespace                  = "argocd"
	inClusterServer                = "https://kubernetes.default.svc"
	gitOpsInventorySchema          = "codefly.dev/module-gitops/v1"
	secretReferenceContractVersion = "github.com/codefly-dev/core/configurations@v0.2.47#reference-only-secret-manifest"
)

var (
	fullCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dnsLabelPattern   = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
	unresolvedPattern = regexp.MustCompile(`(?i)REPLACE_ME|saas-starter|\$\{[^}]+\}|<[^>]*replace[^>]*>`)
)

var awsManagedServices = map[string]string{
	"cache":          "elasticache",
	"object-storage": "s3",
	"store":          "rds-postgresql",
	"vault":          "secrets-manager",
}

type moduleManifest struct {
	Name     string             `yaml:"name"`
	Services []serviceReference `yaml:"services"`
}

type workspaceManifest struct {
	Name         string               `yaml:"name"`
	Gitops       *workspaceGitOps     `yaml:"gitops,omitempty"`
	Environments []*environmentConfig `yaml:"environments,omitempty"`
	root         string
}

type workspaceGitOps struct {
	RepoURL string `yaml:"repo-url"`
	Path    string `yaml:"path"`
	Branch  string `yaml:"branch"`
}

type environmentConfig struct {
	Name      string             `yaml:"name"`
	Namespace string             `yaml:"namespace"`
	Cluster   environmentCluster `yaml:"cluster"`
}

type environmentCluster struct {
	Kind string `yaml:"kind"`
}

type serviceReference struct {
	Name string  `yaml:"name"`
	Path *string `yaml:"path,omitempty"`
}

type gitOpsInventory struct {
	SchemaVersion           string                 `json:"schemaVersion"`
	Workspace               string                 `json:"workspace"`
	Module                  string                 `json:"module"`
	Repository              string                 `json:"repository"`
	OwnedPath               string                 `json:"ownedPath"`
	SecretReferenceContract string                 `json:"secretReferenceContract"`
	Environments            []environmentInventory `json:"environments"`
}

type environmentInventory struct {
	Name                   string                  `json:"name"`
	Namespace              string                  `json:"namespace"`
	Revision               string                  `json:"revision"`
	Services               []string                `json:"services"`
	ManagedServiceHandoffs []managedServiceHandoff `json:"managedServiceHandoffs,omitempty"`
}

type managedServiceHandoff struct {
	Service string `json:"service"`
	AWSKind string `json:"awsKind"`
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
	ClusterResourceWhitelist   []argoResourceAllow `yaml:"clusterResourceWhitelist"`
}

type argoDestination struct {
	Namespace string `yaml:"namespace"`
	Server    string `yaml:"server"`
}

type argoResourceAllow struct {
	Group string `yaml:"group"`
	Kind  string `yaml:"kind"`
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
	environment *environmentConfig
	revision    string
	services    []string
	handoffs    []managedServiceHandoff
	project     string
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
	if workspace.Gitops == nil {
		return fmt.Errorf("workspace %q must declare gitops repository, path, and branch", workspace.Name)
	}
	repository, ownedPath, err := validateGitOpsContract(workspace, manifest.Name)
	if err != nil {
		return err
	}
	plans, err := planEnvironments(ctx, workspace, manifest.Name, services)
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
	defer os.RemoveAll(stage)

	inventory := gitOpsInventory{
		SchemaVersion:           gitOpsInventorySchema,
		Workspace:               workspace.Name,
		Module:                  manifest.Name,
		Repository:              repository,
		OwnedPath:               ownedPath,
		SecretReferenceContract: secretReferenceContractVersion,
	}
	for _, plan := range plans {
		if err := renderEnvironment(stage, workspace.Name, manifest.Name, repository, ownedPath, plan); err != nil {
			return err
		}
		inventory.Environments = append(inventory.Environments, environmentInventory{
			Name:                   plan.environment.Name,
			Namespace:              plan.environment.Namespace,
			Revision:               plan.revision,
			Services:               append([]string(nil), plan.services...),
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

func validateServiceInventory(moduleDir string, references []serviceReference) ([]string, error) {
	declared := make(map[string]string, len(references))
	declaredPaths := make(map[string]string, len(references))
	services := make([]string, 0, len(references))
	for _, reference := range references {
		if err := validateDNSLabel("service name", reference.Name); err != nil {
			return nil, err
		}
		if _, exists := declared[reference.Name]; exists {
			return nil, fmt.Errorf("module declares service %q more than once", reference.Name)
		}
		relative := reference.Name
		if reference.Path != nil {
			var err error
			relative, err = cleanRelativePath(*reference.Path)
			if err != nil {
				return nil, fmt.Errorf("service %q path: %w", reference.Name, err)
			}
		}
		if owner, exists := declaredPaths[relative]; exists {
			return nil, fmt.Errorf("services %q and %q declare the same path %q", owner, reference.Name, relative)
		}
		declared[reference.Name] = relative
		declaredPaths[relative] = reference.Name
		services = append(services, reference.Name)
		serviceRoot := filepath.Join(moduleDir, "services")
		current := serviceRoot
		for _, element := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
			current = filepath.Join(current, element)
			info, err := os.Lstat(current)
			if err != nil {
				return nil, fmt.Errorf("declared service %q path %q is missing: %w", reference.Name, relative, err)
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("declared service %q path %q is not a directory", reference.Name, relative)
			}
		}
	}

	serviceRoot := filepath.Join(moduleDir, "services")
	expectedPaths := make(map[string]struct{}, len(declared))
	for _, relative := range declared {
		expectedPaths[filepath.FromSlash(relative)] = struct{}{}
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

func validateGitOpsContract(workspace *workspaceManifest, moduleName string) (string, string, error) {
	if err := validateDNSLabel("workspace name", workspace.Name); err != nil {
		return "", "", err
	}
	repository := strings.TrimSpace(workspace.Gitops.RepoURL)
	lowerRepository := strings.ToLower(repository)
	if repository == "" || strings.Contains(repository, "*") ||
		strings.Contains(lowerRepository, "replace_me") || strings.ContainsAny(repository, "\r\n\t ") {
		return "", "", fmt.Errorf("workspace gitops repo-url must be an exact repository URL")
	}
	if strings.Contains(repository, "://") {
		parsed, err := url.Parse(repository)
		if err != nil || parsed.Host == "" || parsed.Path == "" ||
			(parsed.Scheme != "https" && parsed.Scheme != "ssh") ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", fmt.Errorf("workspace gitops repo-url must be an exact HTTPS or SSH repository URL")
		}
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password ||
				parsed.Scheme == "https" || parsed.User.Username() != "git" {
				return "", "", fmt.Errorf("workspace gitops repo-url must not contain credentials")
			}
		}
	} else if !validSCPRepository(repository) {
		return "", "", fmt.Errorf("workspace gitops repo-url must be an exact HTTPS or SSH repository URL")
	}
	base, err := cleanOptionalRelativePath(workspace.Gitops.Path)
	if err != nil {
		return "", "", fmt.Errorf("workspace gitops path: %w", err)
	}
	if strings.TrimSpace(workspace.Gitops.Branch) == "" {
		return "", "", fmt.Errorf("workspace gitops branch must select a revision")
	}
	owned := path.Join(base, "deployments", "modules", moduleName, "services")
	return repository, owned, nil
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

func planEnvironments(ctx context.Context, workspace *workspaceManifest, moduleName string, services []string) ([]environmentPlan, error) {
	if len(workspace.Environments) == 0 {
		return nil, fmt.Errorf("workspace %q must declare at least one GitOps environment", workspace.Name)
	}
	seen := make(map[string]struct{}, len(workspace.Environments))
	plans := make([]environmentPlan, 0, len(workspace.Environments))
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
		local, aws, err := classifyEnvironment(environment)
		if err != nil {
			return nil, err
		}
		revision, err := resolveRevision(ctx, workspace.root, workspace.Gitops.Branch, local)
		if err != nil {
			return nil, fmt.Errorf("environment %q: %w", environment.Name, err)
		}
		plan := environmentPlan{
			environment: environment,
			revision:    revision,
			project:     kubernetesName(workspace.Name, moduleName, environment.Name),
		}
		for _, service := range services {
			if aws {
				if kind, managed := awsManagedServices[service]; managed {
					plan.handoffs = append(plan.handoffs, managedServiceHandoff{Service: service, AWSKind: kind})
					continue
				}
			}
			plan.services = append(plan.services, service)
		}
		plans = append(plans, plan)
	}
	return plans, nil
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

func renderEnvironment(root, workspaceName, moduleName, repository, ownedPath string, plan environmentPlan) error {
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
			Description:  fmt.Sprintf("%s/%s %s GitOps boundary", workspaceName, moduleName, plan.environment.Name),
			SourceRepos:  []string{repository},
			Destinations: []argoDestination{{Namespace: plan.environment.Namespace, Server: inClusterServer}},
			NamespaceResourceWhitelist: []argoResourceAllow{
				{Group: "", Kind: "ConfigMap"},
				{Group: "", Kind: "LimitRange"},
				{Group: "", Kind: "PersistentVolumeClaim"},
				{Group: "", Kind: "ResourceQuota"},
				{Group: "", Kind: "Service"},
				{Group: "", Kind: "ServiceAccount"},
				{Group: "apps", Kind: "Deployment"},
				{Group: "apps", Kind: "StatefulSet"},
				{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
				{Group: "batch", Kind: "CronJob"},
				{Group: "batch", Kind: "Job"},
				{Group: "external-secrets.io", Kind: "ExternalSecret"},
				{Group: "networking.istio.io", Kind: "DestinationRule"},
				{Group: "networking.istio.io", Kind: "Gateway"},
				{Group: "networking.istio.io", Kind: "VirtualService"},
				{Group: "networking.k8s.io", Kind: "Ingress"},
				{Group: "networking.k8s.io", Kind: "NetworkPolicy"},
				{Group: "policy", Kind: "PodDisruptionBudget"},
				{Group: "security.istio.io", Kind: "AuthorizationPolicy"},
				{Group: "security.istio.io", Kind: "PeerAuthentication"},
			},
			ClusterResourceWhitelist: []argoResourceAllow{{Group: "", Kind: "Namespace"}},
		},
	}
	if err := writeYAML(filepath.Join(resourceRoot, "project.yaml"), project); err != nil {
		return err
	}
	if err := renderSharedResources(resourceRoot, plan.environment.Namespace, identityLabels); err != nil {
		return err
	}

	resourcePaths := []string{
		"resources/namespace.yaml",
		"resources/project.yaml",
		"resources/resource-quota.yaml",
		"resources/limit-range.yaml",
		"resources/network-policy.yaml",
		"resources/istio-mtls.yaml",
	}
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
					Path:           path.Join(ownedPath, service, "overlays", plan.environment.Name),
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

func renderSharedResources(root, namespace string, labels map[string]string) error {
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
		return err
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
		return err
	}
	networkPolicies := []kubeObject{
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
	if err := writeYAMLDocuments(filepath.Join(root, "network-policy.yaml"), networkPolicies); err != nil {
		return err
	}
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
	return writeYAMLDocuments(filepath.Join(root, "istio-mtls.yaml"), istio)
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
				return fmt.Errorf("AppProject contains wildcard authority")
			}
			repositories, _ := spec["sourceRepos"].([]any)
			if len(repositories) != 1 || repositories[0] != inventory.Repository {
				return fmt.Errorf("AppProject source repository is not exact")
			}
		case "Application":
			spec, _ := object["spec"].(map[string]any)
			source, _ := spec["source"].(map[string]any)
			revision, _ := source["targetRevision"].(string)
			if !fullCommitPattern.MatchString(revision) {
				return fmt.Errorf("Application revision %q is mutable", revision)
			}
			if source["repoURL"] != inventory.Repository {
				return fmt.Errorf("Application repository does not match workspace contract")
			}
			labels, _ := metadata["labels"].(map[string]any)
			service, _ := labels["codefly.dev/service"].(string)
			if _, exists := expectedServices[service]; !exists {
				return fmt.Errorf("Application references undeclared service %q", service)
			}
			expectedPath := path.Join(inventory.OwnedPath, service, "overlays", environment.Name)
			if source["path"] != expectedPath {
				return fmt.Errorf("Application service %q path is %v, want %s", service, source["path"], expectedPath)
			}
			if _, exists := applications[service]; exists {
				return fmt.Errorf("service %q has more than one Application", service)
			}
			applications[service] = struct{}{}
		default:
			if _, exists := object["data"]; exists {
				return fmt.Errorf("%s contains data outside the secret-reference contract", kind)
			}
			if _, exists := object["stringData"]; exists {
				return fmt.Errorf("%s contains stringData outside the secret-reference contract", kind)
			}
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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []map[string]any
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
		if len(document) == 0 {
			continue
		}
		if document["apiVersion"] == nil || document["kind"] == nil || document["metadata"] == nil {
			return nil, fmt.Errorf("%s contains an incomplete Kubernetes object", file)
		}
		documents = append(documents, document)
	}
	return documents, nil
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
