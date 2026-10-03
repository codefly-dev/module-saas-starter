package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/solutionhost"
)

// The host's attestation check over a delivered carrier (issue #952).
//
// core holds no verifier and never will, and the reason is worth repeating here
// because it shapes this file: signing is KEYLESS over a workload identity, so
// verifying is a trust root plus an identity allowlist that the deployment owns.
// A library every binary imports cannot hold either.
//
// So this is the host's half, and it is a POLICY CHOICE made at boot, named
// explicitly, with no default. An unset policy refuses to start rather than
// picking one: every possible default is wrong somewhere. Trusting delivery is
// wrong in production, and refusing everything is wrong on a laptop, so the
// operator says which.
//
// What a verifier returns is the SIGNER IDENTITY, not a yes. The host maps that
// identity to the ownership domains it may deliver under
// (SOLUTION_HOST_SIGNER_DOMAINS), and core refuses a document whose asserted
// domain the attested signer may not speak for. That mapping is why a verifier
// that answered a bare boolean would be unusable: "this is signed" does not say
// "by someone entitled to this name".

// SolutionHostTrustPolicy is how a delivered carrier's bundle is checked.
type SolutionHostTrustPolicy string

const (
	// SolutionHostTrustKeyless verifies a Sigstore bundle against a trust root
	// and an identity allowlist. This is the production policy.
	SolutionHostTrustKeyless SolutionHostTrustPolicy = "keyless"

	// SolutionHostTrustLocal performs NO cryptographic verification and attests
	// every carrier as one configured local identity. It exists for a laptop,
	// where delivery is a directory the developer wrote and there is no
	// workflow identity to attest.
	//
	// It is usable only on a LOCAL COORDINATE, and that is enforced rather than
	// documented: see NewSolutionHostBundleVerifier. A policy that trusts the
	// mount is sound exactly when the mount and the host are the same person,
	// and the coordinate is the only thing that says so.
	SolutionHostTrustLocal SolutionHostTrustPolicy = "local"
)

// ErrSolutionHostTrustUnavailable reports a trust policy that is named and
// understood but cannot be built on this deployment.
var ErrSolutionHostTrustUnavailable = errors.New("solution host trust policy unavailable")

// localCoordinatePrefixes are the coordinates SolutionHostTrustLocal is allowed
// on. A coordinate is an operator-declared string, so this is a deliberate,
// narrow allowlist rather than a pattern: anything not named here is treated as
// a real environment, which is the fail-closed direction.
var localCoordinatePrefixes = []string{"local/", "localhost/"}

// SolutionHostLocalSignerIdentity is the identity SolutionHostTrustLocal attests
// every carrier as. An operator puts it in SOLUTION_HOST_SIGNER_DOMAINS against
// the domains a local developer may deliver under, so even the local policy
// still goes through the signer-to-domain check rather than around it.
const SolutionHostLocalSignerIdentity = "local"

// localBundleVerifier attests every carrier as SolutionHostLocalSignerIdentity.
type localBundleVerifier struct{}

// VerifyBundle accepts any bundle. It reads neither the payload nor the bundle,
// and says so by ignoring both: a reader should not have to check whether this
// looks at the bytes.
func (localBundleVerifier) VerifyBundle(context.Context, []byte, json.RawMessage) (string, error) {
	return SolutionHostLocalSignerIdentity, nil
}

