package infra

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// Keyless verification of a delivered carrier: the production trust policy.
//
// There is no key. Delivery signs with a workload identity through an OIDC
// provider, Fulcio issues a short-lived certificate for it, and the signature
// plus that certificate plus a transparency-log entry are the bundle. So
// verifying is not "do I hold the right key" but two separate questions:
//
//  1. Is this bundle cryptographically sound against a TRUST ROOT — the Fulcio
//     chain that may issue, the Rekor key that may witness, the CT log keys?
//  2. Is the identity in its certificate one this host accepts — which
//     repository, which workflow, which ref, which issuer?
//
// Both inputs are the deployment's, and NEITHER may come from the composition's
// own tree. A deployer who can edit the identity allowlist does not need to
// forge a signature: it can replace the verifier. That is why the root and the
// policy are read from a mount a platform-owned delivery path writes, and why a
// delivered document can never nominate its own anchor — the policy is not in
// the tree the document arrives from.
//
// WHAT THIS RETURNS is the attested signer identity, not a yes. The host then
// maps that identity to the ownership domains it may deliver under, and core
// refuses a document whose asserted domain the attested signer may not speak
// for. A verifier answering a bare boolean would be unusable: "this is signed"
// does not say "by someone entitled to this name".

// ErrNoTransparencyEvidence reports a bundle carrying no transparency-log entry
// at all — neither an inclusion promise nor an inclusion proof.
//
// It is DISTINCT from a signature failure, and the distinction is the whole
// reason this error exists: a signer configured without transparency logging is
// a misconfiguration to go and fix, while a signature that does not verify is an
// attack to go and investigate. They reach an operator looking identical
// otherwise, and the first is far more likely.
var ErrNoTransparencyEvidence = errors.New("solution host carrier has no transparency-log evidence")

// ErrSolutionHostTrustRootUnavailable reports that the mirrored trust root or
// the verification policy could not be read from the trust mount.
var ErrSolutionHostTrustRootUnavailable = errors.New("solution host trust root unavailable")

// Filenames inside the trust mount. Fixed rather than configurable: the mount is
// written by one platform-owned path, and a configurable name would be one more
// thing a deployer could point somewhere else.
const (
	trustedRootFileName        = "trusted_root.json"
	verificationPolicyFileName = "verification_policy.json"
)

// SolutionHostVerificationPolicy is the identity allowlist: which signers this
// host accepts, and under which ownership domains each may deliver.
//
// It is delivered as a document rather than assembled from environment
// variables because it is a structure — a signer is four matchers and a domain
// list, not a string — and because it must arrive through the same
// independently-controlled path as the trust root. An earlier shape read a bare
// certificate SAN out of one env var, which bakes a weaker allowlist in: a SAN
// with no issuer, no repository and no ref accepts any workflow in any
// repository that can obtain a certificate for that identity.
type SolutionHostVerificationPolicy struct {
	// Signers is the allowlist. A carrier is accepted when it matches at least
	// one entry WHOLE — every field an entry names must match.
	Signers []SolutionHostSigner `json:"signers"`
}

// SolutionHostSigner is one accepted signer identity, and the domains it speaks
// for.
type SolutionHostSigner struct {
	// Name is how this host refers to the signer afterwards: it is the identity
	// VerifyBundle returns, and the key the signer-to-domain mapping uses. It is
	// the host's own label, deliberately not read out of the certificate — a
	// certificate field would change shape with the provider.
	Name string `json:"name"`

	// Issuer is the OIDC issuer that must have authenticated the workload.
	// Required: without it the allowlist accepts a matching SAN from any issuer,
	// and anyone who can stand up an issuer can mint that SAN.
	Issuer string `json:"issuer"`

	// SubjectAlternativeName is the workflow identity the certificate must
	// carry, exactly. Required.
	SubjectAlternativeName string `json:"subjectAlternativeName"`

	// SourceRepositoryURI, BuildConfigURI and SourceRepositoryRef narrow it
	// further, and are matched exactly when present. Each is optional in the
	// document and IndependentlyRequired is what decides whether leaving one out
	// is allowed at all — see Validate.
	SourceRepositoryURI string `json:"sourceRepositoryUri,omitempty"`
	BuildConfigURI      string `json:"buildConfigUri,omitempty"`
	SourceRepositoryRef string `json:"sourceRepositoryRef,omitempty"`

	// Domains are the ownership domains this signer may deliver under. core
	// refuses a document whose asserted domain is not in this list for the
	// attested signer, which is what stops an accepted signer from speaking for
	// a slice of the binding space it was not given.
	Domains []string `json:"domains"`
}

