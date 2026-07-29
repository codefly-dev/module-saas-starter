package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const fixtureRevision = "0123456789abcdef0123456789abcdef01234567"

var fixtureAWSManagedServices = map[string]string{
	"cache":          "elasticache",
	"object-storage": "s3",
	"store":          "rds-postgresql",
	"vault":          "secrets-manager",
}

func TestGenerateGitOpsGoldenShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		workspace string
		module    string
		services  []string
		golden    string
	}{
		{
			name:      "warden",
			workspace: "warden-control",
			module:    "identity",
			services:  []string{"accounts", "auth-sidecar", "cache", "frontend", "object-storage", "store", "vault"},
			golden:    "testdata/warden-inventory.golden.json",
		},
		{
			name:      "mind",
			workspace: "mind-control",
			module:    "users",
			services:  []string{"store", "vault", "accounts", "cache", "frontend", "forge-edge", "object-storage"},
			golden:    "testdata/mind-inventory.golden.json",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, moduleDir := writeGitOpsFixture(t, test.workspace, test.module, test.services)
			workspace, err := loadWorkspaceManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "inventory.json"))
			if err != nil {
				t.Fatal(err)
			}
			var normalized gitOpsInventory
			if err := json.Unmarshal(got, &normalized); err != nil {
				t.Fatal(err)
			}
			for index := range normalized.Environments {
				normalized.Environments[index].Revision = fixtureRevision
			}
			got, err = json.MarshalIndent(normalized, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			want, err := os.ReadFile(test.golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("inventory golden mismatch\nwant:\n%s\ngot:\n%s", want, got)
			}

			assertEnvironmentApplications(t, moduleDir, "local", test.services)
			awsServices := make([]string, 0, len(test.services))
			for _, service := range test.services {
				if _, managed := fixtureAWSManagedServices[service]; !managed {
					awsServices = append(awsServices, service)
				}
			}
			assertEnvironmentApplications(t, moduleDir, "aws", awsServices)
		})
	}
}

func TestGenerateGitOpsRejectsHostileContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(t *testing.T, root, moduleDir string, workspace *workspaceManifest)
		want   string
	}{
		{
			name: "missing gitops contract",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops = nil
			},
			want: "must declare gitops",
		},
		{
			name: "placeholder repository",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "REPLACE_ME"
			},
			want: "exact repository URL",
		},
		{
			name: "repository credentials",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "https://token@github.com/acme/platform.git"
			},
			want: "must not contain credentials",
		},
		{
			name: "repository query credentials",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "https://github.com/acme/platform.git?token=secret"
			},
			want: "exact HTTPS or SSH repository URL",
		},
		{
			name: "SCP repository query credentials",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "git@github.com:acme/platform.git?token=secret"
			},
			want: "exact HTTPS or SSH repository URL",
		},
		{
			name: "insecure repository transport",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.RepoURL = "http://github.com/acme/platform.git"
			},
			want: "exact HTTPS or SSH repository URL",
		},
		{
			name: "escaping owned path",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.Path = "../other"
			},
			want: "canonical relative repository path",
		},
		{
			name: "missing publish branch",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.Branch = ""
			},
			want: "publish branch",
		},
		{
			name: "missing immutable revision",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.Revision = ""
			},
			want: "immutable rendered snapshot",
		},
		{
			name: "mutable production revision",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Gitops.Revision = "main"
				workspace.Environments = workspace.Environments[1:]
			},
			want: "full commit SHA or a signed tag",
		},
		{
			name: "missing namespace",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Environments[1].Namespace = ""
			},
			want: "Kubernetes DNS label",
		},
		{
			name: "wildcard ingress host",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Environments[0].Ingress = []environmentIngressRoute{{
					Name:     "product",
					Service:  "accounts",
					Endpoint: "http",
					Hosts:    []string{"*"},
				}}
			},
			want: "is not an exact DNS name",
		},
		{
			name: "unsupported remote cluster",
			mutate: func(_ *testing.T, _, _ string, workspace *workspaceManifest) {
				workspace.Environments[1].Cluster.Kind = "gke"
			},
			want: "is not supported",
		},
		{
			name: "missing service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(moduleDir, "services", "frontend")); err != nil {
					t.Fatal(err)
				}
			},
			want: "declared service \"frontend\" path",
		},
		{
			name: "extra service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(moduleDir, "services", "phantom"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "has no module service declaration",
		},
		{
			name: "duplicate service",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), `kind: module
name: identity
services:
  - name: accounts
  - name: accounts
`)
			},
			want: "more than once",
		},
		{
			name: "duplicate service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), `kind: module
name: identity
services:
  - name: accounts
    path: accounts
  - name: frontend
    path: accounts
`)
			},
			want: "declare the same path",
		},
		{
			name: "symlinked service path",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(moduleDir, "services", "accounts")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(moduleDir, "services", "accounts")); err != nil {
					t.Fatal(err)
				}
			},
			want: "is not a directory",
		},
		{
			name: "service path below symlink",
			mutate: func(t *testing.T, _, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				external := t.TempDir()
				if err := os.Mkdir(filepath.Join(external, "accounts"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(moduleDir, "services")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(moduleDir, "services"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(moduleDir, "services", "external")); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), `kind: module
name: identity
services:
  - name: accounts
    path: external/accounts
`)
			},
			want: "is not a directory",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, moduleDir := writeGitOpsFixture(t, "hostile-control", "identity", []string{"accounts", "frontend"})
			workspace, err := loadWorkspaceManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root, moduleDir, workspace)
			err = generateGitOps(context.Background(), moduleDir, workspace)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generateGitOps() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestGenerateGitOpsReplacesStaleSecretBearingTree(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "safe-control", "identity", []string{"accounts"})
	staleRoot := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir))
	writeTestFile(t, filepath.Join(staleRoot, "overlays", "aws", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: leaked
stringData:
  password: hostile-canary
`)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(staleRoot, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "hostile-canary") || strings.Contains(string(data), "kind: Secret") {
			t.Fatalf("secret material survived in %s", file)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateGitOpsRejectsSecretInImmutableServiceRender(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "secret-control", "identity", []string{"accounts"})
	workload := filepath.Join(
		root,
		"clusters/codefly/deployments/modules/identity/services/accounts/overlays/local/workload.yaml",
	)
	writeTestFile(t, workload, `apiVersion: v1
kind: Secret
metadata:
  name: leaked
stringData:
  password: hostile-canary
`)
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "render secret")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = head
	workspace.Environments = workspace.Environments[:1]
	err = generateGitOps(context.Background(), moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), "Kubernetes Secret") {
		t.Fatalf("generateGitOps() error = %v, want immutable Secret rejection", err)
	}
}

func TestGenerateGitOpsRejectsManagedServiceWorkloadInAWS(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "aws-control", "identity", []string{"accounts", "store"})
	localOverlay := filepath.Join(
		root,
		"clusters/codefly/deployments/modules/identity/services/store/overlays/local",
	)
	awsOverlay := filepath.Join(
		root,
		"clusters/codefly/deployments/modules/identity/services/store/overlays/aws",
	)
	if err := copyTree(localOverlay, awsOverlay, "", false); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "render managed workload")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = head
	workspace.Environments = workspace.Environments[1:]
	err = generateGitOps(context.Background(), moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), "unexpected in-cluster overlay") {
		t.Fatalf("generateGitOps() error = %v, want managed workload rejection", err)
	}
}

func TestGenerateGitOpsRejectsMissingManagedServicePath(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "aws-control", "identity", []string{"accounts", "store"})
	serviceRoot := filepath.Join(
		root,
		"clusters/codefly/deployments/modules/identity/services/store",
	)
	if err := os.RemoveAll(serviceRoot); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "remove managed service")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = head
	workspace.Environments = workspace.Environments[1:]

	err = generateGitOps(context.Background(), moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), `missing service path "store"`) {
		t.Fatalf("generateGitOps() error = %v, want missing managed service path rejection", err)
	}
}

