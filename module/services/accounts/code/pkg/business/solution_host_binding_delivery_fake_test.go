package business

import (
	"context"
	"testing"

	"accounts/internal/deliverytest"

	"github.com/codefly-dev/core/solutionhost"
)

// Every admission fixture is signed and verified, because core's Delivered
// values can only be constructed by checking the envelope and ownership domain.
func fixtureSigner(t *testing.T) *solutionhost.Verifier {
	return deliverytest.Verifier(t, map[string][]string{solutionhost.FixtureDeliveredBy: {solutionhost.FixtureDomain}})
}

// signedCarrier wraps one document in the carrier delivery would ship: the
// canonical bytes verbatim, plus a DSSE envelope over them.
//
// CanonicalBytes, not Marshal. Marshal renders YAML for a delivery repository;
// the SIGNING input is the canonical JSON, and PresenceFromVerified refuses a
// payload that is not the canonical encoding of the document it decodes to —
// even when the attestation over those bytes is genuine, because a signer and a
// host that disagree about which bytes represent the document disagree about
// what was approved.
func signedCarrier(t *testing.T, document *solutionhost.SolutionHostBinding) []byte {
	t.Helper()
	canonical, err := solutionhost.SignedPayload(document)
	if err != nil {
		t.Fatalf("canonical bytes for %q: %v", document.Binding, err)
	}
	return deliverytest.Carrier(t, canonical, solutionhost.FixtureDeliveredBy)
}

// carrierOver wraps arbitrary payload bytes, so a test can deliver bytes that
// are deliberately not a valid document. The carrier is JSON, so a payload that
// is not a JSON value cannot be carried at all — which is itself the shape of
// the refusal a test about unreadable bytes is asserting.
func carrierOver(t *testing.T, payload []byte) []byte {
	t.Helper()
	envelope := deliverytest.Envelope(t, payload, solutionhost.FixtureDeliveredBy)
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{Document: payload, Envelope: envelope})
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	return carrier
}

// delivered verifies documents through the real VerifyDelivered with the fixture
// signer, producing the values admission takes.
func delivered(t *testing.T, documents ...*solutionhost.SolutionHostBinding) []deliveredSolutionHostBinding {
	t.Helper()
	return deliveredBy(t, fixtureSigner(t), documents...)
}

// deliveredBy is delivered with a pinned-key verifier, for a test about who
// signed rather than about what was signed.
//
// It pairs each carrier with the document re-derived from its attested bytes,
// the way the reconcile pass does, rather than with the document it was handed:
// a helper that returned the caller's own pointer would let a test assert
// against a value admission never reads. See deliveredSolutionHostBinding.
func deliveredBy(
	t *testing.T, verifier *solutionhost.Verifier, documents ...*solutionhost.SolutionHostBinding,
) []deliveredSolutionHostBinding {
	t.Helper()
	out := make([]deliveredSolutionHostBinding, 0, len(documents))
	for _, document := range documents {
		carrier, err := solutionhost.ParseSigned(signedCarrier(t, document))
		if err != nil {
			t.Fatalf("parse carrier for %q: %v", document.Binding, err)
		}
		one, err := solutionhost.VerifyDelivered(context.Background(), carrier, verifier)
		if err != nil {
			t.Fatalf("verify carrier for %q: %v", document.Binding, err)
		}
		attested, err := one.Document()
		if err != nil {
			t.Fatalf("re-derive document for %q: %v", document.Binding, err)
		}
		out = append(out, deliveredSolutionHostBinding{Delivered: one, Document: attested})
	}
	return out
}

// fixtureSignerHost is a host at one coordinate that accepts core's fixture
// domain and lets the fixture signer speak for it.
//
// DomainsBySigner is required whenever a coordinate is set — core refuses the
// call without it — so a test host that omitted it would fail with "declare a
// signer policy" instead of the refusal it meant to assert, which is how a test
// passes for the wrong reason.
func fixtureSignerHost(coordinate string) solutionhost.Host {
	return solutionhost.Host{
		Coordinate:      coordinate,
		Domains:         []string{solutionhost.FixtureDomain},
		DomainsBySigner: map[string][]string{solutionhost.FixtureDeliveredBy: {solutionhost.FixtureDomain}},
	}
}
