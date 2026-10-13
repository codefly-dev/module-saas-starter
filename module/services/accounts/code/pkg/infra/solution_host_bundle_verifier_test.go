package infra_test

import (
	"strings"
	"testing"

	"accounts/pkg/infra"
)

// The trust policy decides whether a delivered carrier is checked at all, so
// each of these is about a way the host could end up admitting something nobody
// attested.
//
// No policy is not a permissive policy: the absence is refused by name at boot
// rather than defaulted, because a host that does not say how it checks a
// carrier must not be guessed at.
// No directory is passed to any of these any more: the anchor's location is a
// constant, deliberately not a parameter. See
// infra.SolutionHostTrustAnchorPath — a path a caller could pass is a path a
// composition could eventually supply, which is the hole the constant closes.
func TestUnsetTrustPolicyIsRefused(t *testing.T) {
	verifier, err := infra.NewSolutionHostBundleVerifier("")
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
	for _, policy := range []infra.SolutionHostTrustPolicy{"trust-me", "local", "none", "KEYS"} {
		verifier, err := infra.NewSolutionHostBundleVerifier(policy)
		if err == nil {
			t.Fatalf("trust policy %q must be refused", policy)
		}
		if verifier != nil {
			t.Fatalf("trust policy %q must yield no verifier", policy)
		}
	}
}

func TestRemovedKeylessPolicyIsRefused(t *testing.T) {
	verifier, err := infra.NewSolutionHostBundleVerifier("keyless")
	if verifier != nil || err == nil || !strings.Contains(err.Error(), "keyless signing was removed") || !strings.Contains(err.Error(), `"keys"`) {
		t.Fatalf("removed policy: verifier %v, error %v", verifier, err)
	}
}
