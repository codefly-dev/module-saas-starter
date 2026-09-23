package cataloggen_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

const moduleRoot = "../../../../../"

// readDeploymentDocuments loads the authored model — module.codefly.yaml and
// every service manifest it declares — exactly as the generator does.
func readDeploymentDocuments(t *testing.T) cataloggen.DeploymentDocuments {
	t.Helper()
	documents, err := cataloggen.LoadDeploymentDocuments(moduleRoot)
	require.NoError(t, err)
	return documents
}

// withService returns a copy of the documents with one service manifest
// rewritten; the replacement must actually match, or the fixture has drifted
// from the tree and the case would test nothing.
func withService(t *testing.T, documents cataloggen.DeploymentDocuments, service, old, replacement string) cataloggen.DeploymentDocuments {
	t.Helper()
	current := string(documents.Services[service])
	require.Contains(t, current, old, "service %s manifest no longer matches this fixture", service)
	return withServiceDocument(documents, service, []byte(strings.Replace(current, old, replacement, 1)))
}

func withServiceDocument(documents cataloggen.DeploymentDocuments, service string, document []byte) cataloggen.DeploymentDocuments {
	services := make(map[string][]byte, len(documents.Services)+1)
	for name, existing := range documents.Services {
		services[name] = existing
	}
	services[service] = document
	return cataloggen.DeploymentDocuments{Module: documents.Module, Services: services, Jobs: documents.Jobs}
}

func withModule(t *testing.T, documents cataloggen.DeploymentDocuments, old, replacement string) cataloggen.DeploymentDocuments {
	t.Helper()
	current := string(documents.Module)
	require.Contains(t, current, old, "module.codefly.yaml no longer matches this fixture")
	return cataloggen.DeploymentDocuments{Module: []byte(strings.Replace(current, old, replacement, 1)), Services: documents.Services, Jobs: documents.Jobs}
}

func TestDeploymentTopologyIsDeterministicAndCurrent(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	documents := readDeploymentDocuments(t)

	first, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, documents)
	require.NoError(t, err)
	second, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, documents)
	require.NoError(t, err)
	require.Equal(t, first.CatalogJSON, second.CatalogJSON)
	require.Equal(t, first.NetworkPolicy, second.NetworkPolicy)
	require.Equal(t, first.MeshPolicy, second.MeshPolicy)

	require.Equal(t, string(readFixture(t, moduleRoot+"deployment/generated/service-topology.json")), string(first.CatalogJSON), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "testdata/network-policy.golden.yaml")), string(first.NetworkPolicy), "run: go generate ./pkg/cataloggen")
	require.Equal(t, string(readFixture(t, "testdata/mesh-policy.golden.yaml")), string(first.MeshPolicy), "run: go generate ./pkg/cataloggen")

	require.Len(t, first.Catalog.GetServices(), 8)
	require.Len(t, first.Catalog.GetInterfaceEndpoints(), 5)
	require.Len(t, first.Catalog.GetPublicEgress(), 4)
	endpointCount, dependencyCount := 0, 0
	for _, service := range first.Catalog.GetServices() {
		endpointCount += len(service.GetEndpoints())
		dependencyCount += len(service.GetDependencies())
	}
	require.Equal(t, 12, endpointCount)
	require.Equal(t, 8, dependencyCount)
	// The accounts REST surface is reachable only through the gateway; the
	// gateway's REST surface is module-visible because composed modules and
	// solutions use it (Work Context minting, solution registration) and the
	// interface exports it to them.
	restVisibility := map[string]catalogv1.EndpointVisibility{
		"accounts":     catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_PRIVATE,
		"auth-gateway": catalogv1.EndpointVisibility_ENDPOINT_VISIBILITY_MODULE,
	}
	privateREST := map[string]bool{"accounts": false, "auth-gateway": false}
	authGatewayTelemetry := false
	for _, service := range first.Catalog.GetServices() {
		if _, ok := privateREST[service.GetName()]; !ok {
			continue
		}
		for _, endpoint := range service.GetEndpoints() {
			if endpoint.GetName() == "rest" {
				require.Equal(t, restVisibility[service.GetName()], endpoint.GetVisibility())
				privateREST[service.GetName()] = true
			}
		}
		if service.GetName() == "auth-gateway" {
			for _, dependency := range service.GetDependencies() {
				if dependency.GetService() == "telemetry" {
					require.Equal(t, []string{"grpc"}, dependency.GetEndpoints())
					authGatewayTelemetry = true
				}
			}
		}
	}
	require.Equal(t, map[string]bool{"accounts": true, "auth-gateway": true}, privateREST)
	require.True(t, authGatewayTelemetry)
	accountsConnectExposed, gatewayRESTExposed := false, false
	for _, endpoint := range first.Catalog.GetInterfaceEndpoints() {
		if endpoint.GetService() == "accounts" && endpoint.GetEndpoint() == "connect" {
			accountsConnectExposed = true
		}
		if endpoint.GetService() == "auth-gateway" && endpoint.GetEndpoint() == "rest" {
			gatewayRESTExposed = true
		}
		require.False(t, endpoint.GetService() == "accounts" && endpoint.GetEndpoint() != "connect")
	}
	require.True(t, accountsConnectExposed)
	require.True(t, gatewayRESTExposed)
	require.Equal(t, 20, strings.Count(string(first.NetworkPolicy), "\nkind: NetworkPolicy\n"))
	require.NotContains(t, string(first.NetworkPolicy), "allow-intra-namespace")
	for _, name := range []string{
		"allow-accounts-from-dependents", "allow-auth-gateway-from-dependents", "allow-auth-gateway-to-dependencies",
		"allow-frontend-to-dependencies", "allow-store-from-bootstrap", "allow-store-bootstrap-to-store",
		"allow-frontend-public-egress", "allow-marketing-public-egress", "allow-telemetry-from-dependents",
		"allow-telemetry-public-egress", "allow-istio-ingress-to-marketing", "allow-istio-ingress-to-frontend",
	} {
		require.Contains(t, string(first.NetworkPolicy), "name: "+name)
	}
	require.NotContains(t, string(first.NetworkPolicy), "name: allow-istio-ingress-to-auth-gateway")
	require.Equal(t, 2, strings.Count(string(first.NetworkPolicy), "codefly.dev/bootstrap-service: store"))
	require.Contains(t, string(first.NetworkPolicy), "192.175.48.0/24")
	require.Contains(t, string(first.NetworkPolicy), "64:ff9b::/96")
}

