package composition

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	corecomposition "github.com/codefly-dev/core/composition"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"
)

// Client publication policy lives in module/clients.codefly.yaml, which the CLI
// reads directly (cli/pkg/generators/module_clients.go,
// LoadModuleClientsConfig). It is deliberately NOT part of
// module.codefly.yaml's generated `interface:` block: that file is rendered
// from deployment/topology.bindings.codefly.yaml, so an inline block there is
// replaced on the next regeneration — and, worse, is silently ignored, because
// nothing in the CLI reads it.
//
// These constants and structs mirror the CLI's loader, including its strict
// field checking. That duplication is the point: the gate reads exactly the
// bytes the CLI will read, so a schema or filename move surfaces in `go test`
// here instead of at a release tag, and the repository does not need the CLI
// installed to know its own publication policy is well formed.
const (
	moduleClientsConfigFileName = "clients.codefly.yaml"
	moduleClientsConfigSchema   = "codefly/module-clients-config/v1"
)

type endpointClientsConfig struct {
	Service   string   `yaml:"service"`
	Endpoint  string   `yaml:"endpoint"`
	Publish   *bool    `yaml:"publish,omitempty"`
	Languages []string `yaml:"languages,omitempty"`
	Services  []string `yaml:"services,omitempty"`
}

type moduleClientsConfigDocument struct {
	Schema    string                  `yaml:"schema"`
	Endpoints []endpointClientsConfig `yaml:"endpoints,omitempty"`
}

// defaultClientLanguages mirrors generators.DefaultClientLanguages: an
// endpoint that declares no `languages:` publishes all of these, so the set is
// also the ceiling a declared list may draw from.
func defaultClientLanguages(kind string) []string {
	if kind == corecomposition.APIContractKindOpenAPI {
		return []string{"go", "typescript"}
	}
	return []string{"go", "typescript", "python"}
}

