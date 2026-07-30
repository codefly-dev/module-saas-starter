package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var fixtureAWSManagedServices = map[string]string{
	"cache":          "elasticache",
	"object-storage": "s3",
	"store":          "rds-postgresql",
	"vault":          "secrets-manager",
}

func TestGenerateBundleGoldenShapes(t *testing.T) {
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
			golden:    "testdata/warden-bundle.golden.json",
		},
		{
			name:      "mind",
			workspace: "mind-control",
			module:    "users",
			services:  []string{"store", "vault", "accounts", "cache", "frontend", "forge-edge", "object-storage"},
			golden:    "testdata/mind-bundle.golden.json",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, moduleDir := writeModuleFixture(t, test.workspace, test.module, test.services)
			workspace, err := loadWorkspaceManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir), "bundle.json"))
			if err != nil {
				t.Fatal(err)
			}
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(test.golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(test.golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("bundle golden mismatch\nwant:\n%s\ngot:\n%s", want, got)
			}

			// Both declared environments must render module-owned overlays.
			for _, environment := range []string{"local", "aws"} {
				overlayObjects(t, moduleDir, environment)
			}
		})
	}
}

func TestGeneratedBundleCarriesNoGitOrArgoTransport(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(t, "neutral-control", "identity", []string{"accounts", "store"})
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir))
	forbidden := []string{"repoURL", "targetRevision", "AppProject", "argoproj.io", "fluxcd.io", "sourceRepos"}
	err = filepath.WalkDir(generated, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				t.Errorf("generated file %s contains repository/Argo transport token %q", file, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var bundle moduleBundle
	data, err := os.ReadFile(filepath.Join(generated, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.SchemaVersion != moduleBundleSchema || bundle.Module != "identity" {
		t.Fatalf("bundle identity = %q/%q", bundle.SchemaVersion, bundle.Module)
	}
	if len(bundle.Environments) != 2 {
		t.Fatalf("bundle environments = %d, want local and aws", len(bundle.Environments))
	}
	aws := bundle.Environments[1]
	if aws.Name != "aws" || aws.Cluster != "eks" || aws.ResourcePath != "overlays/aws" {
		t.Fatalf("aws bundle environment = %#v", aws)
	}
	if len(aws.ManagedServiceHandoffs) != 1 || aws.ManagedServiceHandoffs[0].Service != "store" {
		t.Fatalf("aws managed handoffs = %#v", aws.ManagedServiceHandoffs)
	}
	if slices.Contains(aws.Services, "store") {
		t.Fatalf("managed store must not appear as an in-cluster workload: %v", aws.Services)
	}
}

func TestGenerateBundleIgnoresLegacyGitopsPublicationBlock(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(t, "legacy-control", "identity", []string{"accounts"})
	// A consumer workspace migrated from the previous release still carries the
	// CLI GitOps publication block. The plugin must parse the workspace and
	// ignore that block entirely rather than depend on any repository field.
	appendWorkspace(t, root, `
gitops:
  repo-url: git@github.com:acme/platform-config.git
  branch: main
  revision: 0123456789abcdef0123456789abcdef01234567
  inventory: clusters/codefly/deployments/modules/identity/.codefly-render.json
  environment: local
`)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateBundleRendersEnvironmentWithoutPublicIngress(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(t, "mixed-control", "identity", []string{"accounts", "store"})
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	// aws is a declared deploy target that has not configured public ingress.
	// A single incompletely-exposed environment must not fail the whole bundle
	// or the sibling environments that are fully configured.
	workspace.Environments[1].Ingress = nil
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatalf("generateDeploymentBundle() with an ingress-less environment: %v", err)
	}

	overlays := filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir), "overlays")
	if _, err := os.Stat(filepath.Join(overlays, "local", "resources", "istio-gateway.yaml")); err != nil {
		t.Fatalf("fully configured local overlay is missing its gateway: %v", err)
	}
	awsResources := filepath.Join(overlays, "aws", "resources")
	if _, err := os.Stat(filepath.Join(awsResources, "istio-gateway.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ingress-less aws overlay emitted a public gateway: %v", err)
	}
	for _, base := range []string{"namespace.yaml", "network-policy.yaml", "istio-mtls.yaml"} {
		if _, err := os.Stat(filepath.Join(awsResources, base)); err != nil {
			t.Errorf("ingress-less aws overlay is missing module-owned %s: %v", base, err)
		}
	}
	for _, object := range overlayObjects(t, moduleDir, "aws") {
		if kind := object["kind"]; kind == "Gateway" || kind == "VirtualService" {
			t.Fatalf("ingress-less aws overlay still emits %v", kind)
		}
	}

	var bundle moduleBundle
	data, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir), "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Environments) != 2 || bundle.Environments[1].Name != "aws" || len(bundle.Environments[1].Ingress) != 0 {
		t.Fatalf("bundle did not record the ingress-less aws environment: %#v", bundle.Environments)
	}
}

func TestGenerateBundleRejectsHostileContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(t *testing.T, moduleDir string, workspace *workspaceManifest)
		want   string
	}{
		{
			name: "no declared environments",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments = nil
			},
			want: "declares no deployment environments",
		},
		{
			name: "missing namespace",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments[0].Namespace = ""
			},
			want: "Kubernetes DNS label",
		},
		{
			name: "duplicate environment",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments[1].Name = "local"
			},
			want: "more than once",
		},
		{
			name: "wildcard ingress host",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
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
			name: "ingress targets private endpoint",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments[0].Ingress = []environmentIngressRoute{{
					Name:     "product",
					Service:  "frontend",
					Endpoint: "http",
					Hosts:    []string{"identity.localhost"},
				}}
			},
			want: "is not a public module interface",
		},
		{
			name: "unsupported cluster kind",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments[0].Cluster.Kind = "gke"
			},
			want: "is not supported",
		},
		{
			name: "managed service on in-cluster environment",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments[0].ManagedServices = map[string]managedServiceConfig{
					"store": {Kind: "rds-postgresql", ExternalName: "db.internal.example.com"},
				}
			},
			want: "managed services for an in-cluster environment",
		},
		{
			name: "unexpected managed service",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				workspace.Environments[1].ManagedServices["phantom"] = managedServiceConfig{
					Kind:         "rds-postgresql",
					ExternalName: "db.internal.example.com",
					EgressCIDRs:  []string{"10.42.0.0/24"},
				}
			},
			want: "unexpected managed service",
		},
		{
			name: "unsupported managed kind",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				config := workspace.Environments[1].ManagedServices["store"]
				config.Kind = "dynamodb"
				workspace.Environments[1].ManagedServices["store"] = config
			},
			want: "is not supported",
		},
		{
			name: "invalid managed egress cidr",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				config := workspace.Environments[1].ManagedServices["store"]
				config.EgressCIDRs = []string{"not-a-cidr"}
				workspace.Environments[1].ManagedServices["store"] = config
			},
			want: "egress CIDR",
		},
		{
			name: "incomplete managed secret reference",
			mutate: func(_ *testing.T, _ string, workspace *workspaceManifest) {
				config := workspace.Environments[1].ManagedServices["store"]
				config.SecretReferences = []managedSecretReference{{
					Name:        "store-runtime",
					RemoteKey:   "",
					SecretStore: secretStoreRef{Name: "aws", Kind: "ClusterSecretStore"},
				}}
				workspace.Environments[1].ManagedServices["store"] = config
			},
			want: "secret reference",
		},
		{
			name: "missing service path",
			mutate: func(t *testing.T, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(moduleDir, "services", "frontend")); err != nil {
					t.Fatal(err)
				}
			},
			want: "declared service \"frontend\" path",
		},
		{
			name: "extra service path",
			mutate: func(t *testing.T, moduleDir string, _ *workspaceManifest) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(moduleDir, "services", "phantom"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "has no module service declaration",
		},
		{
			name: "symlinked service path",
			mutate: func(t *testing.T, moduleDir string, _ *workspaceManifest) {
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
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, moduleDir := writeModuleFixture(t, "hostile-control", "identity", []string{"accounts", "frontend", "store"})
			workspace, err := loadWorkspaceManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, moduleDir, workspace)
			err = generateDeploymentBundle(moduleDir, workspace)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generateDeploymentBundle() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestGeneratedPoliciesAndGatewayMatchTopology(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(
		t,
		"policy-control",
		"identity",
		[]string{"accounts", "auth-sidecar", "store"},
	)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir), "overlays")
	localNetwork, err := os.ReadFile(filepath.Join(generated, "local", "resources", "network-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{
		"allow-ambient-hbone-transport",
		"allow-istio-ingress-to-auth-sidecar",
		"allow-accounts-to-store",
		"allow-store-from-accounts",
		"allow-store-from-bootstrap",
		"allow-store-bootstrap-to-store",
		"allow-auth-sidecar-public-egress",
	} {
		if !strings.Contains(string(localNetwork), "name: "+policy) {
			t.Errorf("local network policy is missing %q", policy)
		}
	}
	if count := strings.Count(string(localNetwork), "port: 15008"); count != 2 {
		t.Errorf("local network policy has %d ambient HBONE transport exceptions, want 2", count)
	}
	if count := strings.Count(string(localNetwork), "codefly.dev/bootstrap-service: store"); count != 2 {
		t.Errorf("local network policy has %d bootstrap service selectors, want 2", count)
	}

	localIstio, err := os.ReadFile(filepath.Join(generated, "local", "resources", "istio-mtls.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"name: allow-store-from-internal-topology",
		"cluster.local/ns/identity-local/sa/default",
		`- "8080"`,
	} {
		if !strings.Contains(string(localIstio), expected) {
			t.Errorf("local Istio policy is missing %q", expected)
		}
	}
	awsIstio, err := os.ReadFile(filepath.Join(generated, "aws", "resources", "istio-mtls.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(awsIstio), "allow-store-from-internal-topology") {
		t.Error("AWS Istio policy grants internal authority to managed Postgres")
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

	// No AppProject / Application is emitted anywhere in the overlay.
	for _, environment := range []string{"local", "aws"} {
		objects := overlayObjects(t, moduleDir, environment)
		for _, object := range objects {
			if kind := object["kind"]; kind == "AppProject" || kind == "Application" {
				t.Fatalf("%s overlay still emits Argo %v", environment, kind)
			}
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
	if strings.Contains(string(awsNetwork), "allow-store-from-bootstrap") ||
		strings.Contains(string(awsNetwork), "allow-store-bootstrap-to-store") {
		t.Errorf("AWS network policy retains bootstrap authority for managed Postgres:\n%s", awsNetwork)
	}
}

func TestGenerateBundleRejectsDependencyWithoutEndpointAuthority(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(
		t,
		"policy-control",
		"identity",
		[]string{"accounts", "store"},
	)
	topologyFile := filepath.Join(moduleDir, "deployment", "topology.bindings.codefly.yaml")
	data, err := os.ReadFile(topologyFile)
	if err != nil {
		t.Fatal(err)
	}
	topology := strings.Replace(
		string(data),
		"        endpoints:\n          - http\n",
		"        endpoints: []\n",
		1,
	)
	if topology == string(data) {
		t.Fatal("fixture topology has no dependency endpoint block")
	}
	writeTestFile(t, topologyFile, topology)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	err = generateDeploymentBundle(moduleDir, workspace)
	if err == nil || !strings.Contains(err.Error(), `dependency "store" declares no endpoints`) {
		t.Fatalf("generateDeploymentBundle() error = %v, want empty dependency authority rejection", err)
	}
}

func TestGeneratedMarketingIngressUsesExactEnvironmentRoutes(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(
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
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}

	generated := filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir), "overlays", "local", "resources")
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

func TestMindRenderUsesExactServiceGraphRoutesAndPolicies(t *testing.T) {
	t.Parallel()
	services := []string{"accounts", "cache", "forge-edge", "frontend", "object-storage", "store", "vault"}
	root, moduleDir := writeModuleFixture(t, "mind-control", "users", services)
	writeMindTopology(t, moduleDir)
	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Environments[0].Ingress = []environmentIngressRoute{{
		Name: "product", Service: "forge-edge", Endpoint: "rest",
		Hosts: []string{"app.mind.localhost"},
	}}
	workspace.Environments[1].Ingress = []environmentIngressRoute{{
		Name: "product", Service: "forge-edge", Endpoint: "rest",
		Hosts: []string{"app.mind.example.com"},
	}}

	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	assertMindRouteAndPolicies(t, moduleDir, "local", "app.mind.localhost", false)
	assertMindRouteAndPolicies(t, moduleDir, "aws", "app.mind.example.com", true)
}

func TestManagedHandoffUsesExternalReferencesWithoutSecretValues(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(t, "handoff-control", "identity", []string{"accounts", "store"})
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
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(
		moduleDir,
		filepath.FromSlash(bundleRelativeDir),
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
	writeTestFile(t, filepath.Join(root, "bundle.json"), "previous\n")

	err := replaceGeneratedTree(filepath.Join(parent, "missing-stage"), root)
	if err == nil || !strings.Contains(err.Error(), "install generated bundle directory") {
		t.Fatalf("replaceGeneratedTree() error = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "bundle.json"))
	if readErr != nil {
		t.Fatalf("previous tree was not restored: %v", readErr)
	}
	if string(data) != "previous\n" {
		t.Fatalf("restored bundle = %q", data)
	}
}

func TestGenerateBundleReplacesStaleArgoTree(t *testing.T) {
	t.Parallel()
	root, moduleDir := writeModuleFixture(t, "migration-control", "identity", []string{"accounts"})
	staleRoot := filepath.Join(moduleDir, filepath.FromSlash(bundleRelativeDir))
	// A tree generated by the previous release still carries Argo Applications,
	// an AppProject, a pinned repository revision, and even a leaked Secret.
	writeTestFile(t, filepath.Join(staleRoot, "overlays", "local", "applications", "accounts.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: identity-accounts-local
spec:
  source:
    repoURL: git@github.com:acme/platform-config.git
    targetRevision: 0123456789abcdef0123456789abcdef01234567
`)
	writeTestFile(t, filepath.Join(staleRoot, "overlays", "aws", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: leaked
stringData:
  password: hostile-canary
`)
	writeTestFile(t, filepath.Join(staleRoot, "inventory.json"), `{"repository":"git@github.com:acme/platform-config.git"}`)

	workspace, err := loadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generateDeploymentBundle(moduleDir, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(staleRoot, "inventory.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale inventory.json survived generation: %v", err)
	}
	err = filepath.WalkDir(staleRoot, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, token := range []string{"hostile-canary", "kind: Secret", "kind: Application", "repoURL", "targetRevision", "argoproj.io"} {
			if strings.Contains(string(data), token) {
				t.Fatalf("stale token %q survived in %s", token, file)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateGeneratesBundleAndPreservesConsumerFiles(t *testing.T) {
	root, target := writeModuleFixture(t, "create-control", "identity", []string{"accounts"})
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
	writeTestFile(t, filepath.Join(source, "services", "README.md"), "canonical service documentation\n")
	writeTestFile(t, filepath.Join(target, "services", "README.md"), "stale consumer copy\n")
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
	writeTestFile(t, filepath.Join(target, "deployment", "generated", "consumer-artifact.json"), `{"owner":"consumer"}`)
	// A stale plugin-owned tree from the previous release must be regenerated,
	// never carried forward.
	writeTestFile(t, filepath.Join(source, filepath.FromSlash(bundleRelativeDir), "overlays", "local", "application.yaml"), `repoURL: REPLACE_ME
targetRevision: main
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
	if data, err := os.ReadFile(filepath.Join(target, "services", "README.md")); err != nil ||
		string(data) != "canonical service documentation\n" {
		t.Fatalf("canonical service documentation was not refreshed: data=%q error=%v", data, err)
	}
	if topologyData, err := os.ReadFile(topology); err != nil ||
		strings.Contains(string(topologyData), "saas-starter") {
		t.Fatalf("deployment topology retained starter identity: data=%q error=%v", topologyData, err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "deployment", "generated", "consumer-artifact.json")); err != nil ||
		string(data) != `{"owner":"consumer"}` {
		t.Fatalf("consumer generated artifact was not preserved: data=%q error=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(target, "deployment", "generated", "accounts-routes.virtualservice.yaml")); !os.IsNotExist(err) {
		t.Fatalf("transport-specific route artifact was copied: %v", err)
	}
	bundleRoot := filepath.Join(target, filepath.FromSlash(bundleRelativeDir))
	if _, err := os.Stat(filepath.Join(bundleRoot, "overlays", "local", "application.yaml")); !os.IsNotExist(err) {
		t.Fatalf("placeholder Application was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, "bundle.json")); err != nil {
		t.Fatalf("generated bundle manifest is missing: %v", err)
	}
	overlayObjects(t, target, "local")
	if _, err := os.Stat(filepath.Join(root, workspaceYamlPath)); err != nil {
		t.Fatal(err)
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
	root, target := writeModuleFixture(t, "monorepo-control", "identity", []string{"accounts"})
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

func TestCanonicalSourceDoesNotShipGeneratedBundleTree(t *testing.T) {
	t.Parallel()
	root := filepath.Join("module", filepath.FromSlash(bundleRelativeDir))
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || entry.IsDir() {
			return err
		}
		t.Errorf("canonical source still contains consumer-owned bundle file %s", file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- fixtures ------------------------------------------------------------

func overlayObjects(t *testing.T, moduleDir, environment string) []map[string]any {
	t.Helper()
	objects, err := renderKustomization(filepath.Join(
		moduleDir,
		filepath.FromSlash(bundleRelativeDir),
		"overlays",
		environment,
	))
	if err != nil {
		t.Fatalf("render %s overlay: %v", environment, err)
	}
	return objects
}

func writeModuleFixture(t *testing.T, workspaceName, moduleName string, services []string) (string, string) {
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
		if service == "store" {
			topology.WriteString("    bootstrap_job_endpoints:\n      - http\n")
		}
		if service == entry {
			topology.WriteString("    public_egress_ports:\n      - 443\n")
		}
		if service == "accounts" && slices.Contains(services, "store") {
			topology.WriteString("    dependencies:\n      - service: store\n        endpoints:\n          - http\n")
		}
		if err := os.MkdirAll(filepath.Join(moduleDir, "services", service), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(moduleDir, moduleYamlPath), module.String())
	writeTestFile(t, filepath.Join(moduleDir, "deployment", "topology.bindings.codefly.yaml"), topology.String())
	workspace := "name: " + workspaceName + `
layout: modules
environments:
  - name: local
    cluster:
      kind: k3d
    namespace: ` + moduleName + `-local
    ingress:
      - name: product
        service: ` + entry + `
        endpoint: http
        hosts: [` + moduleName + `.localhost]
  - name: aws
    cluster:
      kind: eks
    namespace: ` + moduleName + `-aws
    ingress:
      - name: product
        service: ` + entry + `
        endpoint: http
        hosts: [` + moduleName + `.example.com]
    managed-services:
`
	for _, service := range services {
		kind, managed := fixtureAWSManagedServices[service]
		if !managed {
			continue
		}
		workspace += "      " + service + ":\n" +
			"        kind: " + kind + "\n" +
			"        external-name: " + service + ".internal.example.com\n" +
			"        egress-cidrs:\n" +
			"          - 10.42.0.0/24\n"
	}
	writeTestFile(t, filepath.Join(root, workspaceYamlPath), workspace)
	return root, moduleDir
}

func appendWorkspace(t *testing.T, root, extra string) {
	t.Helper()
	file := filepath.Join(root, workspaceYamlPath)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, []byte(extra)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMindTopology(t *testing.T, moduleDir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(moduleDir, "deployment", "topology.bindings.codefly.yaml"), `version: v1
module:
  name: users
  namespace: users
  service_entry: forge-edge
  description: Mind users boundary
interface:
  - service: forge-edge
    endpoint: rest
    visibility: public
services:
  - name: accounts
    endpoints:
      - name: grpc
        api: grpc
        visibility: private
        port: 8080
    dependencies:
      - service: cache
        endpoints: [redis]
      - service: object-storage
        endpoints: [http]
      - service: store
        endpoints: [postgres]
      - service: vault
        endpoints: [http]
  - name: cache
    endpoints:
      - name: redis
        api: redis
        visibility: private
        port: 6379
  - name: forge-edge
    endpoints:
      - name: rest
        api: http
        visibility: public
        port: 8080
    dependencies:
      - service: accounts
        endpoints: [grpc]
    public_egress_ports: [443]
  - name: frontend
    endpoints:
      - name: http
        api: http
        visibility: private
        port: 3000
    dependencies:
      - service: forge-edge
        endpoints: [rest]
  - name: object-storage
    endpoints:
      - name: http
        api: http
        visibility: private
        port: 9000
  - name: store
    endpoints:
      - name: postgres
        api: postgres
        visibility: private
        port: 5432
  - name: vault
    endpoints:
      - name: http
        api: http
        visibility: private
        port: 8200
`)
}

func assertMindRouteAndPolicies(
	t *testing.T,
	moduleDir,
	environment,
	host string,
	aws bool,
) {
	t.Helper()
	resources := filepath.Join(
		moduleDir,
		filepath.FromSlash(bundleRelativeDir),
		"overlays",
		environment,
		"resources",
	)
	objects, err := decodeYAMLDocuments(filepath.Join(resources, "istio-gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var route map[string]any
	var destinationRuleHosts []string
	for _, object := range objects {
		switch object["kind"] {
		case "VirtualService":
			route = object
		case "DestinationRule":
			spec := object["spec"].(map[string]any)
			ruleHost := spec["host"].(string)
			if strings.Contains(ruleHost, "*") {
				t.Errorf("%s DestinationRule retains wildcard authority %q", environment, ruleHost)
			}
			destinationRuleHosts = append(destinationRuleHosts, ruleHost)
		}
	}
	if route == nil {
		t.Fatal("Mind bootstrap has no VirtualService")
	}
	expectedServices := []string{"accounts", "cache", "forge-edge", "frontend", "object-storage", "store", "vault"}
	if aws {
		expectedServices = []string{"accounts", "forge-edge", "frontend"}
	}
	expectedRuleHosts := make([]string, 0, len(expectedServices))
	for _, service := range expectedServices {
		expectedRuleHosts = append(expectedRuleHosts, service+".users-"+environment+".svc.cluster.local")
	}
	if !slices.Equal(destinationRuleHosts, expectedRuleHosts) {
		t.Fatalf("%s DestinationRule hosts = %v, want %v", environment, destinationRuleHosts, expectedRuleHosts)
	}
	spec := route["spec"].(map[string]any)
	if got := spec["hosts"].([]any); !slices.Equal(got, []any{host}) {
		t.Fatalf("%s route hosts = %v, want [%s]", environment, got, host)
	}
	httpRoutes := spec["http"].([]any)
	destination := httpRoutes[0].(map[string]any)["route"].([]any)[0].(map[string]any)["destination"].(map[string]any)
	if destination["host"] != "forge-edge.users-"+environment+".svc.cluster.local" ||
		destination["port"].(map[string]any)["number"] != 8080 {
		t.Fatalf("%s route destination = %#v", environment, destination)
	}

	data, err := os.ReadFile(filepath.Join(resources, "network-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(data)
	for _, name := range []string{
		"default-deny-all",
		"allow-ambient-hbone-transport",
		"allow-istio-ingress-to-forge-edge",
		"allow-forge-edge-to-accounts",
		"allow-accounts-from-forge-edge",
		"allow-frontend-to-forge-edge",
		"allow-forge-edge-from-frontend",
	} {
		if !strings.Contains(rendered, "name: "+name) {
			t.Errorf("%s network policy is missing %q", environment, name)
		}
	}
	if count := strings.Count(rendered, "port: 15008"); count != 2 {
		t.Errorf("%s network policy has %d ambient HBONE transport exceptions, want 2", environment, count)
	}
	if aws {
		for _, name := range []string{
			"allow-accounts-to-cache",
			"allow-accounts-to-object-storage",
			"allow-accounts-to-store",
			"allow-accounts-to-vault",
		} {
			if !strings.Contains(rendered, "name: "+name) {
				t.Errorf("AWS network policy is missing managed exception %q", name)
			}
		}
		if !strings.Contains(rendered, "cidr: 10.42.0.0/24") {
			t.Error("AWS network policy has no exact managed-service CIDR")
		}
		return
	}
	for _, name := range []string{
		"allow-cache-from-accounts",
		"allow-object-storage-from-accounts",
		"allow-store-from-accounts",
		"allow-vault-from-accounts",
	} {
		if !strings.Contains(rendered, "name: "+name) {
			t.Errorf("local network policy is missing topology exception %q", name)
		}
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