// The manifests are the model: a deployment fact lives in the service manifest
// it belongs to, under spec.deployment, and nowhere else. The generator refuses
// a manifest that omits it, names an endpoint it does not have, or carries a
// key the schema does not know — a misspelt key would otherwise render nothing
// and read as protection.
func TestDeploymentSpecIsStrictAndComplete(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	documents := readDeploymentDocuments(t)

	_, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "accounts", "    deployment:\n        endpoint-ports:\n", "    deployment:\n        unknown: true\n        endpoint-ports:\n"))
	require.ErrorContains(t, err, "field unknown not found")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "accounts", "            rest: 8080\n", ""))
	require.ErrorContains(t, err, `endpoint "rest" has no port`)

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "accounts", "            rest: 8080\n", "            rest: 8080\n            ghost: 1\n"))
	require.ErrorContains(t, err, `names unknown endpoint "ghost"`)

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "telemetry", "    deployment:\n", "    deployment-typo:\n"))
	require.ErrorContains(t, err, "has no spec.deployment block")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withServiceDocument(documents, "ghost", documents.Services["telemetry"]))
	require.ErrorContains(t, err, "not declared by module.codefly.yaml")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withModule(t, documents, "    - name: vault\n", ""))
	require.ErrorContains(t, err, "not declared by module.codefly.yaml")
}

