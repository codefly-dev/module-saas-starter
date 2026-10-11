package infra

import (
	"errors"
	"fmt"
	"os"

	"github.com/codefly-dev/core/solutionhost"
)

// The host's attestation check over a delivered carrier (issue #952).
//
// core holds no verifier and never will, and the reason shapes this file:
// signing is KEYLESS over a workload identity, so verifying is a trust root plus
// an identity allowlist that the deployment owns. A library every binary imports
// cannot hold either.
//
// What a verifier returns is the SIGNER IDENTITY, not a yes. The host maps that
// identity to the ownership domains it may deliver under, and core refuses a
// document whose asserted domain the attested signer may not speak for. That
// mapping is why a verifier answering a bare boolean would be unusable: "this is
// signed" does not say "by someone entitled to this name".
//
// THERE IS ONE POLICY. An earlier shape offered a second, `local`, which
// performed no cryptography and attested every carrier as the identity `local`,
// gated on the host's coordinate beginning `local/`. It is deleted, and the
// deletion resolves a contradiction this repository's own documentation carried:
// §8 of SOLUTION_REGISTRATION.md argued at length that there should be no local
// trust *mechanism* — "one verifier, one policy shape, two sets of listed
// identities" — while §7 shipped a policy that attested everything.
//
// The adversarial reviews named the concrete failure: the gate was
// `strings.HasPrefix(coordinate, "local/")` on an OPERATOR-DECLARED string, so a
// deployed host configured with a local coordinate admitted any file any mount
// writer dropped, and the coordinate being self-asserted on both sides meant the
// misconfiguration was not self-defeating. It survived only because keyless was
// unavailable; keyless is built now, so a laptop runs the same verifier against a
// local trust root and a local allowlist. The boundary is data, not code, and a
// deployed host refuses a locally signed document through the ORDINARY check that
// refuses any unlisted identity — not a mode flag, not a coordinate comparison,
// and not a code path that exists only to be disabled.

// SolutionHostTrustPolicy is how a delivered carrier's bundle is checked.
type SolutionHostTrustPolicy string

// SolutionHostTrustKeyless verifies a Sigstore bundle against the mirrored trust
// root and the identity allowlist on the trust mount. It is the only policy.
const SolutionHostTrustKeyless SolutionHostTrustPolicy = "keyless"

// ErrSolutionHostTrustUnavailable reports a trust policy that is named and
// understood but cannot be built on this deployment.
var ErrSolutionHostTrustUnavailable = errors.New("solution host trust policy unavailable")

// solutionHostBundleVerifierWithPolicy is a verifier that also declares the
// signer-to-domain mapping it was built from.
//
// The two travel together because they come from the same independently
// delivered document. When the mapping lived in a workspace environment variable
// instead, a deployer who could set the environment could widen what an accepted
// signer speaks for without touching the policy at all — the allowlist was
// independent and the thing it authorized was not.
type solutionHostBundleVerifierWithPolicy interface {
	solutionhost.BundleVerifier

	// SignerDomains maps each attested signer identity to the ownership domains
	// it may deliver under, in the shape core's Host takes.
	SignerDomains() map[string][]string
}

// NewSolutionHostBundleVerifier builds the verifier for a named trust policy.
//
// The coordinate is no longer a parameter, and its absence is the point: when a
// policy's validity depended on the coordinate, the coordinate was an
// operator-declared string doing security work. Nothing here is decided by it
// now — what a host accepts is decided entirely by the trust root and the
// allowlist it was delivered.
// SolutionHostTrustAnchorPath is where the trust root and the verification
// policy are read from, and it is a CONSTANT on purpose.
//
// It was `SOLUTION_HOST_TRUST_MOUNT`, a workspace environment value — and that
// was a hole, not a convenience. Workspace environment is delivered by the
// COMPOSITION, so a composition could repoint the verifier at a policy and a
// root it wrote itself and then sign its own presence documents with a key the
// policy it also wrote happened to list. Every check downstream still passed; it
// was checking against an anchor the attacker chose.
//
// "A document never nominates its own anchor" cannot be enforced by a check,
// because the thing being checked is where the check's own inputs come from. It
// has to hold BY CONSTRUCTION, which means the path is not configurable — not by
// environment, not by flag, not by a default a deployer can override. A
// deployment that needs a different path changes the pod spec that mounts it,
// which is the platform's to render and not the composition's to supply.
//
// The anchor is therefore reachable only by whoever can mount into this pod.
// That is the property, and the constant is what makes it one.
const SolutionHostTrustAnchorPath = "/etc/obin/delivery-trust"

// RequireSolutionHostTrustAnchor refuses when the trust anchor is not mounted.
//
// It exists so the ABSENCE OF THE ANCHOR IS A BOOT FAILURE THAT NAMES THE
// ANCHOR. Verification already fails without it — the keyless verifier cannot
// read a root it does not have — but the refusal an operator reads then depends
// on which configuration value was consulted first, and an unmounted anchor was
// reported as a missing policy setting. Those send an operator to two different
// places, and only one of them is where the problem is.
//
// It takes no path, for the reason SolutionHostTrustAnchorPath is a constant: a
// function that could be told where to look is a function that will be told,
// and the location of the check's own inputs is exactly what must not be
// anyone's choice. A deployment that needs a different path changes the pod spec
// that mounts it.
func RequireSolutionHostTrustAnchor() error {
	info, err := os.Stat(SolutionHostTrustAnchorPath)
	if err != nil {
		return fmt.Errorf("%w: the delivery trust anchor is not mounted at %s: %w; "+
			"this host verifies every delivered carrier against the root and the identity allowlist delivered "+
			"there, so starting without it would mean serving while claiming to verify",
			ErrSolutionHostTrustRootUnavailable, SolutionHostTrustAnchorPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory, and the trust anchor is two documents in one: "+
			"%s and %s", ErrSolutionHostTrustRootUnavailable, SolutionHostTrustAnchorPath,
			trustedRootFileName, verificationPolicyFileName)
	}
	return nil
}

// NewSolutionHostBundleVerifier builds the one verifier, reading its anchor from
// the fixed path.
//
// It takes no path parameter. An optional path is a configurable path with extra
// steps: a caller that could pass one is a caller that could be given one.
func NewSolutionHostBundleVerifier(
	policy SolutionHostTrustPolicy,
) (solutionHostBundleVerifierWithPolicy, error) {
	switch policy {
	case SolutionHostTrustKeyless:
		// Refuses at BOOT when the trust mount has no root or no policy. That
		// refusal IS the correct behaviour: a host configured for a policy it
		// cannot perform must not start and claim to verify, because refusing
		// per document instead makes "this host has no trust root" and "delivery
		// is shipping something bad" the same observable.
		return NewSolutionHostKeylessVerifier(SolutionHostTrustAnchorPath)

	case "":
		return nil, fmt.Errorf(
			"SOLUTION_HOST_TRUST_POLICY is required wherever this host answers for a coordinate, and the only value is %q: "+
				"a host must say how it checks a delivered carrier rather than inherit a default",
			SolutionHostTrustKeyless)
	}
	return nil, fmt.Errorf("unknown solution host trust policy %q; the only policy is %q — "+
		"a second one that verified less is what this host deleted", policy, SolutionHostTrustKeyless)
}