// NewSolutionHostBundleVerifier builds the verifier for a named trust policy on
// a named coordinate.
//
// The coordinate is a parameter because one policy's validity depends on it.
// Checking it here rather than at the call site is the difference between an
// invariant and a convention.
func NewSolutionHostBundleVerifier(
	policy SolutionHostTrustPolicy, coordinate, trustMount string, localDomains []string,
) (solutionHostBundleVerifierWithPolicy, error) {
	switch policy {
	case SolutionHostTrustLocal:
		if !isLocalSolutionHostCoordinate(coordinate) {
			return nil, fmt.Errorf(
				"trust policy %q is only usable on a local coordinate, and %q is not one (expected one of %s); "+
					"a policy that trusts whatever is in the mount is sound only where the mount and the host are the same person",
				policy, coordinate, strings.Join(localCoordinatePrefixes, ", "))
		}
		if len(localDomains) == 0 {
			return nil, fmt.Errorf("trust policy %q needs the ownership domains the local identity may deliver under; "+
				"the local policy still goes through the signer-to-domain check rather than around it", policy)
		}
		return localVerifierDomains{domains: localDomains}, nil

	case SolutionHostTrustKeyless:
		// Built now, and it refuses at BOOT when the trust mount has no root or
		// no policy. That refusal IS the correct behaviour: a host configured
		// for a policy it cannot perform must not start and claim to verify,
		// because refusing per document instead makes "this host has no trust
		// root" and "delivery is shipping something bad" the same observable.
		return NewSolutionHostKeylessVerifier(trustMount)

	case "__unreachable_keyless_placeholder":
		// Deliberately not implemented rather than implemented untested.
		//
		// Keyless verification needs a Sigstore trust root and an identity
		// allowlist — repository, workflow path, ref pattern, issuer — that are
		// provisioned independently of both delivery writers, because a
		// deployer who can edit the allowlist does not need to forge a
		// signature: it can replace the verifier. Nothing on this deployment
		// provisions either yet, so a verifier written here could not be
		// exercised against a single real bundle before shipping, in the one
		// service that owns identity.
		//
		// This refuses at BOOT, not per document. A host configured for a
		// policy it cannot perform does not start and says why, rather than
		// starting and refusing every document as if delivery were broken.
		return nil, fmt.Errorf(
			"%w: %q needs a Sigstore trust root and an identity allowlist provisioned independently of delivery, "+
				"and this deployment provisions neither; the declared-presence paths are not live, so configure %q on a local coordinate or leave the mount unset",
			ErrSolutionHostTrustUnavailable, policy, SolutionHostTrustLocal)

	case "":
		return nil, errors.New(
			"SOLUTION_HOST_TRUST_POLICY is required with the mount: a host must say how it checks a delivered carrier, " +
				"because every default is wrong somewhere — trusting delivery in production, or refusing everything on a laptop")
	}
	return nil, fmt.Errorf("unknown solution host trust policy %q, want %q or %q",
		policy, SolutionHostTrustKeyless, SolutionHostTrustLocal)
}

// isLocalSolutionHostCoordinate reports whether a coordinate names a local
// environment.
func isLocalSolutionHostCoordinate(coordinate string) bool {
	for _, prefix := range localCoordinatePrefixes {
		if strings.HasPrefix(coordinate, prefix) {
			return true
		}
	}
	return false
}

// solutionHostBundleVerifierWithPolicy is a verifier that also declares the
// signer-to-domain mapping it was built from.
//
// The two travel together because they come from the same independently
// delivered document. When the mapping lived in a workspace environment
// variable instead, a deployer who could set the environment could widen what an
// accepted signer speaks for without touching the policy at all — the allowlist
// was independent and the thing it authorized was not.
type solutionHostBundleVerifierWithPolicy interface {
	solutionhost.BundleVerifier

	// SignerDomains maps each attested signer identity to the ownership domains
	// it may deliver under, in the shape core's Host takes.
	SignerDomains() map[string][]string
}

// localVerifierDomains is the local policy's mapping: the one local identity,
// against the domains the operator declared for it.
//
// The local policy still goes THROUGH the signer-to-domain check rather than
// around it. A local developer who declares one ownership domain cannot deliver
// under another by editing a document, which keeps the two policies the same
// shape and means a test written against one is meaningful against the other.
type localVerifierDomains struct {
	localBundleVerifier
	domains []string
}

func (l localVerifierDomains) SignerDomains() map[string][]string {
	return map[string][]string{SolutionHostLocalSignerIdentity: append([]string(nil), l.domains...)}
}