func TestGeneratedCodeflyAndNetworkManifestsParseStrictly(t *testing.T) {
	ctx := context.Background()
	loadedModule, err := resources.LoadModuleFromDir(ctx, filepath.Clean("../../../../../"))
	require.NoError(t, err)
	require.NoError(t, loadedModule.ValidateInterface(ctx))
	loadedServices, err := loadedModule.LoadServices(ctx)
	require.NoError(t, err)
	require.Len(t, loadedServices, 8)

	moduleDocument := readFixture(t, "../../../../../module.codefly.yaml")
	var moduleEntry struct {
		ServiceEntry string `yaml:"service-entry"`
	}
	require.NoError(t, yaml.Unmarshal(moduleDocument, &moduleEntry))
	require.Equal(t, "frontend", moduleEntry.ServiceEntry)
	var module resources.Module
	require.NoError(t, yaml.Unmarshal(moduleDocument, &module))
	_, err = module.Proto(ctx)
	require.NoError(t, err)
	require.Len(t, module.ServiceReferences, 8)

	for _, reference := range module.ServiceReferences {
		document := readFixture(t, filepath.Join("../../../../../services", reference.Name, "service.codefly.yaml"))
		var service resources.Service
		require.NoError(t, yaml.Unmarshal(document, &service), reference.Name)
		require.Equal(t, reference.Name, service.Name)
		for _, endpoint := range service.Endpoints {
			endpoint.Module = module.Name
			endpoint.Service = service.Name
			if endpoint.Visibility == "" {
				endpoint.Visibility = resources.VisibilityPrivate
			}
			_, err := endpoint.Proto()
			require.NoError(t, err, "%s/%s", service.Name, endpoint.Name)
		}
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(readFixture(t, "testdata/network-policy.golden.yaml"))))
	names := make(map[string]bool)
	for {
		var document struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, "networking.k8s.io/v1", document.APIVersion)
		require.Equal(t, "NetworkPolicy", document.Kind)
		require.Equal(t, "saas-starter", document.Metadata.Namespace)
		require.NotEmpty(t, document.Spec)
		require.False(t, names[document.Metadata.Name], "duplicate NetworkPolicy %s", document.Metadata.Name)
		names[document.Metadata.Name] = true
	}
	require.Len(t, names, 20)
	require.True(t, names["allow-istio-ingress-to-marketing"])
	require.True(t, names["allow-istio-ingress-to-frontend"])
	require.False(t, names["allow-istio-ingress-to-auth-gateway"])
	require.True(t, names["allow-store-from-bootstrap"])
	require.True(t, names["allow-store-bootstrap-to-store"])
	require.True(t, names["allow-telemetry-from-dependents"])
	require.True(t, names["allow-telemetry-public-egress"])
}

