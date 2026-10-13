package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codefly-dev/core/solutionhost"
)

// The host owns the pins, while core owns the cryptographic checks. Keeping
// those responsibilities separate means a delivered document cannot choose the
// key that authenticates it or the ownership domains that key may speak for.
// Local delivery uses the same checks; only a cluster-bound policy admits a
// person's key. The cluster client proves that binding at boot.

// SolutionHostTrustPolicy is how a delivered carrier's envelope is checked.
type SolutionHostTrustPolicy string

// SolutionHostTrustKeys verifies against the platform-owned pinned public keys.
// It is the only policy, so local delivery cannot silently verify less.
const SolutionHostTrustKeys SolutionHostTrustPolicy = "keys"

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
	// Verifier is unavailable until a local policy has proven its cluster.
	Verifier() (*solutionhost.Verifier, error)

	// SignerDomains maps each attested signer identity to the ownership domains
	// it may deliver under, in the shape core's Host takes.
	SignerDomains() map[string][]string

	// ProveLocalCluster binds a LOCAL policy to the cluster it was written for:
	// the cluster client must hold exactly the pinned certificate authority,
	// reach an API server under it that authenticates this pod's own token,
	// and keep that pinned transport for every later call. It refuses — and the
	// boot must stop — when it cannot, which is how a host in any other cluster
	// refuses a local profile. A deployed policy is bound to no cluster and this
	// does nothing. Until it succeeds a local policy admits no carrier.
	ProveLocalCluster(ctx context.Context, cluster SolutionHostClusterAuthority) error
}

// SolutionHostTrustAnchorPath is where the verification policy is read from,
// and it is a CONSTANT on purpose. What a host accepts is decided entirely by
// the pinned keys delivered there, never by an operator-declared coordinate.
//
// It was `SOLUTION_HOST_TRUST_MOUNT`, a workspace environment value — and that
// was a hole, not a convenience. Workspace environment is delivered by the
// COMPOSITION, so a composition could repoint the verifier at a policy it
// wrote itself and sign its own presence documents with a key that policy
// happened to list. Every check downstream still passed; it
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

// LocalTrustAnchorEnvironment names the trust anchor's directory when this host
// is NOT a pod — the same developer run LocalClusterEnvironment describes, which
// writes the local profile there. The constant above stays the only place a pod
// reads its anchor from, for the reason given above it: IN A POD THIS VARIABLE
// IS REFUSED, so nothing a composition can set moves a cell's anchor. Outside a
// pod, nobody mounts anything; the anchor it names is a local profile, which
// the host accepts only after proving it runs in the cluster that profile is
// bound to.
const LocalTrustAnchorEnvironment = "SOLUTION_HOST_LOCAL_TRUST_ANCHOR"

// ErrLocalTrustAnchorInAPod refuses a relocated anchor named inside a cluster.
var ErrLocalTrustAnchorInAPod = errors.New("a pod may not relocate its delivery trust anchor")

// solutionHostTrustAnchorDir is where this process reads its anchor from.
func solutionHostTrustAnchorDir() (string, error) {
	return trustAnchorDir(os.Getenv(LocalTrustAnchorEnvironment), InCluster())
}

// trustAnchorDir is solutionHostTrustAnchorDir over facts already read, so a
// test drives the refusal through this code.
func trustAnchorDir(local string, inPod bool) (string, error) {
	local = strings.TrimSpace(local)
	if local == "" {
		return SolutionHostTrustAnchorPath, nil
	}
	if inPod {
		return "", fmt.Errorf("%w: %s is set and this process carries what a kubelet puts in a pod; "+
			"a pod reads its anchor from %s and from nowhere else",
			ErrLocalTrustAnchorInAPod, LocalTrustAnchorEnvironment, SolutionHostTrustAnchorPath)
	}
	if !filepath.IsAbs(local) {
		return "", fmt.Errorf("%w: %s must be an absolute directory, got %q", ErrSolutionHostTrustRootUnavailable, LocalTrustAnchorEnvironment, local)
	}
	return local, nil
}

// RequireSolutionHostTrustAnchor refuses when the trust anchor is not mounted.
//
// It exists so the ABSENCE OF THE ANCHOR IS A BOOT FAILURE THAT NAMES THE
// ANCHOR. Verification already fails without it — the verifier cannot
// read pins it does not have — but the refusal an operator reads then depends
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
	dir, err := solutionHostTrustAnchorDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, verificationPolicyFileName)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: the delivery trust anchor %s is not mounted or readable: %w; "+
			"this host needs verification_policy.json with the platform-owned pinned keys before it can verify delivery",
			ErrSolutionHostTrustRootUnavailable, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular verification policy document", ErrSolutionHostTrustRootUnavailable, path)
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
	case SolutionHostTrustKeys:
		// Refuses at BOOT when the trust mount has no policy. That
		// refusal IS the correct behaviour: a host configured for a policy it
		// cannot perform must not start and claim to verify, because refusing
		// per document instead makes "this host has no pinned keys" and "delivery
		// is shipping something bad" the same observable.
		return NewSolutionHostKeyVerifier()

	case "keyless":
		return nil, fmt.Errorf("keyless signing was removed; the only solution host trust policy is %q", SolutionHostTrustKeys)
	case "":
		return nil, fmt.Errorf(
			"SOLUTION_HOST_TRUST_POLICY is required wherever this host answers for a coordinate, and the only value is %q: "+
				"a host must say how it checks a delivered carrier rather than inherit a default",
			SolutionHostTrustKeys)
	}
	return nil, fmt.Errorf("unknown solution host trust policy %q; the only policy is %q — "+
		"a second one that verified less is what this host deleted", policy, SolutionHostTrustKeys)
}
