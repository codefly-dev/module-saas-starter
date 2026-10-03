package business

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
)

// Admission takes *solutionhost.Delivered, which has unexported fields and no
// constructor but solutionhost.VerifyDelivered. That is deliberate on core's
// part — it is how "this document was attested" becomes a compiler fact rather
// than a naming convention — and it means a test cannot fabricate one. So these
// helpers build a real carrier and run it through a real VerifyDelivered with a
// stub attestation check, which is also the only way a test can exercise the
// refusal paths the host must record.

// stubBundleVerifier is a BundleVerifier that attests whatever it is told to.
// It stands in for sigstore-go, which cannot be exercised here: a real bundle
// would need a trust root, a transparency-log entry and a certificate that
// expires, none of which belong in a unit test.
type stubBundleVerifier struct {
	// signer is the identity attested for any bundle. Empty means refuse.
	signer string

	// err, when set, is the refusal — a bundle that does not verify.
	err error

	// payloads records the exact bytes each call was asked about, so a test can
	// assert the verifier saw the carrier's document verbatim rather than a
	// re-encoding of a parsed form.
	payloads [][]byte
}

func (v *stubBundleVerifier) VerifyBundle(_ context.Context, payload []byte, _ json.RawMessage) (string, error) {
	v.payloads = append(v.payloads, payload)
	if v.err != nil {
		return "", v.err
	}
	return v.signer, nil
}

// fixtureSigner attests every carrier as core's own fixture signer identity,
// which solutionhost.FixtureHost's DomainsBySigner policy is written against.
func fixtureSigner() *stubBundleVerifier {
	return &stubBundleVerifier{signer: solutionhost.FixtureDeliveredBy}
}

// signedCarrier wraps one document in the carrier delivery would ship: the
// canonical bytes verbatim, plus a bundle over them.
//
// CanonicalBytes, not Marshal. Marshal renders YAML for a delivery repository;
// the SIGNING input is the canonical JSON, and PresenceFromVerified refuses a
// payload that is not the canonical encoding of the document it decodes to —
// even when the attestation over those bytes is genuine, because a signer and a
// host that disagree about which bytes represent the document disagree about
// what was approved.
func signedCarrier(t *testing.T, document *solutionhost.SolutionHostBinding) []byte {
	t.Helper()
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical bytes for %q: %v", document.Binding, err)
	}
	return carrierOver(t, canonical)
}

// carrierOver wraps arbitrary payload bytes, so a test can deliver bytes that
// are deliberately not a valid document. The carrier is JSON, so a payload that
// is not a JSON value cannot be carried at all — which is itself the shape of
// the refusal a test about unreadable bytes is asserting.
func carrierOver(t *testing.T, payload []byte) []byte {
	t.Helper()
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{
		Schema:   solutionhost.SchemaSignedV1,
		Document: payload,
		Bundle:   json.RawMessage(solutionhost.FixtureBundle),
	})
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	return carrier
}

// delivered verifies documents through the real VerifyDelivered with the fixture
// signer, producing the values admission takes.
func delivered(t *testing.T, documents ...*solutionhost.SolutionHostBinding) []deliveredSolutionHostBinding {
	t.Helper()
	return deliveredBy(t, fixtureSigner(), documents...)
}

// deliveredBy is delivered with a named attestation check, for a test about who
// signed rather than about what was signed.
//
// It pairs each carrier with the document re-derived from its attested bytes,
// the way the reconcile pass does, rather than with the document it was handed:
// a helper that returned the caller's own pointer would let a test assert
// against a value admission never reads. See deliveredSolutionHostBinding.
func deliveredBy(
	t *testing.T, verifier solutionhost.BundleVerifier, documents ...*solutionhost.SolutionHostBinding,
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
