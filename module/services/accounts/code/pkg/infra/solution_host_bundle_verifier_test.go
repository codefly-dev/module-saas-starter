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
// There is ONE policy now. The `local` policy — which performed no cryptography
// and attested every carrier, gated on the host's coordinate starting `local/` —
// is deleted, and its tests went with it. What replaced them is the keyless
// suite, which exercises the same verifier a laptop would run, against a local
// trust root and a local allowlist.

// No policy is not a permissive policy: the absence is refused by name at boot
// rather than defaulted, because a host that does not say how it checks a
// carrier must not be guessed at.
func TestUnsetTrustPolicyIsRefused(t *testing.T) {
	verifier, err := infra.NewSolutionHostBundleVerifier("", t.TempDir())
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
//
// `local` is included deliberately: it USED to be a working value, and a
// deployment still carrying it in its configuration must fail to boot rather
// than silently fall through to keyless — which would read as "my local policy
// is still working" while the host had started verifying for real, or worse,
// read as nothing at all.
func TestUnknownTrustPolicyIsRefused(t *testing.T) {
	for _, policy := range []infra.SolutionHostTrustPolicy{"trust-me", "local", "none", "KEYLESS"} {
		verifier, err := infra.NewSolutionHostBundleVerifier(policy, t.TempDir())
		if err == nil {
			t.Fatalf("trust policy %q must be refused", policy)
		}
		if verifier != nil {
			t.Fatalf("trust policy %q must yield no verifier", policy)
		}
	}
}