// Validate refuses a policy that would accept more than its author meant.
//
// Every refusal here is a case where the document parses and reads as a policy
// while admitting far more than it appears to. They are checked at BOOT, so a
// policy that cannot do its job stops the host rather than quietly widening it.
func (policy *SolutionHostVerificationPolicy) Validate() error {
	if policy == nil || len(policy.Signers) == 0 {
		return errors.New("verification policy declares no signers, so no carrier could ever be accepted; " +
			"an empty allowlist is refused rather than treated as 'accept none', because it reads as a policy that works")
	}
	names := map[string]bool{}
	for index, signer := range policy.Signers {
		switch {
		case strings.TrimSpace(signer.Name) == "":
			return fmt.Errorf("verification policy signer %d declares no name, and the name is the identity this host attributes a delivery to", index)
		case names[signer.Name]:
			return fmt.Errorf("verification policy declares signer %q twice; which entry's domains apply would depend on ordering", signer.Name)
		case strings.TrimSpace(signer.Issuer) == "":
			return fmt.Errorf("verification policy signer %q declares no issuer: a subject alternative name with no issuer is accepted from ANY issuer, "+
				"so anyone who can stand up an issuer can mint that identity", signer.Name)
		case strings.TrimSpace(signer.SubjectAlternativeName) == "":
			return fmt.Errorf("verification policy signer %q declares no subject alternative name", signer.Name)
		case strings.TrimSpace(signer.SourceRepositoryURI) == "" && strings.TrimSpace(signer.BuildConfigURI) == "":
			// One of the two is required. A workflow identity alone is not a
			// narrow claim: the same workflow path exists in every fork of a
			// repository, so an allowlist naming only the SAN accepts a fork's
			// run of the same file.
			return fmt.Errorf("verification policy signer %q names neither a source repository nor a build config: "+
				"a workflow identity alone matches that same workflow path in any fork, so the repository is what makes the claim narrow", signer.Name)
		case len(signer.Domains) == 0:
			return fmt.Errorf("verification policy signer %q may deliver under no ownership domain, so accepting it could not admit anything; "+
				"remove the entry or give it a domain", signer.Name)
		}
		names[signer.Name] = true
	}
	return nil
}

// keylessBundleVerifier verifies a Sigstore bundle against a mirrored trust root
// and an identity allowlist, offline.
type keylessBundleVerifier struct {
	verifier *verify.Verifier
	policy   *SolutionHostVerificationPolicy
}

