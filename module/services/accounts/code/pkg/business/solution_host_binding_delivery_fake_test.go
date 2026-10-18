package business

import (
	"context"
	"testing"

	"accounts/internal/deliverytest"

	"github.com/codefly-dev/core/solutionhost"
)

// Every admission fixture is signed and verified, because core's Delivered
// values can only be constructed by checking the envelope and ownership domain.

const (
	testDeliveryKey        = "example-release"
	testDeliveryDomain     = "acme"
	testDeliveryCoordinate = "acme-test"
)

// testModuleNamespaceRule is core's bare rule: a module's namespace is its
// name.
var testModuleNamespaceRule = ModuleNamespaceRule{}

func fixtureSigner(t *testing.T) *solutionhost.Verifier {
	return deliverytest.Verifier(t, map[string][]string{testDeliveryKey: {testDeliveryDomain}})
}

// fixtureManifest is a valid live manifest for one alias on the test host.
func fixtureManifest(t *testing.T, alias string, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test." + alias, Alias: alias,
		Kind: solutionhost.KindModule, Generation: generation,
		Host:      solutionhost.HostTarget{Coordinate: testDeliveryCoordinate, Domain: testDeliveryDomain},
		Release:   solutionhost.Release{Publisher: "acme", Name: alias, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: alias, Container: alias}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	return manifest
}

// withdrawalOf is the withdrawal of manifest at generation.
func withdrawalOf(t *testing.T, manifest *solutionhost.ModuleManifest, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	removed := *manifest
	removed.Generation, removed.Removed = generation, true
	removed.Workloads, removed.Endpoints, removed.Authority, removed.Frontend, removed.Group = nil, nil, nil, nil, nil
	if err := removed.Validate(); err != nil {
		t.Fatalf("fixture withdrawal: %v", err)
	}
	return &removed
}

// signedCarrier wraps one manifest in the carrier delivery ships: the
// canonical bytes verbatim, plus a DSSE envelope over them.
func signedCarrier(t *testing.T, manifest *solutionhost.ModuleManifest) []byte {
	t.Helper()
	canonical, err := solutionhost.SignedPayload(manifest)
	if err != nil {
		t.Fatalf("canonical bytes for %q: %v", manifest.Binding, err)
	}
	return deliverytest.Carrier(t, canonical, testDeliveryKey)
}

// carrierOver wraps arbitrary payload bytes, so a test can deliver bytes that
// are deliberately not a valid manifest.
func carrierOver(t *testing.T, payload []byte) []byte {
	t.Helper()
	envelope := deliverytest.Envelope(t, payload, testDeliveryKey)
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{Document: payload, Envelope: envelope})
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	return carrier
}

// delivered verifies manifests through the real VerifyDelivered, pairing each
// carrier with the manifest re-derived from its attested bytes, as the pass
// does.
func delivered(t *testing.T, manifests ...*solutionhost.ModuleManifest) []deliveredSolutionHostBinding {
	t.Helper()
	out := make([]deliveredSolutionHostBinding, 0, len(manifests))
	for _, manifest := range manifests {
		carrier, err := solutionhost.ParseSigned(signedCarrier(t, manifest))
		if err != nil {
			t.Fatalf("parse carrier for %q: %v", manifest.Binding, err)
		}
		one, err := solutionhost.VerifyDelivered(context.Background(), carrier, fixtureSigner(t))
		if err != nil {
			t.Fatalf("verify carrier for %q: %v", manifest.Binding, err)
		}
		attested, err := one.Manifest()
		if err != nil {
			t.Fatalf("re-derive manifest for %q: %v", manifest.Binding, err)
		}
		out = append(out, deliveredSolutionHostBinding{Delivered: one, Document: attested})
	}
	return out
}

// appliedRecordOf is the host's record of having applied manifest.
func appliedRecordOf(t *testing.T, manifest *solutionhost.ModuleManifest) *SolutionHostBindingRecord {
	t.Helper()
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := solutionhost.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	solutionID := manifest.Alias
	if !manifest.Removed {
		routes = []string{manifest.Alias}
	}
	return &SolutionHostBindingRecord{
		BindingID: manifest.Binding, HostCoordinate: manifest.Host.Coordinate,
		Applied: &SolutionHostBindingApplied{
			SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: manifest.Generation, Digest: digest, Document: string(encoded)},
			Removed:                       manifest.Removed, Routes: routes, SolutionID: solutionID,
			Release: manifest.Release.Identity(), Domain: manifest.Host.Domain,
		},
	}
}