func TestGeneratedMeshPolicyGatesInternalAuthorityByCallerIdentity(t *testing.T) {
	const ingressGatewaySA = "cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account"

	decoder := yaml.NewDecoder(strings.NewReader(string(readFixture(t, "testdata/mesh-policy.golden.yaml"))))
	strictMTLS := false
	allowFound := false
	waypointFound := false
	for {
		var document struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string            `yaml:"name"`
				Namespace string            `yaml:"namespace"`
				Labels    map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, "saas-starter", document.Metadata.Namespace)

		switch document.Kind {
		case "PeerAuthentication":
			require.Equal(t, "security.istio.io/v1", document.APIVersion)
			mtls, ok := document.Spec["mtls"].(map[string]any)
			require.True(t, ok, "PeerAuthentication must configure mtls")
			require.Equal(t, "STRICT", mtls["mode"])
			strictMTLS = true
		case "AuthorizationPolicy":
			require.Equal(t, "security.istio.io/v1", document.APIVersion)
			// Attachment, not selection. A selector routes an L7 policy to ztunnel,
			// which cannot evaluate one and fails safe by turning it into a blanket
			// DENY on the workload it selects. A targetRef to a Service both puts
			// the policy on the waypoint that can evaluate it and keeps it scoped to
			// one service rather than the namespace (the shape the GitOps baseline's
			// empty-ALLOW default-deny takes).
			require.NotContains(t, document.Spec, "selector",
				"an L7 policy attached by selector is enforced by ztunnel, which fails safe into a blanket deny")
			targetRefs, ok := document.Spec["targetRefs"].([]any)
			require.True(t, ok, "an L7 policy must attach to a waypoint by targetRef")
			require.Len(t, targetRefs, 1, "the gate scopes to exactly one service")
			targetRef, ok := targetRefs[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "Service", targetRef["kind"])
			require.Equal(t, "", targetRef["group"])

			if document.Metadata.Name == "deny-frontend-internal-http" {
				// A different resource gating a different surface: the authored HTTP
				// routes, not the catalog-derived procedures. Its rules are pinned by
				// TestMeshPolicyGatesInternalSurfacesByShape.
				require.Equal(t, "frontend", targetRef["name"])
				require.Equal(t, "DENY", document.Spec["action"])
				continue
			}
			require.Equal(t, "allow-accounts-internal-authority", document.Metadata.Name)
			require.Equal(t, "ALLOW", document.Spec["action"])
			require.Equal(t, "accounts", targetRef["name"],
				"only the catalog owner carries a generated reach gate")
			allowFound = true

			rule := document.Spec["rules"].([]any)[0].(map[string]any)
			source := rule["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
			principals := source["principals"].([]any)
			// Positive allowlist: only accounts' declared caller (auth-gateway),
			// named by its per-service SA. The shared sa/default (any namespace
			// pod) and the ingress gateway SA are NOT admitted — deny-by-default.
			require.Equal(t, []any{"cluster.local/ns/saas-starter/sa/auth-gateway"}, principals)
			require.NotContains(t, principals, "cluster.local/ns/saas-starter/sa/default")
			require.NotContains(t, principals, ingressGatewaySA)

			paths := rule["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)["paths"].([]any)
			require.Contains(t, paths, "/saas.accounts.v1.APIKeyService/ValidateAPIKey")
			require.Contains(t, paths, "/saas.accounts.v1.UsageService/ConsumeUsage")
			// EVERY gated path is a gRPC procedure, not only the two named above.
			// The frontend's token-gated HTTP routes are not gated here and cannot
			// be: this policy is derived from authz-methods.json. They are authored
			// instead, and carry their own deny. See DEPLOYMENT_TOPOLOGY.md,
			// "Cluster-internal HTTP routes".
			for _, path := range paths {
				require.True(t, strings.HasPrefix(path.(string), "/saas.accounts.v1."),
					"the gate lists gRPC procedures, not HTTP routes: %q", path)
			}
		case "Gateway":
			// The waypoint that makes the L7 allow enforceable in the ambient mesh.
			require.Equal(t, "gateway.networking.k8s.io/v1", document.APIVersion)
			require.Equal(t, "istio-waypoint", document.Spec["gatewayClassName"])
			require.Equal(t, "service", document.Metadata.Labels["istio.io/waypoint-for"])
			waypointFound = true
		default:
			t.Fatalf("unexpected mesh policy kind %q", document.Kind)
		}
	}
	require.True(t, strictMTLS, "namespace mTLS must be STRICT")
	require.True(t, allowFound, "internal-authority AuthorizationPolicy must be present")
	require.True(t, waypointFound, "L7 allow requires a waypoint to be enforced in the ambient mesh")
}

// TestMeshPolicyGatesInternalSurfacesByShape states the boundary the pin #570
// left behind (TestMeshPolicyGatesOnlyOwnerGRPCProcedures, since folded into
// TestGeneratedMeshPolicyGatesInternalAuthorityByCallerIdentity) was written to
// force: it required the mesh policy to carry exactly one gate, the catalog
// owner's gRPC procedures, and to fail the moment the renderer learned HTTP
// paths. It now has, so the boundary is stated by shape rather than by count.
// A catalog-derived gate lists gRPC procedures for the owner alone; an authored
// HTTP route is only ever subtracted from the port-wide ingress ALLOW by a DENY.
// Attachment is pinned by the walker above; what is pinned here is every rule of
// the deny, which is the half no other test reads.
func TestMeshPolicyGatesInternalSurfacesByShape(t *testing.T) {
	const meshIngressPrincipal = "cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account"

	// Every authored route, and the methods that carry internal authority on it.
	// A GET on /api/solutions/register is the sidebar's unauthenticated nav poll
	// and must stay reachable; a GET on /api/internal/solutions is the internal
	// detail read and must not.
	wantRules := map[string][]any{
		"/api/internal/solutions": {"GET"},
		"/api/solutions/register": {"DELETE", "POST"},
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(readFixture(t, "testdata/mesh-policy.golden.yaml"))))
	allows, denies := 0, 0
	for {
		var document struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if document.Kind != "AuthorizationPolicy" {
			continue
		}
		// Read defensively: a policy shape this test does not expect must fail it,
		// not panic out of the run and take the other assertions with it.
		rules, ok := document.Spec["rules"].([]any)
		require.True(t, ok, "policy %q carries no rules", document.Metadata.Name)

		switch document.Spec["action"] {
		case "ALLOW":
			allows++
			require.Equal(t, "allow-accounts-internal-authority", document.Metadata.Name)
			for _, rule := range rules {
				operation, ok := rule.(map[string]any)["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)
				require.True(t, ok)
				for _, path := range operation["paths"].([]any) {
					require.True(t, strings.HasPrefix(path.(string), "/saas.accounts.v1."),
						"a catalog-derived gate lists gRPC procedures, not HTTP routes: %q", path)
				}
			}
		case "DENY":
			denies++
			require.Equal(t, "deny-frontend-internal-http", document.Metadata.Name)
			require.Len(t, rules, len(wantRules), "one rule per authored route")
			for _, rule := range rules {
				source, ok := rule.(map[string]any)["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
				require.True(t, ok, "a deny with no source clause denies the route outright")
				// The waypoint that evaluates this rule never sees ingress-originated
				// traffic, so the ingress gateway is exempt by construction. Denying
				// it here would read as a north-south boundary and enforce nothing;
				// the route's credential is what gates the front door.
				require.Contains(t, source["notPrincipals"], meshIngressPrincipal)

				operation, ok := rule.(map[string]any)["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)
				require.True(t, ok)
				paths := operation["paths"].([]any)
				// Istio does not merge duplicate slashes by default, so the exact
				// path alone would let //api/solutions/register reach the handler.
				require.Len(t, paths, 2, "each route is matched exactly and as a suffix")
				path := paths[1].(string)
				require.Equal(t, "*"+path, paths[0])
				methods, declared := wantRules[path]
				require.True(t, declared, "policy gates an undeclared route %q", path)
				require.Equal(t, methods, operation["methods"])
				delete(wantRules, path)
			}
		default:
			t.Fatalf("unexpected mesh policy action %v on %q", document.Spec["action"], document.Metadata.Name)
		}
	}
	require.Equal(t, 1, allows, "exactly one internal-authority allow, for the catalog owner")
	require.Equal(t, 1, denies, "exactly one internal-HTTP deny, for the service that declares such routes")
	require.Empty(t, wantRules, "every authored route must be gated")
}

// TestInternalHTTPDenyExemptsDeclaredCallers covers the branch the shipped
// topology does not exercise: once a service declares an edge to the endpoint
// carrying internal routes, that caller — and only it, alongside the ingress
// gateway the waypoint cannot see — is subtracted from the deny.
func TestInternalHTTPDenyExemptsDeclaredCallers(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	documents := withService(t, readDeploymentDocuments(t), "marketing",
		"endpoints:\n    - name: http\n      visibility: public\n",
		"service-dependencies:\n    - name: frontend\n      endpoints:\n        - name: http\nendpoints:\n    - name: http\n      visibility: public\n")
	documents = withService(t, documents, "marketing",
		"    mode: ssr\n",
		"    mode: ssr\n    service-account:\n        name: marketing\n")

	artifacts, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog, documents)
	require.NoError(t, err)

	// The same authored edge opens the L3 path the exemption is useless without.
	network := string(artifacts.NetworkPolicy)
	require.Contains(t, network, "name: allow-frontend-from-dependents")
	require.Contains(t, network, "name: allow-marketing-to-dependencies")

	decoder := yaml.NewDecoder(strings.NewReader(string(artifacts.MeshPolicy)))
	found := false
	for {
		var document struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec map[string]any `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if document.Metadata.Name != "deny-frontend-internal-http" {
			continue
		}
		found = true
		rules, ok := document.Spec["rules"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, rules)
		for _, rule := range rules {
			source, ok := rule.(map[string]any)["from"].([]any)[0].(map[string]any)["source"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, []any{
				"cluster.local/ns/istio-system/sa/istio-ingressgateway-service-account",
				"cluster.local/ns/saas-starter/sa/marketing",
			}, source["notPrincipals"],
				"the exemption is the caller's own ServiceAccount, never the shared default")
		}
	}
	require.True(t, found, "the frontend still declares internal HTTP routes")
}

func TestDeploymentTopologyRejectsUnsafeOrIncompleteManifests(t *testing.T) {
	serviceCatalog := readFixture(t, "../../../generated/service-catalog.json")
	documents := readDeploymentDocuments(t)

	_, err := cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "accounts", "        - name: read\n        - name: write", "        - name: missing\n        - name: write"))
	require.ErrorContains(t, err, "unknown endpoint")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "store", "        bootstrap-job-endpoints:\n            - tcp", "        bootstrap-job-endpoints:\n            - missing"))
	require.ErrorContains(t, err, "bootstrap Job references unknown endpoint")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "accounts", "    - name: connect\n      visibility: module", "    - name: connect\n      api: http\n      visibility: module"))
	require.ErrorContains(t, err, "required API CODEFLY_API_CONNECT")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withModule(t, documents, "service-entry: frontend", "service-entry: missing"))
	require.ErrorContains(t, err, "service entry references unknown service")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "frontend", "                - DELETE\n                - POST", "                - POST\n                - DELETE"))
	require.ErrorContains(t, err, "internal HTTP route \"/api/solutions/register\" methods are invalid or unsorted")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "frontend", "            - path: /api/solutions/register", "            - path: api/solutions/register"))
	require.ErrorContains(t, err, "internal HTTP routes are invalid or unsorted")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "frontend", "              methods:\n                - DELETE\n                - POST", "              methods: []"))
	require.ErrorContains(t, err, "declares no methods")

	// A path policy on a service that speaks TCP matches nothing that will ever
	// be evaluated, so the route must belong to a service that serves HTTP.
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "store", "        bootstrap-job-endpoints:\n", "        internal-http-routes:\n            - path: /internal\n              methods:\n                - POST\n        bootstrap-job-endpoints:\n"))
	require.ErrorContains(t, err, "internal HTTP routes without an HTTP endpoint")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "cache", "endpoints:\n    - name: read\n", "service-dependencies:\n    - name: accounts\n      endpoints:\n        - name: connect\nendpoints:\n    - name: read\n"))
	require.ErrorContains(t, err, "contains a cycle")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withService(t, documents, "vault", "      entries:\n        - key: vault_token", "      entries: []"))
	require.ErrorContains(t, err, "secret service configurations are invalid or unsorted")

	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog,
		withModule(t, documents, "service-entry: frontend\n", "service-entry: frontend\nagent:\n    kind: codefly:module\n    name: saas-starter\n    version: 0.0.28\n"))
	require.ErrorContains(t, err, "module agent identity is incomplete")

	complete := withModule(t, documents, "service-entry: frontend\n", "service-entry: frontend\nagent:\n    kind: codefly:module\n    name: saas-starter\n    version: 0.0.28\n    publisher: codefly.dev\n")
	_, err = cataloggen.BuildDeploymentArtifacts(serviceCatalog, complete)
	require.NoError(t, err)
}

func TestDeploymentCatalogValidationRejectsConsumerUnsafeDrift(t *testing.T) {
	document := readFixture(t, moduleRoot+"deployment/generated/service-topology.json")
	catalog := &catalogv1.DeploymentCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(document, catalog))
	require.NoError(t, cataloggen.ValidateDeploymentCatalog(catalog))

	unsorted := proto.Clone(catalog).(*catalogv1.DeploymentCatalog)
	unsorted.Services[0], unsorted.Services[1] = unsorted.Services[1], unsorted.Services[0]
	require.ErrorContains(t, cataloggen.ValidateDeploymentCatalog(unsorted), "unsorted")

	unknownEndpoint := proto.Clone(catalog).(*catalogv1.DeploymentCatalog)
	unknownEndpoint.Services[0].Dependencies[0].Endpoints[0] = "missing"
	require.ErrorContains(t, cataloggen.ValidateDeploymentCatalog(unknownEndpoint), "unknown endpoint")

	invalidEgress := proto.Clone(catalog).(*catalogv1.DeploymentCatalog)
	invalidEgress.PublicEgress[0].Ports[0] = 0
	require.ErrorContains(t, cataloggen.ValidateDeploymentCatalog(invalidEgress), "public egress")
}