// NewSolutionHostKeylessVerifier builds the keyless verifier from a trust mount.
//
// It refuses at BOOT when the mount is absent or unreadable, and that is the
// correct behaviour rather than a limitation: a host configured for a policy it
// cannot perform must not start and claim to be verifying. Refusing per document
// instead would make "this host has no trust root" and "delivery is shipping
// something bad" the same observable, which are the two facts an operator most
// needs to tell apart.
func NewSolutionHostKeylessVerifier(directory string) (solutionHostBundleVerifierWithPolicy, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("%w: no trust mount is configured, so there is no root to verify against",
			ErrSolutionHostTrustRootUnavailable)
	}

	rootBytes, err := os.ReadFile(filepath.Join(directory, trustedRootFileName))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s from the trust mount %q: %w; "+
			"the mirrored Fulcio/Rekor root is delivered by a platform-owned path, independently of either delivery writer",
			ErrSolutionHostTrustRootUnavailable, trustedRootFileName, directory, err)
	}
	trustedRoot, err := root.NewTrustedRootFromJSON(rootBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %s in the trust mount is not a usable trusted root: %w",
			ErrSolutionHostTrustRootUnavailable, trustedRootFileName, err)
	}

	policyBytes, err := os.ReadFile(filepath.Join(directory, verificationPolicyFileName))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s from the trust mount %q: %w; "+
			"a trust root without an identity allowlist would accept any signer Fulcio ever issued for",
			ErrSolutionHostTrustRootUnavailable, verificationPolicyFileName, directory, err)
	}
	policy := &SolutionHostVerificationPolicy{}
	decoder := json.NewDecoder(strings.NewReader(string(policyBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(policy); err != nil {
		return nil, fmt.Errorf("%w: %s in the trust mount does not decode: %w; "+
			"decoding is strict, so a field this host does not model is an error rather than a silently ignored intention",
			ErrSolutionHostTrustRootUnavailable, verificationPolicyFileName, err)
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionHostTrustRootUnavailable, err)
	}

	return NewSolutionHostKeylessVerifierFrom(trustedRoot, policy)
}

// NewSolutionHostKeylessVerifierFrom builds the verifier over already-loaded
// trust material and an already-validated policy.
//
// It exists so the DECISION is separable from the I/O. Reading a mount and
// deciding whether a carrier is acceptable are different concerns with different
// failure modes, and keeping them in one function would have made the decision
// testable only through files — which is how a verifier ends up exercised
// against no real bundle at all. Tests drive this entry point with an in-process
// Sigstore, so the same code path that production uses is the one under test.
func NewSolutionHostKeylessVerifierFrom(
	trustedMaterial root.TrustedMaterial, policy *SolutionHostVerificationPolicy,
) (solutionHostBundleVerifierWithPolicy, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionHostTrustRootUnavailable, err)
	}
	// WithTransparencyLog(1) and WithObserverTimestamps(1), and deliberately NO
	// online verification: the delivered namespaces are default-deny egress, so
	// an online Rekor or Fulcio lookup would HANG rather than fail fast — a
	// staging timeout instead of a permission error in a test. The bundle
	// carries its own evidence, so the lookup is unnecessary as well as
	// unreachable.
	verifier, err := verify.NewVerifier(trustedMaterial,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot build the bundle verifier: %w", ErrSolutionHostTrustRootUnavailable, err)
	}
	return &keylessBundleVerifier{verifier: verifier, policy: policy}, nil
}

// VerifyBundle checks one carrier and returns the attested signer's name.
//
// The payload is passed separately from the bundle because the bundle attests a
// DIGEST: this verifies that the signature covers exactly these bytes, not that
// some bytes were signed by someone acceptable.
func (v *keylessBundleVerifier) VerifyBundle(
	_ context.Context, payload []byte, rawBundle json.RawMessage,
) (string, error) {
	signed := &bundle.Bundle{}
	if err := signed.UnmarshalJSON(rawBundle); err != nil {
		return "", fmt.Errorf("carrier bundle does not decode as a Sigstore bundle: %w", err)
	}
	return v.verifySignedEntity(signed, payload)
}

