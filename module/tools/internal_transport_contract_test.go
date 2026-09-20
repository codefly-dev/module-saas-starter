package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// INTERNAL_TRANSPORT.md is the contract a consumer writes its client against:
// which endpoint carries the internal tier, that nothing terminates TLS in
// front of it, and where verification keys come from. Prose alone drifts
// silently and in the worst direction — a consumer configures against a
// sentence that stopped being true and finds out on its first authority call,
// in production, with a refusal that names the credential rather than the
// document. These checks tie each load-bearing claim to the artifact that
// decides it.

const internalTransportDoc = "INTERNAL_TRANSPORT.md"

// internalTransportCarrier is the accounts endpoint the internal gRPC server is
// multiplexed onto. The doc names it; these checks hold it to the manifests.
const internalTransportCarrier = "rest"

const (
	accountsServiceManifest = "services/accounts/service.codefly.yaml"
	moduleManifest          = "module.codefly.yaml"
	jwksHandlerSource       = "services/accounts/code/pkg/adapters/jwks_http.go"
	meshPolicyGolden        = "services/accounts/code/pkg/cataloggen/testdata/mesh-policy.golden.yaml"
)

var jwksPathDeclaration = regexp.MustCompile(`jwksPath\s*=\s*"([^"]+)"`)

// delegatedAudienceTTLSource caps the lifetime of an exchanged audience child.
// Both delegated exchanges funnel through it, so it is the one bound a consumer
// has to size its cache against — and the one the contract previously
// contradicted by quoting the module identity context's fifteen minutes.
const delegatedAudienceTTLSource = "services/accounts/code/pkg/adapters/delegated_read_audience.go"

var delegatedAudienceTTLBound = regexp.MustCompile(`ttl := min\(int64\((\d+)\)`)