func TestGeneratedPoliciesAndProjectMatchRenderedTopology(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(
		t,
		"policy-control",
		"identity",
		[]string{"accounts", "auth-sidecar", "store"},
	)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "overlays")
	localNetwork, err := os.ReadFile(filepath.Join(generated, "local", "resources", "network-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{
		"allow-istio-ingress-to-auth-sidecar",
		"allow-accounts-to-store",
		"allow-store-from-accounts",
		"allow-auth-sidecar-public-egress",
	} {
		if !strings.Contains(string(localNetwork), "name: "+policy) {
			t.Errorf("local network policy is missing %q", policy)
		}
	}
	awsNetwork, err := os.ReadFile(filepath.Join(generated, "aws", "resources", "network-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(awsNetwork), "cidr: 10.42.0.0/24") ||
		!strings.Contains(string(awsNetwork), "name: allow-accounts-to-store") {
		t.Errorf("AWS network policy does not bind the managed store CIDR:\n%s", awsNetwork)
	}
	for _, file := range []string{
		filepath.Join(generated, "local", "resources", "istio-gateway.yaml"),
		filepath.Join(generated, "local", "resources", "istio-mtls.yaml"),
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "kind: Gateway") &&
			!strings.Contains(string(data), "name: allow-istio-ingress-to-auth-sidecar") {
			t.Errorf("%s does not contain the ingress boundary", file)
		}
	}
	gatewayObjects, err := decodeYAMLDocuments(filepath.Join(generated, "local", "resources", "istio-gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var gatewayName string
	var routeGateways []any
	for _, object := range gatewayObjects {
		metadata := object["metadata"].(map[string]any)
		switch object["kind"] {
		case "Gateway":
			gatewayName, _ = metadata["name"].(string)
		case "VirtualService":
			spec := object["spec"].(map[string]any)
			routeGateways, _ = spec["gateways"].([]any)
		}
	}
	if gatewayName != "identity" || len(routeGateways) != 1 || routeGateways[0] != "identity" {
		t.Errorf("generated Gateway %q and route gateways %#v do not match the module-owned route contract", gatewayName, routeGateways)
	}
	projectData, err := os.ReadFile(filepath.Join(generated, "local", "resources", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var project map[string]any
	if err := yaml.Unmarshal(projectData, &project); err != nil {
		t.Fatal(err)
	}
	spec := project["spec"].(map[string]any)
	if _, exists := spec["clusterResourceWhitelist"]; exists {
		t.Fatal("AppProject grants cluster-scoped authority even though child Applications create no cluster resources")
	}
	whitelist := spec["namespaceResourceWhitelist"].([]any)
	if len(whitelist) != 2 {
		t.Fatalf("namespace allowlist = %#v, want exact Deployment and Service kinds", whitelist)
	}
	encoded := string(projectData)
	for _, kind := range []string{"Deployment", "Service"} {
		if !strings.Contains(encoded, "kind: "+kind) {
			t.Errorf("AppProject allowlist is missing rendered kind %s", kind)
		}
	}
	if strings.Contains(encoded, "StatefulSet") || strings.Contains(encoded, "ConfigMap") {
		t.Errorf("AppProject contains resources absent from the immutable render:\n%s", encoded)
	}
}

func TestGeneratedMarketingIngressUsesExactEnvironmentRoutes(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(
		t,
		"website-control",
		"identity",
		[]string{"auth-sidecar", "marketing"},
	)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Environments[0].Ingress = []environmentIngressRoute{
		{
			Name:     "marketing",
			Service:  "marketing",
			Endpoint: "http",
			Hosts:    []string{"identity.localhost", "www.identity.localhost", "docs.identity.localhost"},
		},
		{
			Name:     "product",
			Service:  "auth-sidecar",
			Endpoint: "http",
			Hosts:    []string{"app.identity.localhost", "localhost"},
		},
	}
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}

	assertEnvironmentApplications(t, moduleDir, "local", []string{"auth-sidecar", "marketing"})
	assertEnvironmentApplications(t, moduleDir, "aws", []string{"auth-sidecar", "marketing"})

	generated := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "overlays", "local", "resources")
	gatewayObjects, err := decodeYAMLDocuments(filepath.Join(generated, "istio-gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var virtualService map[string]any
	for _, object := range gatewayObjects {
		if object["kind"] == "VirtualService" {
			virtualService = object
			break
		}
	}
	if virtualService == nil {
		t.Fatal("generated ingress is missing its VirtualService")
	}
	spec := virtualService["spec"].(map[string]any)
	hosts := spec["hosts"].([]any)
	for _, host := range []string{
		"app.identity.localhost",
		"docs.identity.localhost",
		"identity.localhost",
		"localhost",
		"www.identity.localhost",
	} {
		if !slices.Contains(hosts, any(host)) {
			t.Errorf("VirtualService hosts %v do not include %q", hosts, host)
		}
	}
	if slices.Contains(hosts, any("*")) {
		t.Fatalf("VirtualService retains wildcard authority: %v", hosts)
	}

	routes := make(map[string]map[string]any)
	for _, value := range spec["http"].([]any) {
		route := value.(map[string]any)
		routes[route["name"].(string)] = route
	}
	assertRoute := func(name, regex, host string, port int) {
		t.Helper()
		route, exists := routes[name]
		if !exists {
			t.Fatalf("VirtualService route %q is missing", name)
		}
		matches := route["match"].([]any)
		found := false
		for _, value := range matches {
			match := value.(map[string]any)
			authority := match["authority"].(map[string]any)
			if authority["regex"] == regex {
				found = true
			}
		}
		if !found {
			t.Errorf("VirtualService route %q does not match %q", name, regex)
		}
		destinations := route["route"].([]any)
		destination := destinations[0].(map[string]any)["destination"].(map[string]any)
		if destination["host"] != host {
			t.Errorf("VirtualService route %q host = %v, want %s", name, destination["host"], host)
		}
		renderedPort := destination["port"].(map[string]any)["number"]
		if renderedPort != port {
			t.Errorf("VirtualService route %q port = %v, want %d", name, renderedPort, port)
		}
	}
	assertRoute(
		"marketing",
		`^www\.identity\.localhost(:[0-9]+)?$`,
		"marketing.identity-local.svc.cluster.local",
		3000,
	)
	assertRoute(
		"product",
		`^app\.identity\.localhost(:[0-9]+)?$`,
		"auth-sidecar.identity-local.svc.cluster.local",
		8080,
	)

	authorizationObjects, err := decodeYAMLDocuments(filepath.Join(generated, "istio-mtls.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	authorizedPorts := make(map[string][]any)
	for _, object := range authorizationObjects {
		if object["kind"] != "AuthorizationPolicy" {
			continue
		}
		metadata := object["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		if !strings.HasPrefix(name, "allow-istio-ingress-to-") {
			continue
		}
		policySpec := object["spec"].(map[string]any)
		rules := policySpec["rules"].([]any)
		to := rules[0].(map[string]any)["to"].([]any)
		operation := to[0].(map[string]any)["operation"].(map[string]any)
		authorizedPorts[name] = operation["ports"].([]any)
	}
	if got := authorizedPorts["allow-istio-ingress-to-marketing"]; !slices.Equal(got, []any{"3000"}) {
		t.Errorf("marketing ingress ports = %v, want [3000]", got)
	}
	if got := authorizedPorts["allow-istio-ingress-to-auth-sidecar"]; !slices.Equal(got, []any{"8080"}) {
		t.Errorf("product ingress ports = %v, want [8080]", got)
	}
}

func TestManagedHandoffUsesExternalReferencesWithoutSecretValues(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "handoff-control", "identity", []string{"accounts", "store"})
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Environments[1].ManagedServices["store"] = managedServiceConfig{
		Kind:         "rds-postgresql",
		ExternalName: "db.internal.example.com",
		EgressCIDRs:  []string{"10.42.0.0/24"},
		SecretReferences: []managedSecretReference{{
			Name:      "store-runtime",
			RemoteKey: "products/identity/store",
			SecretStore: secretStoreRef{
				Name: "aws-secrets-manager",
				Kind: "ClusterSecretStore",
			},
		}},
	}
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(
		moduleDir,
		filepath.FromSlash(gitOpsRelativeDir),
		"overlays/aws/resources/handoffs/store.yaml",
	)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(data)
	for _, expected := range []string{
		"kind: Service",
		"type: ExternalName",
		"externalName: db.internal.example.com",
		"kind: ExternalSecret",
		"key: products/identity/store",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("managed handoff is missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "kind: Secret\n") || strings.Contains(rendered, "stringData:") {
		t.Errorf("managed handoff emitted secret values:\n%s", rendered)
	}
}

func TestReplaceGeneratedTreeRestoresPreviousTreeOnInstallFailure(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "kustomize")
	writeTestFile(t, filepath.Join(root, "inventory.json"), "previous\n")

	err := replaceGeneratedTree(filepath.Join(parent, "missing-stage"), root)
	if err == nil || !strings.Contains(err.Error(), "install generated GitOps directory") {
		t.Fatalf("replaceGeneratedTree() error = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "inventory.json"))
	if readErr != nil {
		t.Fatalf("previous tree was not restored: %v", readErr)
	}
	if string(data) != "previous\n" {
		t.Fatalf("restored inventory = %q", data)
	}
}

func TestLocalRevisionBindsCheckedOutHarnessSnapshot(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "local-control", "identity", []string{"accounts"})
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = "main"
	workspace.Environments = workspace.Environments[:1]
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory gitOpsInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Environments[0].Revision != head {
		t.Fatalf("local revision = %s, want harness snapshot %s", inventory.Environments[0].Revision, head)
	}
}

func TestLocalFullSHARejectsStaleHarnessSnapshot(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "stale-control", "identity", []string{"accounts"})
	stale := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeTestFile(t, filepath.Join(root, "snapshot-marker"), "new snapshot\n")
	runGit(t, root, "add", "snapshot-marker")
	runGit(t, root, "commit", "-m", "advance harness")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = stale
	workspace.Environments = workspace.Environments[:1]
	err = generateGitOps(context.Background(), moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), "not the harness snapshot "+head) {
		t.Fatalf("generateGitOps() error = %v, want stale local SHA rejection", err)
	}
}

func TestRevisionMustContainEveryApplicationPath(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "path-control", "identity", []string{"accounts"})
	overlay := filepath.Join(
		root,
		"clusters/codefly/deployments/modules/identity/services/accounts/overlays/aws",
	)
	if err := os.RemoveAll(overlay); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-m", "remove immutable application path")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = head
	workspace.Environments = workspace.Environments[1:]
	err = generateGitOps(context.Background(), moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), "immutable overlay is missing") {
		t.Fatalf("generateGitOps() error = %v, want missing committed path rejection", err)
	}
}

func TestGitOpsCheckoutMustMatchDeclaredRepository(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "repo-control", "identity", []string{"accounts"})
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.RepoURL = "git@github.com:acme/other-config.git"
	err = generateGitOps(context.Background(), moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), "origin does not match") {
		t.Fatalf("generateGitOps() error = %v, want repository checkout mismatch", err)
	}
}

func TestRevisionResolvesInExplicitGitOpsCheckout(t *testing.T) {
	t.Parallel()
	checkout, moduleDir := writeGitOpsFixture(t, "split-control", "identity", []string{"accounts"})
	workspace, err := loadWorkspaceManifest(checkout)
	if err != nil {
		t.Fatal(err)
	}
	workspace.root = t.TempDir()
	workspace.Gitops.Checkout = checkout
	workspace.Environments = workspace.Environments[1:]
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatalf("generateGitOps() with a separate declared checkout: %v", err)
	}
}