// verifySignedEntity is the decision, over anything that presents as a signed
// entity. Separated from the JSON decode so a test can drive it with an
// in-process Sigstore rather than a hand-assembled bundle document.
func (v *keylessBundleVerifier) verifySignedEntity(
	signed verify.SignedEntity, payload []byte,
) (string, error) {
	// BEFORE the verifier runs, and on the decoded bundle rather than by
	// classifying the verifier's error.
	//
	// This ordering is load-bearing and the obvious implementation is the wrong
	// one. The verifier reports a bundle with no log entry as "not enough
	// verified log entries from transparency log: 0 < 1" — the SAME words it
	// reports for a bundle whose log key this trust root does not hold. Those
	// are opposite faults: one is a signer that never logged, the other is a
	// root that cannot check the log it did. A classifier reading that string
	// cannot tell them apart, and would hand the second case a message telling
	// an operator to go fix their signer.
	//
	// An RFC 3161 signed timestamp does NOT stand in. It attests WHEN something
	// was signed, not THAT it was logged, so a bundle carrying one still has
	// nothing an inclusion can be checked against — which is exactly the case a
	// looser "has it got any evidence at all?" test admits silently.
	if !signed.HasInclusionPromise() && !signed.HasInclusionProof() {
		return "", fmt.Errorf("%w: neither an inclusion promise nor an inclusion proof; "+
			"a signer that does not log cannot be verified offline against a mirrored root, and this is NOT a signature failure",
			ErrNoTransparencyEvidence)
	}

	digest := sha256.Sum256(payload)
	artifactPolicy := verify.WithArtifactDigest("sha256", digest[:])

	// Each allowlist entry is tried WHOLE. A carrier is accepted when it
	// satisfies one entry completely, never by matching fields from several —
	// which is what a single combined identity policy with per-field alternation
	// would allow: this repository's issuer with that repository's workflow.
	var failures []string
	for _, signer := range v.policy.Signers {
		identity, err := signer.certificateIdentity()
		if err != nil {
			// A policy that validated at boot cannot normally fail here, so
			// this is a defect rather than a rejection — and it must not read
			// as "this carrier was not signed by an accepted signer".
			return "", fmt.Errorf("verification policy signer %q cannot be compiled into an identity matcher: %w", signer.Name, err)
		}
		if _, err := v.verifier.Verify(signed,
			verify.NewPolicy(artifactPolicy, verify.WithCertificateIdentity(identity))); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", signer.Name, err))
			continue
		}
		return signer.Name, nil
	}
	return "", fmt.Errorf("carrier matched no accepted signer in the verification policy (%d tried): %s",
		len(v.policy.Signers), strings.Join(failures, "; "))
}

// SignerDomains is the attested-signer-to-ownership-domain mapping this policy
// declares, in the shape core's Host takes.
//
// It comes from the SAME document as the identity allowlist deliberately. When
// the two were separate — the allowlist in the trust mount and the domains in a
// workspace environment variable — a deployer who could set the environment
// could widen what an accepted signer speaks for without touching the
// independently-delivered policy at all.
func (v *keylessBundleVerifier) SignerDomains() map[string][]string {
	domains := make(map[string][]string, len(v.policy.Signers))
	for _, signer := range v.policy.Signers {
		domains[signer.Name] = append([]string(nil), signer.Domains...)
	}
	return domains
}

// certificateIdentity compiles one allowlist entry into a matcher.
//
// Exact values throughout, with no regular expressions reachable from the
// document. A regex in an allowlist is a widening waiting to happen — `.*` in a
// ref pattern accepts every branch, and reads at a glance like a configured
// constraint.
func (signer SolutionHostSigner) certificateIdentity() (verify.CertificateIdentity, error) {
	san, err := verify.NewSANMatcher(signer.SubjectAlternativeName, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuer, err := verify.NewIssuerMatcher(signer.Issuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	// The issuer goes in the IssuerMatcher and NOT in the extensions: sigstore-go
	// refuses it in both ("please specify issuer in IssuerMatcher, not
	// Extensions") rather than silently preferring one, which is the right call
	// — two places to state one constraint is two places for them to disagree.
	return verify.NewCertificateIdentity(san, issuer, certificate.Extensions{
		SourceRepositoryURI: signer.SourceRepositoryURI,
		BuildConfigURI:      signer.BuildConfigURI,
		SourceRepositoryRef: signer.SourceRepositoryRef,
	})
}