func readModuleFile(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleDir(t), filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

// collapsed compares a claim as a sentence rather than as a column: reflowing a
// paragraph must not fail the build.
func collapsed(text string) string { return strings.Join(strings.Fields(text), " ") }

// TestInternalTransportContractStatesTheDecision keeps the decision itself in
// the file. Everything else here checks that a claim is still true; this checks
// that it is still made, because a contract with its conclusions edited out
// leaves a consumer no better off than no contract at all.
func TestInternalTransportContractStatesTheDecision(t *testing.T) {
	document := collapsed(readModuleFile(t, internalTransportDoc))
	// One pin per distinct conclusion. Pinning every paragraph that restates a
	// conclusion buys no coverage and breaks the build on an honest rewrite.
	for _, claim := range []string{
		"In-cluster transport security is the mesh's, not the service's.",
		"A library that requires an explicit `https://` origin",
		"**Mounting a hand-authored copy is outside the contract.**",
	} {
		if !strings.Contains(document, collapsed(claim)) {
			t.Errorf("%s no longer states %q", internalTransportDoc, claim)
		}
	}
}

// TestInternalTransportContractNamesTheServedJWKSPath holds the one path a
// consumer types by hand to the one the handler serves. A moved path with the
// doc left behind sends every consumer to a 404 that reads as an outage.
func TestInternalTransportContractNamesTheServedJWKSPath(t *testing.T) {
	match := jwksPathDeclaration.FindStringSubmatch(readModuleFile(t, jwksHandlerSource))
	if match == nil {
		t.Fatalf("no jwksPath declaration in %s", jwksHandlerSource)
	}
	if !strings.Contains(readModuleFile(t, internalTransportDoc), match[1]) {
		t.Errorf(
			"%s names a key-set path other than %q, which is what %s serves",
			internalTransportDoc, match[1], jwksHandlerSource,
		)
	}
}

// TestInternalTransportCarrierIsDeclaredAndUnexported pins both halves of the
// awkward fact the contract has to state: the internal tier rides on an
// endpoint accounts really declares, and that endpoint is deliberately absent
// from the module interface. Exporting it would make the mixed private listener
// a product integration endpoint, which is the thing P1-NET-007 exists to avoid.
func TestInternalTransportCarrierIsDeclaredAndUnexported(t *testing.T) {
	var service struct {
		Endpoints []struct {
			Name string `yaml:"name"`
		} `yaml:"endpoints"`
	}
	if err := yaml.Unmarshal([]byte(readModuleFile(t, accountsServiceManifest)), &service); err != nil {
		t.Fatalf("parse %s: %v", accountsServiceManifest, err)
	}
	declared := false
	for _, endpoint := range service.Endpoints {
		if endpoint.Name == internalTransportCarrier {
			declared = true
		}
	}
	if !declared {
		t.Errorf(
			"accounts declares no %q endpoint, but %s names it as the internal-tier carrier",
			internalTransportCarrier, internalTransportDoc,
		)
	}

	var module struct {
		Interface struct {
			Endpoints []struct {
				Service  string `yaml:"service"`
				Endpoint string `yaml:"endpoint"`
			} `yaml:"endpoints"`
		} `yaml:"interface"`
	}
	if err := yaml.Unmarshal([]byte(readModuleFile(t, moduleManifest)), &module); err != nil {
		t.Fatalf("parse %s: %v", moduleManifest, err)
	}
	for _, exported := range module.Interface.Endpoints {
		if exported.Service == "accounts" && exported.Endpoint == internalTransportCarrier {
			t.Errorf(
				"%s exports accounts/%s: the mixed private listener became a module export, which %s says it is not",
				moduleManifest, internalTransportCarrier, internalTransportDoc,
			)
		}
	}
}

// authorizationPolicy is the slice of an Istio policy document these checks
// read: what it decides, and for whom.
type authorizationPolicy struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Action string `yaml:"action"`
		Rules  []struct {
			From []struct {
				Source struct {
					// Both fields are read because only reading one of them is
					// how this check failed open: an ALLOW whose source moved to
					// notPrincipals admits every principal EXCEPT the named one,
					// and a decoder that never looks leaves Principals empty and
					// every assertion below vacuously true.
					Principals    []string `yaml:"principals"`
					NotPrincipals []string `yaml:"notPrincipals"`
				} `yaml:"source"`
			} `yaml:"from"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

// TestInternalAuthorityAllowlistNamesOnlyThisModulesServices holds the reach
// gate to what the contract promises a consumer: an ALLOW keyed on the callers
// this workspace declares, which is why reach for a composed module is the
// composition's to grant and not something this module can confer. A principal
// from another namespace appearing here would mean the host had learned the
// name of a consumer; a flip to DENY would mean the surface had become
// allow-by-default for every principal the rule forgot to list.
func TestInternalAuthorityAllowlistNamesOnlyThisModulesServices(t *testing.T) {
	var bindings struct {
		Module struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"module"`
		Services []struct {
			Spec struct {
				ServiceAccount struct {
					Name string `yaml:"name"`
				} `yaml:"service-account"`
			} `yaml:"spec"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(readModuleFile(t, topologyBindingFile)), &bindings); err != nil {
		t.Fatalf("parse %s: %v", topologyBindingFile, err)
	}
	// Only explicitly declared service accounts. The renderer falls back to
	// `default` for a service that declares none, but six of this module's
	// services run as `default`, so admitting it here would admit all of them to
	// the authority surface at once — a widening, never a caller the topology
	// meant to name.
	admissible := map[string]bool{}
	for _, service := range bindings.Services {
		if name := service.Spec.ServiceAccount.Name; name != "" {
			admissible[name] = true
		}
	}

	var policies []authorizationPolicy
	decoder := yaml.NewDecoder(strings.NewReader(readModuleFile(t, meshPolicyGolden)))
	for {
		var policy authorizationPolicy
		if err := decoder.Decode(&policy); err != nil {
			break
		}
		policies = append(policies, policy)
	}

	found := false
	for _, document := range policies {
		if document.Kind != "AuthorizationPolicy" || !strings.HasSuffix(document.Metadata.Name, "-accounts-internal-authority") {
			continue
		}
		found = true
		if document.Spec.Action != "ALLOW" {
			t.Errorf("%s is %s, not ALLOW: the internal surface is allowlisted, never denied by exception", document.Metadata.Name, document.Spec.Action)
		}
		if len(document.Spec.Rules) == 0 {
			t.Errorf("%s carries no rule: an ALLOW that names no source admits nothing legible", document.Metadata.Name)
		}
		prefix := fmt.Sprintf("cluster.local/ns/%s/sa/", bindings.Module.Namespace)
		for _, rule := range document.Spec.Rules {
			if len(rule.From) == 0 {
				t.Errorf("%s has a rule with no source: it would admit every principal", document.Metadata.Name)
			}
			for _, from := range rule.From {
				if len(from.Source.NotPrincipals) > 0 {
					t.Errorf(
						"%s is an ALLOW keyed on notPrincipals %v: that admits every principal EXCEPT those, inverting the allowlist",
						document.Metadata.Name, from.Source.NotPrincipals,
					)
				}
				if len(from.Source.Principals) == 0 {
					t.Errorf(
						"%s has a source naming no principal: an ALLOW rule that names none restricts nobody",
						document.Metadata.Name,
					)
				}
				for _, principal := range from.Source.Principals {
					account, inNamespace := strings.CutPrefix(principal, prefix)
					switch {
					case !inNamespace:
						t.Errorf(
							"%s admits %q from outside this module's namespace: the host would be naming a consumer",
							document.Metadata.Name, principal,
						)
					case account == "default":
						t.Errorf(
							"%s admits the namespace default service account: every service that declares none runs as it, so this admits them all — declare a service account for the caller instead",
							document.Metadata.Name,
						)
					case !admissible[account]:
						t.Errorf(
							"%s admits %q, which is not a service account this module's own topology declares",
							document.Metadata.Name, principal,
						)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("no accounts internal-authority AuthorizationPolicy in %s", meshPolicyGolden)
	}
}

// TestInternalTransportContractNamesTheExchangedChildLifetime holds the number a
// consumer sizes its credential cache against to the number the exchange
// actually signs. The contract first shipped the module identity context's
// fifteen minutes as if it governed every capability; an exchanged child lives
// 60 seconds, so a worker that believed the document was refused on every call
// after the first minute.
func TestInternalTransportContractNamesTheExchangedChildLifetime(t *testing.T) {
	match := delegatedAudienceTTLBound.FindStringSubmatch(readModuleFile(t, delegatedAudienceTTLSource))
	if match == nil {
		t.Fatalf("no delegated-audience TTL bound in %s", delegatedAudienceTTLSource)
	}
	bound := collapsed(fmt.Sprintf("audience child at %s seconds", match[1]))
	if !strings.Contains(collapsed(readModuleFile(t, internalTransportDoc)), bound) {
		t.Errorf(
			"%s does not state that an exchanged audience child caps at %s seconds, which is what %s signs",
			internalTransportDoc, match[1], delegatedAudienceTTLSource,
		)
	}
}

// TestInternalTransportContractNamesEveryExportedEndpoint keeps an enumerating
// sentence from going stale in silence. The contract lists what the module
// interface publishes so a consumer knows none of it carries the internal tier;
// a fourth export added without touching that sentence leaves the reader an
// inventory that is quietly short.
func TestInternalTransportContractNamesEveryExportedEndpoint(t *testing.T) {
	var module struct {
		Interface struct {
			Endpoints []struct {
				Service    string `yaml:"service"`
				Endpoint   string `yaml:"endpoint"`
				Visibility string `yaml:"visibility"`
			} `yaml:"endpoints"`
		} `yaml:"interface"`
	}
	if err := yaml.Unmarshal([]byte(readModuleFile(t, moduleManifest)), &module); err != nil {
		t.Fatalf("parse %s: %v", moduleManifest, err)
	}
	document := readModuleFile(t, internalTransportDoc)
	named := 0
	for _, exported := range module.Interface.Endpoints {
		if exported.Visibility != "module" {
			continue
		}
		named++
		reference := fmt.Sprintf("`%s/%s`", exported.Service, exported.Endpoint)
		if !strings.Contains(document, reference) {
			t.Errorf(
				"%s exports %s with module visibility and %s never names it",
				moduleManifest, reference, internalTransportDoc,
			)
		}
	}
	if named == 0 {
		t.Fatalf("%s publishes no module-visible endpoint; the contract's inventory has nothing to describe", moduleManifest)
	}
}
