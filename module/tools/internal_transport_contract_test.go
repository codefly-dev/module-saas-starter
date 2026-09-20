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
	for _, claim := range []string{
		"In-cluster transport security is the mesh's, not the service's.",
		"The accounts service holds no server certificate, terminates no TLS of its own",
		"The origin is **resolved, never configured as a URL**",
		"A library that requires an explicit `https://` origin",
		"**This document is the only producer.**",
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
					Principals []string `yaml:"principals"`
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
	admissible := map[string]bool{"default": true}
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
		for _, rule := range document.Spec.Rules {
			for _, from := range rule.From {
				for _, principal := range from.Source.Principals {
					prefix := fmt.Sprintf("cluster.local/ns/%s/sa/", bindings.Module.Namespace)
					account, inNamespace := strings.CutPrefix(principal, prefix)
					if !inNamespace || !admissible[account] {
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
