package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTopologyConsumerDerivedInternalReach(t *testing.T) {
	for _, exported := range []bool{false, true} {
		visibility, err := endpointVisibility("internal", "", exported)
		if err != nil || visibility != "module" {
			t.Fatalf("internal exported=%v: %q, %v", exported, visibility, err)
		}
	}
	visibility, err := endpointVisibility("", "", true)
	if err != nil || visibility != "module" {
		t.Fatalf("default interface reach: %q, %v", visibility, err)
	}
	visibility, err = endpointVisibility("", "", false)
	if err != nil || visibility != "private" {
		t.Fatalf("default service reach: %q, %v", visibility, err)
	}
}

func TestTopologyRefusesAuthoredConsumersByKeyPresence(t *testing.T) {
	for _, key := range []string{"allow-modules", "allow_modules", "allowModules", "Allow Modules"} {
		for _, value := range []string{"[\"*\"]", "[example]", "[]", "null"} {
			document := []byte("visibility: internal\n" + key + ": " + value + "\n")
			for _, endpoint := range []any{&topologyManifestEndpoint{}, &topologyInterface{}} {
				err := yaml.Unmarshal(document, endpoint)
				if err == nil || !strings.Contains(err.Error(), "derived from consumers") {
					t.Fatalf("%T %s=%s: %v", endpoint, key, value, err)
				}
			}
		}
	}
}

func TestBootstrapPoliciesSelectImmutableJobByServiceLabel(t *testing.T) {
	// Postgres appends a digest to the immutable Job name. Kubernetes copies
	// that name into job-name; the agent's service label remains stable.
	for _, policy := range []kubeObject{
		bootstrapJobIngressPolicy("example", nil, "store", "store", []uint32{5432}),
		bootstrapJobEgressPolicy("example", nil, "store", "store", []uint32{5432}),
	} {
		document, err := yaml.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(document), "job-name:") || !strings.Contains(string(document), "codefly.dev/bootstrap-service: store") {
			t.Fatalf("policy cannot select a content-addressed bootstrap Job:\n%s", document)
		}
	}
}
