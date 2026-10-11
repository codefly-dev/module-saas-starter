package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/codefly-dev/core/solutionhost"
)

// ErrSolutionHostTrustRootUnavailable reports that the platform-owned anchor
// could not supply a usable verification policy. Refusing at boot distinguishes
// a host with no trust policy from a delivered document that fails its checks.
var ErrSolutionHostTrustRootUnavailable = errors.New("solution host trust anchor unavailable")

const verificationPolicyFileName = "verification_policy.json"

// SolutionHostVerificationPolicy carries keys and their domains together so a
// composition cannot widen a signer's authority through workspace environment.
type SolutionHostVerificationPolicy struct {
	Local   *SolutionHostLocalProfile `json:"local,omitempty"`
	Signers []SolutionHostSigner      `json:"signers"`
}

const SolutionHostSignerPerson = "person"

// SolutionHostSigner is a platform-owned pin, never trust material supplied by
// a carrier. Name is the host's label; attribution and domain policy use KeyID.
type SolutionHostSigner struct {
	Name      string               `json:"name"`
	Kind      solutionhost.KeyKind `json:"kind"`
	KeyID     string               `json:"keyId"`
	PublicKey string               `json:"publicKey"`
	Domains   []string             `json:"domains"`
}

// Validate refuses policy mistakes at boot rather than accepting a document
// under a policy that means something different from what its author wrote.
func (policy *SolutionHostVerificationPolicy) Validate() error {
	_, err := policy.pinnedKeys()
	return err
}

func (policy *SolutionHostVerificationPolicy) pinnedKeys() (*solutionhost.PinnedKeySet, error) {
	if policy == nil {
		return nil, errors.New("verification policy declares no signers")
	}
	if policy.Local != nil {
		if err := policy.Local.validate(); err != nil {
			return nil, err
		}
	}
	names := map[string]bool{}
	pins := make([]solutionhost.PinnedKey, 0, len(policy.Signers))
	for index, signer := range policy.Signers {
		if strings.TrimSpace(signer.Name) == "" {
			return nil, fmt.Errorf("verification policy signer %d declares no name", index)
		}
		if names[signer.Name] {
			return nil, fmt.Errorf("verification policy declares signer %q twice", signer.Name)
		}
		names[signer.Name] = true
		if signer.Kind == solutionhost.KeyKindPerson && policy.Local == nil {
			return nil, fmt.Errorf("verification policy signer %q is a person, admitted only by a policy that declares itself local and proves its cluster", signer.Name)
		}
		pins = append(pins, solutionhost.PinnedKey{KeyID: signer.KeyID, PublicKeyPEM: signer.PublicKey, Kind: signer.Kind, OwnershipDomains: signer.Domains})
	}
	// Core owns key, kind, ID and domain validation, so signing and receiving
	// cannot drift into two interpretations of the same pin.
	return solutionhost.NewPinnedKeySet(pins...)
}

// keyBundleVerifier holds only local trust material. Verification needs no
// HTTP client: the platform supplies the pins independently of either writer.
type keyBundleVerifier struct {
	verifier           *solutionhost.Verifier
	pins               *solutionhost.PinnedKeySet
	local              *SolutionHostLocalProfile
	localClusterProven atomic.Bool
}

// NewSolutionHostKeyVerifier reads the one policy document at the constant
// anchor. A path parameter here would let composition nominate its own trust.
func NewSolutionHostKeyVerifier() (solutionHostBundleVerifierWithPolicy, error) {
	return readSolutionHostKeyVerifier(SolutionHostTrustAnchorPath)
}

// The file reader is private so only tests can substitute a temporary mount.
func readSolutionHostKeyVerifier(directory string) (solutionHostBundleVerifierWithPolicy, error) {
	data, err := os.ReadFile(filepath.Join(directory, verificationPolicyFileName))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s from the trust mount %q: %w", ErrSolutionHostTrustRootUnavailable, verificationPolicyFileName, directory, err)
	}
	var policy SolutionHostVerificationPolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("%w: %s does not decode: %w", ErrSolutionHostTrustRootUnavailable, verificationPolicyFileName, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s must contain exactly one policy document", ErrSolutionHostTrustRootUnavailable, verificationPolicyFileName)
	}
	return newSolutionHostKeyVerifier(&policy)
}

func newSolutionHostKeyVerifier(policy *SolutionHostVerificationPolicy) (solutionHostBundleVerifierWithPolicy, error) {
	pins, err := policy.pinnedKeys()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionHostTrustRootUnavailable, err)
	}
	verifier, err := solutionhost.NewVerifier(pins)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionHostTrustRootUnavailable, err)
	}
	v := &keyBundleVerifier{verifier: verifier, pins: pins}
	if policy.Local != nil {
		local := *policy.Local
		v.local = &local
	}
	return v, nil
}

// Verifier exposes core's full delivery verification only after the local
// cluster proof. Returning no verifier before then means a forgotten boot call
// refuses carriers instead of admitting a person's key in another cluster.
func (v *keyBundleVerifier) Verifier() (*solutionhost.Verifier, error) {
	if v.local != nil && !v.localClusterProven.Load() {
		return nil, ErrSolutionHostLocalPolicyOutsideItsCluster
	}
	return v.verifier, nil
}

// SignerDomains derives attribution from the same immutable pins verification
// uses, so changing a caller's policy or this returned map cannot change trust.
func (v *keyBundleVerifier) SignerDomains() map[string][]string { return v.pins.DomainsBySigner() }

// ProveLocalCluster proves a local policy is on its own cluster, and from then
// on lets it admit carriers. A deployed policy is bound to no cluster.
func (v *keyBundleVerifier) ProveLocalCluster(ctx context.Context, cluster SolutionHostClusterAuthority) error {
	if v.local == nil {
		return nil
	}
	if cluster == nil {
		return fmt.Errorf("%w: the verification policy is a local profile and this host has no cluster to prove it against", ErrSolutionHostLocalPolicyOutsideItsCluster)
	}
	if err := cluster.BindToAuthority(ctx, v.local.ClusterCertificateAuthoritySHA256); err != nil {
		return fmt.Errorf("the verification policy on the trust anchor is a local profile bound to the cluster whose certificate authority is %s, and this host is not running in it — a host in any other cluster refuses a local profile: %w", v.local.ClusterCertificateAuthoritySHA256, err)
	}
	v.localClusterProven.Store(true)
	return nil
}
