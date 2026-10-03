package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/infra"
)

// The trust policy decides whether a delivered carrier is checked at all, so
// each of these is about a way the host could end up admitting something nobody
// attested.

// No policy is not a permissive policy. Every default is wrong somewhere —
// trusting delivery in production, refusing everything on a laptop — so the
// absence is refused by name at boot.
func TestUnsetTrustPolicyIsRefused(t *testing.T) {
	verifier, err := infra.NewSolutionHostBundleVerifier("", "acme/prod/eu-west-1")
	if err == nil {
		t.Fatal("an unset trust policy must be refused, not defaulted")
	}
	if verifier != nil {
		t.Fatal("a refused policy must yield no verifier")
	}
	if !strings.Contains(err.Error(), "SOLUTION_HOST_TRUST_POLICY") {
		t.Fatalf("error %q does not name the key an operator must set", err)
	}
}

// A policy nobody implements must not fall back to one that does.
func TestUnknownTrustPolicyIsRefused(t *testing.T) {
	if _, err := infra.NewSolutionHostBundleVerifier("trust-me", "acme/prod/eu-west-1"); err == nil {
		t.Fatal("an unknown trust policy must be refused")
	}
}

// The local policy performs no cryptography, so the coordinate is the only thing
// standing between it and a production host trusting whatever is in its mount.
// This is the test that makes that an invariant rather than a comment.
func TestLocalTrustPolicyIsRefusedOnANonLocalCoordinate(t *testing.T) {
	for _, coordinate := range []string{
		"acme/prod/eu-west-1",
		"acme/staging/eu-west-1",
		// Deliberately adjacent to the allowlist without matching it: a
		// coordinate is an operator-declared string, so "contains local" must
		// not be enough.
		"acme/local/eu-west-1",
		"notlocal/prod/eu-west-1",
		"",
	} {
		t.Run(coordinate, func(t *testing.T) {
			verifier, err := infra.NewSolutionHostBundleVerifier(infra.SolutionHostTrustLocal, coordinate)
			if err == nil {
				t.Fatalf("the local trust policy must be refused on coordinate %q", coordinate)
			}
			if verifier != nil {
				t.Fatal("a refused policy must yield no verifier")
			}
		})
	}
}

// And it is allowed where the mount and the host are the same person.
func TestLocalTrustPolicyAttestsALocalIdentity(t *testing.T) {
	verifier, err := infra.NewSolutionHostBundleVerifier(infra.SolutionHostTrustLocal, "local/dev/laptop")
	if err != nil {
		t.Fatalf("local coordinate: %v", err)
	}
	signer, err := verifier.VerifyBundle(context.Background(), []byte("anything"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("local verifier: %v", err)
	}
	// It attests an identity rather than a bare yes, because the signer-to-domain
	// check runs even on a laptop: a local developer still has to be granted the
	// domain it delivers under, through the same policy a workflow identity uses.
	if signer != infra.SolutionHostLocalSignerIdentity {
		t.Fatalf("signer = %q, want %q", signer, infra.SolutionHostLocalSignerIdentity)
	}
}

// The keyless policy refuses at BOOT rather than per document, and says why.
//
// A host configured for a policy it cannot perform must not start and then
// refuse every document as though delivery were broken: those are different
// facts, and only one of them is the operator's to fix.
func TestKeylessTrustPolicyRefusesAtBootWhileUnprovisioned(t *testing.T) {
	verifier, err := infra.NewSolutionHostBundleVerifier(infra.SolutionHostTrustKeyless, "acme/prod/eu-west-1")
	if !errors.Is(err, infra.ErrSolutionHostTrustUnavailable) {
		t.Fatalf("error = %v, want ErrSolutionHostTrustUnavailable", err)
	}
	if verifier != nil {
		t.Fatal("an unavailable policy must yield no verifier")
	}
	if !strings.Contains(err.Error(), "trust root") {
		t.Fatalf("error %q does not say what is missing", err)
	}
}