func loadModuleClientsConfig(t *testing.T, moduleRoot string) map[string]endpointClientsConfig {
	t.Helper()
	path := filepath.Join(moduleRoot, moduleClientsConfigFileName)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("read %s: %v", moduleClientsConfigFileName, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var document moduleClientsConfigDocument
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("parse %s under the CLI's strict loader: %v", moduleClientsConfigFileName, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("%s: the CLI rejects multiple YAML documents", moduleClientsConfigFileName)
	}
	if document.Schema != moduleClientsConfigSchema {
		t.Fatalf("%s: schema is %q, but the CLI only accepts %q", moduleClientsConfigFileName, document.Schema, moduleClientsConfigSchema)
	}

	configured := make(map[string]endpointClientsConfig, len(document.Endpoints))
	for _, endpoint := range document.Endpoints {
		endpoint.Service = strings.TrimSpace(endpoint.Service)
		endpoint.Endpoint = strings.TrimSpace(endpoint.Endpoint)
		if endpoint.Service == "" || endpoint.Endpoint == "" {
			t.Fatalf("%s: every endpoint requires service and endpoint", moduleClientsConfigFileName)
		}
		key := endpoint.Service + "/" + endpoint.Endpoint
		if _, exists := configured[key]; exists {
			t.Fatalf("%s: endpoint %s is configured twice", moduleClientsConfigFileName, key)
		}
		configured[key] = endpoint
	}
	return configured
}

// TestEveryExportedAPIContractDeclaresAClientPublicationDecision is the
// load-bearing invariant. `codefly publish clients` iterates the contract
// catalog, not this file: an exported endpoint that the config does not mention
// publishes a client in EVERY default language with its FULL service surface,
// to a repository the store creates public. Silence is therefore the widest
// possible disclosure, not a no-op, so every exported endpoint must state its
// decision explicitly — an allowlist, or `publish: false`.
func TestEveryExportedAPIContractDeclaresAClientPublicationDecision(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	catalog, err := corecomposition.LoadAPIContractCatalog(moduleRoot)
	if err != nil {
		t.Fatalf("load API contract catalog: %v", err)
	}
	if len(catalog.Endpoints) == 0 {
		t.Fatal("API contract catalog declares no endpoints")
	}
	configured := loadModuleClientsConfig(t, moduleRoot)

	for _, endpoint := range catalog.Endpoints {
		key := endpoint.Service + "/" + endpoint.Endpoint
		if _, declared := configured[key]; !declared {
			t.Errorf(
				"exported contract %s is absent from %s, so publishing it would ship every service in package %s "+
					"(%d of them) as a public client library in %v; declare an allowlist or `publish: false`",
				key, moduleClientsConfigFileName, endpoint.Package, len(endpoint.Services),
				defaultClientLanguages(endpoint.Kind),
			)
		}
	}
}

// TestModuleClientsConfigMatchesTheExportedContracts holds the config to what
// the package actually exports. Each of these mirrors a refusal in
// generators.PlanModuleClients; checking them offline means a rename in a
// .proto fails a pull request instead of the release tag that publishes from
// it.
func TestModuleClientsConfigMatchesTheExportedContracts(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	catalog, err := corecomposition.LoadAPIContractCatalog(moduleRoot)
	if err != nil {
		t.Fatalf("load API contract catalog: %v", err)
	}
	configured := loadModuleClientsConfig(t, moduleRoot)

	exported := make(map[string]corecomposition.APIContractEndpoint, len(catalog.Endpoints))
	for _, endpoint := range catalog.Endpoints {
		exported[endpoint.Service+"/"+endpoint.Endpoint] = endpoint
	}

	for key, config := range configured {
		endpoint, isExported := exported[key]
		if !isExported {
			t.Errorf(
				"%s configures clients for %s, which the package does not export; run `codefly generate contracts saas-starter` or drop the entry",
				moduleClientsConfigFileName, key,
			)
			continue
		}

		allowed := defaultClientLanguages(endpoint.Kind)
		for _, language := range config.Languages {
			language = strings.TrimSpace(language)
			if !slices.Contains(allowed, language) {
				t.Errorf("%s: endpoint %s requests language %q, but %s contracts only generate %v",
					moduleClientsConfigFileName, key, language, endpoint.Kind, allowed)
			}
		}

		if len(config.Services) == 0 {
			continue
		}
		if endpoint.Kind != corecomposition.APIContractKindProtobuf {
			t.Errorf("%s: endpoint %s sets `services:`, which only applies to protobuf contracts (this one is %q)",
				moduleClientsConfigFileName, key, endpoint.Kind)
			continue
		}
		declared := make(map[string]struct{}, len(endpoint.Services))
		for _, service := range endpoint.Services {
			declared[service.Name] = struct{}{}
		}
		for _, name := range config.Services {
			if _, exists := declared[strings.TrimSpace(name)]; !exists {
				t.Errorf("%s: endpoint %s allowlists %q, which the exported contract (package %s) does not declare",
					moduleClientsConfigFileName, key, name, endpoint.Package)
			}
		}
	}
}

// TestAllowlistedClientServicesAreAloneInTheirProtoFile enforces the invariant
// the allowlist relies on: a generated binding's unit is the .proto FILE, not
// the service. Allowlisting AuditService ships all of audit.proto, so a second
// service added to that file — or an import of a same-package file that itself
// declares a service — would reach the public client library without ever
// appearing in the allowlist a reviewer reads.
//
// #577 split accessible_scopes.proto out of the shared file for exactly this
// reason. Nothing but this test keeps that split honest.
func TestAllowlistedClientServicesAreAloneInTheirProtoFile(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	catalog, err := corecomposition.LoadAPIContractCatalog(moduleRoot)
	if err != nil {
		t.Fatalf("load API contract catalog: %v", err)
	}
	configured := loadModuleClientsConfig(t, moduleRoot)

	for _, endpoint := range catalog.Endpoints {
		config, declared := configured[endpoint.Service+"/"+endpoint.Endpoint]
		if !declared || len(config.Services) == 0 || endpoint.Kind != corecomposition.APIContractKindProtobuf {
			continue
		}
		if config.Publish != nil && !*config.Publish {
			continue
		}

		data, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(endpoint.Path)))
		if err != nil {
			t.Fatalf("read contract file %q: %v", endpoint.Path, err)
		}
		var set descriptorpb.FileDescriptorSet
		if err := proto.Unmarshal(data, &set); err != nil {
			t.Fatalf("parse contract %q as a FileDescriptorSet: %v", endpoint.Path, err)
		}

		// Files of the endpoint's own proto package, and the services each declares.
		servicesByFile := map[string][]string{}
		fileOfService := map[string]string{}
		for _, file := range set.GetFile() {
			if file.GetPackage() != endpoint.Package {
				continue
			}
			for _, service := range file.GetService() {
				servicesByFile[file.GetName()] = append(servicesByFile[file.GetName()], service.GetName())
				fileOfService[service.GetName()] = file.GetName()
			}
		}
		dependenciesByFile := map[string][]string{}
		for _, file := range set.GetFile() {
			dependenciesByFile[file.GetName()] = file.GetDependency()
		}

		for _, name := range config.Services {
			name = strings.TrimSpace(name)
			source, found := fileOfService[name]
			if !found {
				t.Errorf("allowlisted service %q declares no file in package %s of contract %q",
					name, endpoint.Package, endpoint.Path)
				continue
			}
			if siblings := servicesByFile[source]; len(siblings) != 1 {
				sort.Strings(siblings)
				t.Errorf(
					"%s declares %d services (%s), but %q allowlists only %q; a binding ships the whole file, "+
						"so split the others into their own .proto or allowlist them deliberately",
					source, len(siblings), strings.Join(siblings, ", "), moduleClientsConfigFileName, name,
				)
			}
			for _, dependency := range dependenciesByFile[source] {
				if leaked := servicesByFile[dependency]; len(leaked) > 0 {
					sort.Strings(leaked)
					t.Errorf(
						"%s (shipped for allowlisted %q) imports %s, which declares service(s) %s; "+
							"move the shared messages into a service-free .proto",
						source, name, dependency, strings.Join(leaked, ", "),
					)
				}
			}
		}
	}
}