func TestProductionSignedTagResolvesImmutableCommit(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeGitOpsFixture(t, "production-control", "identity", []string{"accounts"})
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	signingKey := filepath.Join(root, "test-signing-key")
	command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", signingKey)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate signing key: %v\n%s", err, output)
	}
	publicKey, err := os.ReadFile(signingKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowedSigners := filepath.Join(root, "allowed-signers")
	writeTestFile(t, allowedSigners, "test@codefly.dev "+string(publicKey))
	runGit(t, root, "config", "gpg.format", "ssh")
	runGit(t, root, "config", "gpg.ssh.program", "ssh-keygen")
	runGit(t, root, "config", "user.signingkey", signingKey)
	runGit(t, root, "config", "gpg.ssh.allowedSignersFile", allowedSigners)
	runGit(t, root, "tag", "-s", "v1.0.0", "-m", "signed release")

	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Gitops.Revision = "refs/tags/v1.0.0"
	workspace.Environments = workspace.Environments[1:]
	if err := generateGitOps(context.Background(), moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory gitOpsInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Environments[0].Revision != head {
		t.Fatalf("production revision = %s, want signed tag commit %s", inventory.Environments[0].Revision, head)
	}
}

func TestCreateDoesNotCopyPlaceholderGitOpsTree(t *testing.T) {
	root, target := writeGitOpsFixture(t, "create-control", "identity", []string{"accounts"})
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, moduleYamlPath), `kind: module
name: saas-starter
services:
  - name: accounts
  - name: auth-sidecar
`)
	if err := os.MkdirAll(filepath.Join(source, "services", "accounts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "services", "auth-sidecar"), 0o755); err != nil {
		t.Fatal(err)
	}
	topology := filepath.Join(target, "deployment", "topology.bindings.codefly.yaml")
	writeTestFile(t, topology, `version: v1
module:
  name: saas-starter
  namespace: saas-starter
  service_entry: accounts
  description: test
interface:
  - service: accounts
    endpoint: http
    visibility: public
services:
  - name: accounts
    endpoints:
      - name: http
        api: http
        visibility: public
        port: 8080
  - name: auth-sidecar
    endpoints:
      - name: http
        api: http
        visibility: public
        port: 8080
`)
	writeTestFile(t, filepath.Join(target, "deployment", "generated", "service-topology.json"), `{"module":"saas-starter"}`)
	writeTestFile(t, filepath.Join(target, "deployment", "generated", "accounts-routes.virtualservice.yaml"), `apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: saas-starter-accounts-catalog
  namespace: saas-starter
spec:
  gateways:
    - saas-starter
  http:
    - route:
        - destination:
            host: accounts.saas-starter.svc.cluster.local
            port:
              number: 8080
`)
	writeTestFile(t, filepath.Join(target, "deployment", "generated", "consumer-artifact.json"), `{"owner":"consumer"}`)
	writeTestFile(t, filepath.Join(source, filepath.FromSlash(gitOpsRelativeDir), "overlays", "local", "application.yaml"), `repoURL: REPLACE_ME
targetRevision: main
path: deployments/modules/saas-starter/services/accounts/overlays/local
`)
	t.Setenv(sourceEnvVar, source)

	if err := Create(context.Background(), target, "identity"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, moduleYamlPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "name: saas-starter") || !strings.Contains(string(data), "name: identity") {
		t.Fatalf("module name was not structurally rewritten:\n%s", data)
	}
	if strings.Contains(string(data), "auth-sidecar") {
		t.Fatalf("source service inventory replaced the consumer inventory:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(target, "services", "auth-sidecar")); !os.IsNotExist(err) {
		t.Fatalf("undeclared source service was copied: %v", err)
	}
	for _, file := range []string{
		topology,
		filepath.Join(target, "deployment", "generated", "service-topology.json"),
		filepath.Join(target, "deployment", "generated", "accounts-routes.virtualservice.yaml"),
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "saas-starter") || strings.Contains(string(data), "auth-sidecar") {
			t.Fatalf("deployment metadata retained starter inventory in %s:\n%s", file, data)
		}
	}
	if data, err := os.ReadFile(filepath.Join(target, "deployment", "generated", "consumer-artifact.json")); err != nil ||
		string(data) != `{"owner":"consumer"}` {
		t.Fatalf("consumer generated artifact was not preserved: data=%q error=%v", data, err)
	}
	gitOpsRoot := filepath.Join(target, filepath.FromSlash(gitOpsRelativeDir))
	if _, err := os.Stat(filepath.Join(gitOpsRoot, "overlays", "local", "application.yaml")); !os.IsNotExist(err) {
		t.Fatalf("placeholder Application was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, workspaceYamlPath)); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeGeneratedGatewayRouteRejectsMalformedDocument(t *testing.T) {
	generatedRoot := t.TempDir()
	writeTestFile(t, filepath.Join(generatedRoot, "accounts-routes.virtualservice.yaml"), `apiVersion: networking.istio.io/v1
kind: VirtualService
metadata: invalid
spec: invalid
`)
	topology := deploymentTopology{
		Module: topologyModule{
			Name:         "identity",
			Namespace:    "identity",
			ServiceEntry: "accounts",
		},
		Services: []topologyService{{Name: "accounts"}},
	}

	err := normalizeGeneratedGatewayRoute(generatedRoot, topology, "saas-starter", "saas-starter")
	if err == nil || !strings.Contains(err.Error(), "metadata must be an object") {
		t.Fatalf("normalize malformed gateway route error = %v", err)
	}
}

func TestServiceInventoryAcceptsAbsoluteMonorepoPath(t *testing.T) {
	t.Parallel()
	moduleDir := t.TempDir()
	external := filepath.Join(t.TempDir(), "accounts")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	services, err := validateServiceInventory(moduleDir, []serviceReference{{Name: "accounts", Path: &external}})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].directory != external {
		t.Fatalf("resolved services = %#v, want absolute path %q", services, external)
	}
}

func TestCreatePreservesAbsoluteMonorepoServicePath(t *testing.T) {
	root, target := writeGitOpsFixture(t, "monorepo-control", "identity", []string{"accounts"})
	external := filepath.Join(t.TempDir(), "accounts")
	writeTestFile(t, filepath.Join(external, "consumer.txt"), "consumer-owned\n")
	if err := os.RemoveAll(filepath.Join(target, "services")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(target, moduleYamlPath), fmt.Sprintf(`kind: module
name: identity
service-entry: accounts
services:
  - name: accounts
    path: %s
`, external))
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, moduleYamlPath), `kind: module
name: saas-starter
service-entry: accounts
services:
  - name: accounts
`)
	writeTestFile(t, filepath.Join(source, "services", "accounts", "canonical.txt"), "must not escape staging\n")
	t.Setenv(sourceEnvVar, source)

	if err := Create(context.Background(), target, "identity"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "services", "accounts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source service was copied to the default path despite the absolute override: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(external, "consumer.txt")); err != nil || string(data) != "consumer-owned\n" {
		t.Fatalf("external service changed: data=%q error=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, workspaceYamlPath)); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalSourceDoesNotShipConsumerOwnedGitOpsTree(t *testing.T) {
	t.Parallel()
	root := filepath.Join("module", filepath.FromSlash(gitOpsRelativeDir))
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || entry.IsDir() {
			return err
		}
		t.Errorf("canonical source still contains consumer-owned GitOps file %s", file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeGitOpsFixture(t *testing.T, workspaceName, moduleName string, services []string) (string, string) {
	t.Helper()
	root := t.TempDir()
	moduleDir := filepath.Join(root, "modules", moduleName)
	entry := services[0]
	for _, candidate := range []string{"forge-edge", "auth-sidecar", "accounts", "frontend"} {
		if slices.Contains(services, candidate) {
			entry = candidate
			break
		}
	}
	var module strings.Builder
	module.WriteString("kind: module\nname: " + moduleName + "\nservice-entry: " + entry + "\nservices:\n")
	var topology strings.Builder
	topology.WriteString("version: v1\nmodule:\n  name: " + moduleName + "\n  namespace: " + moduleName + "\n  service_entry: " + entry + "\n  description: test\n")
	topology.WriteString("interface:\n  - service: " + entry + "\n    endpoint: http\n    visibility: public\n")
	if entry != "marketing" && slices.Contains(services, "marketing") {
		topology.WriteString("  - service: marketing\n    endpoint: http\n    visibility: public\n")
	}
	topology.WriteString("services:\n")
	for _, service := range services {
		port := 8080
		if service == "marketing" {
			port = 3000
		}
		module.WriteString("  - name: " + service + "\n")
		fmt.Fprintf(
			&topology,
			"  - name: %s\n    version: 0.0.0\n    endpoints:\n      - name: http\n        api: http\n        visibility: private\n        port: %d\n",
			service,
			port,
		)
		if service == entry {
			topology.WriteString("    public_egress_ports:\n      - 443\n")
		}
		if service == "accounts" && slices.Contains(services, "store") {
			topology.WriteString("    dependencies:\n      - service: store\n        endpoints:\n          - http\n")
		}
		if err := os.MkdirAll(filepath.Join(moduleDir, "services", service), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, environment := range []string{"local", "aws"} {
			if environment == "aws" {
				if _, managed := fixtureAWSManagedServices[service]; managed {
					continue
				}
			}
			overlay := filepath.Join(
				root,
				"clusters",
				"codefly",
				"deployments",
				"modules",
				moduleName,
				"services",
				service,
				"overlays",
				environment,
			)
			writeTestFile(t, filepath.Join(overlay, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: `+moduleName+`-`+environment+`
resources:
  - workload.yaml
`)
			writeTestFile(t, filepath.Join(overlay, "workload.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: `+service+`
spec:
  selector:
    matchLabels:
      app: `+service+`
  template:
    metadata:
      labels:
        app: `+service+`
    spec:
      containers:
        - name: service
          image: example.invalid/`+service+`:test
---
apiVersion: v1
kind: Service
metadata:
  name: `+service+`
spec:
  selector:
    app: `+service+`
  ports:
    - name: http
      port: `+fmt.Sprint(port)+`
`)
		}
	}
	writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), module.String())
	writeTestFile(t, filepath.Join(moduleDir, "deployment", "topology.bindings.codefly.yaml"), topology.String())
	workspacePrefix := "name: " + workspaceName + `
layout: modules
gitops:
  repo-url: git@github.com:acme/platform-config.git
  path: clusters/codefly
  branch: main
  revision: REVISION
environments:
  - name: local
    cluster:
      kind: k3d
    namespace: ` + moduleName + `-local
  - name: aws
    cluster:
      kind: eks
    namespace: ` + moduleName + `-aws
    managed-services:
`
	for _, service := range services {
		kind, managed := fixtureAWSManagedServices[service]
		if !managed {
			continue
		}
		workspacePrefix += "      " + service + ":\n" +
			"        kind: " + kind + "\n" +
			"        external-name: " + service + ".internal.example.com\n" +
			"        egress-cidrs:\n" +
			"          - 10.42.0.0/24\n"
	}
	writeTestFile(t, filepath.Join(root, workspaceYamlPath), workspacePrefix)
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.name", "Codefly Test")
	runGit(t, root, "config", "user.email", "test@codefly.dev")
	runGit(t, root, "config", "commit.gpgsign", "false")
	runGit(t, root, "remote", "add", "origin", "git@github.com:acme/platform-config.git")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "immutable GitOps snapshot")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeTestFile(t, filepath.Join(root, workspaceYamlPath), strings.Replace(workspacePrefix, "REVISION", head, 1))
	return root, moduleDir
}

func assertEnvironmentApplications(t *testing.T, moduleDir, environment string, want []string) {
	t.Helper()
	root := filepath.Join(moduleDir, filepath.FromSlash(gitOpsRelativeDir), "overlays", environment)
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatalf("kubectl is required to prove the generated Kustomize overlay renders: %v", err)
	}
	command := exec.Command(kubectl, "kustomize", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("kubectl kustomize %s: %v\n%s", environment, err, output)
	}
	objects, err := renderKustomization(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, object := range objects {
		if object["kind"] != "Application" {
			continue
		}
		metadata := object["metadata"].(map[string]any)
		labels := metadata["labels"].(map[string]any)
		got = append(got, labels["codefly.dev/service"].(string))
	}
	slices.Sort(got)
	want = append([]string(nil), want...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s Applications = %v, want %v", environment, got, want)
	}
}

func writeTestFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
