package infra_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"accounts/pkg/infra"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/require"
)

// Keyless verification, exercised against a real in-process Sigstore.
//
// Every test here is about a way the host could admit a document nobody
// entitled attested, and each is driven through the same decision production
// takes. A verifier tested only against hand-written bundle JSON proves its
// decoder works; these prove its policy does.

const (
	testIssuer     = "https://token.example.test"
	testIdentity   = "https://github.test/acme/delivery/.github/workflows/publish.yaml@refs/heads/main"
	testRepository = "https://github.test/acme/delivery"
)

// acceptedPolicy is a whole, valid production allowlist: it names the source
// repository, which is what makes a workflow identity a narrow claim.
func acceptedPolicy() *infra.SolutionHostVerificationPolicy {
	return &infra.SolutionHostVerificationPolicy{
		Signers: []infra.SolutionHostSigner{{
			Name:                   "delivery",
			Issuer:                 testIssuer,
			SubjectAlternativeName: testIdentity,
			SourceRepositoryURI:    testRepository,
			Domains:                []string{"acme"},
		}},
	}
}

// reachablePolicy is acceptedPolicy with the repository dropped, and it is only
// reachable through the unvalidated test constructor.
//
// The in-process Sigstore mints leaf certificates carrying ONLY the OIDC issuer
// extension, so no certificate it can produce satisfies a policy naming a source
// repository. Rather than weaken the production requirement to make a test pass,
// the accept path is driven with this shape and the production refusal of it is
// asserted separately in TestVerificationPolicyRefusesWhatWouldAdmitTooMuch.
//
// What that leaves genuinely uncovered here: that the repository and ref
// extensions are compared at all. That comparison is sigstore-go's and is
// exercised by its own suite, not by this one.
func reachablePolicy() *infra.SolutionHostVerificationPolicy {
	policy := acceptedPolicy()
	policy.Signers[0].SourceRepositoryURI = ""
	return policy
}

// keylessVerifierFor builds a verifier over the in-process Sigstore, bypassing
// the policy's production validation for the reason reachablePolicy documents.
func keylessVerifierFor(t *testing.T, sigstore *ca.VirtualSigstore) any {
	t.Helper()
	verifier, err := infra.NewKeylessVerifierWithoutPolicyValidation(sigstore, reachablePolicy())
	require.NoError(t, err)
	return verifier
}

// A sound bundle from an allowlisted identity is accepted, and the SIGNER'S NAME
// comes back — not a boolean. The host maps that name to the ownership domains
// the signer may speak for, so a verifier answering yes/no would be unusable:
// "this is signed" does not say "by someone entitled to this name".
func TestKeylessVerifierAcceptsAnAllowlistedSigner(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(`{"schema":"codefly/solution-host-binding/v1"}`)
	entity, err := sigstore.Sign(testIdentity, testIssuer, payload)
	require.NoError(t, err)

	verifier := keylessVerifierFor(t, sigstore)
	signer, err := infra.VerifySignedEntity(verifier, entity, payload)
	require.NoError(t, err)
	require.Equal(t, "delivery", signer)
	require.Equal(t, map[string][]string{"delivery": {"acme"}}, infra.KeylessSignerDomains(verifier))
}

// The signature covers THESE bytes. A bundle that verifies over one payload must
// not verify over another: without binding the digest, an attacker could present
// a genuine attestation beside a document it never covered.
//
// BOTH directions are asserted in one test, deliberately. The negative half
// alone is satisfied by a verifier that errors for any reason at all — and a
// first draft of this test did exactly that: it passed against a mutation that
// removed the digest binding entirely, because the replacement policy option
// failed to construct and the test saw "an error" and was content. Asserting
// that the same bundle DOES verify over its own bytes is what makes the failure
// attributable to the bytes rather than to anything that happens to break.
func TestKeylessVerifierRefusesABundleForDifferentBytes(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	signed := []byte(`{"generation":1}`)
	entity, err := sigstore.Sign(testIdentity, testIssuer, signed)
	require.NoError(t, err)
	verifier := keylessVerifierFor(t, sigstore)

	signer, err := infra.VerifySignedEntity(verifier, entity, signed)
	require.NoError(t, err, "the control: this bundle must verify over the bytes it covers")
	require.Equal(t, "delivery", signer)

	_, err = infra.VerifySignedEntity(verifier, entity, []byte(`{"generation":99}`))
	require.Error(t, err, "an attestation over other bytes must not admit this payload")
}

// An identity nobody allowlisted is refused, even though the bundle is
// cryptographically sound and issued by the very same Fulcio.
//
// This is the finding the trust model rests on: a trust root says who MAY issue,
// never who may deliver. Without the allowlist, every identity that CA ever
// certified could speak for this host's bindings.
func TestKeylessVerifierRefusesAnUnlistedIdentity(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(`{"generation":1}`)
	entity, err := sigstore.Sign(
		"https://github.test/attacker/delivery/.github/workflows/publish.yaml@refs/heads/main",
		testIssuer, payload)
	require.NoError(t, err)

	verifier := keylessVerifierFor(t, sigstore)
	_, err = infra.VerifySignedEntity(verifier, entity, payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "matched no accepted signer")
}

// The same workflow identity from a DIFFERENT issuer is refused.
//
// A subject alternative name with no issuer is accepted from any issuer, so
// anyone who can stand up an OIDC provider can mint that identity. The policy
// requires the issuer for exactly this reason, and this is the test that would
// fail if the requirement were relaxed to "the SAN is enough".
func TestKeylessVerifierRefusesTheRightIdentityFromTheWrongIssuer(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(`{"generation":1}`)
	entity, err := sigstore.Sign(testIdentity, "https://attacker-issuer.example.test", payload)
	require.NoError(t, err)

	verifier := keylessVerifierFor(t, sigstore)
	_, err = infra.VerifySignedEntity(verifier, entity, payload)
	require.Error(t, err, "the allowlisted SAN from an unlisted issuer must be refused")
}

// A bundle verified under a root that did NOT issue it is refused. The trust
// root is the anchor, so a verifier that accepted this would be checking
// arithmetic rather than provenance.
func TestKeylessVerifierRefusesABundleFromAnotherRoot(t *testing.T) {
	signing, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	other, err := ca.NewVirtualSigstore()
	require.NoError(t, err)

	payload := []byte(`{"generation":1}`)
	entity, err := signing.Sign(testIdentity, testIssuer, payload)
	require.NoError(t, err)

	verifier := keylessVerifierFor(t, other)
	_, err = infra.VerifySignedEntity(verifier, entity, payload)
	require.Error(t, err, "a bundle from a different trust root must be refused")
}

// A carrier may not satisfy one allowlist entry's issuer and another's
// repository. Each entry is tried WHOLE.
//
// A single combined identity policy with per-field alternation would admit the
// cross product, which is strictly more than any entry's author approved.
func TestKeylessVerifierDoesNotCombineFieldsAcrossAllowlistEntries(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(`{"generation":1}`)
	entity, err := sigstore.Sign(testIdentity, testIssuer, payload)
	require.NoError(t, err)

	// Two entries, neither of which the carrier satisfies whole: the first has
	// the right issuer with the wrong identity, the second the right identity
	// with the wrong issuer.
	policy := &infra.SolutionHostVerificationPolicy{Signers: []infra.SolutionHostSigner{
		{
			Name: "right-issuer-wrong-identity", Issuer: testIssuer,
			SubjectAlternativeName: "https://github.test/acme/other/.github/workflows/publish.yaml@refs/heads/main",
			SourceRepositoryURI:    testRepository, Domains: []string{"acme"},
		},
		{
			Name: "right-identity-wrong-issuer", Issuer: "https://other-issuer.example.test",
			SubjectAlternativeName: testIdentity,
			SourceRepositoryURI:    testRepository, Domains: []string{"acme"},
		},
	}}
	verifier, err := infra.NewKeylessVerifierWithoutPolicyValidation(sigstore, policy)
	require.NoError(t, err)

	_, err = infra.VerifySignedEntity(verifier, entity, payload)
	require.Error(t, err, "fields must not be satisfiable across different allowlist entries")
}

// ---------------------------------------------------------------------------
// The transparency-evidence refusal, and WHERE the check goes
// ---------------------------------------------------------------------------

// A bundle with no transparency-log entry is refused BY NAME, not as a signature
// failure.
//
// The ordering is the point. sigstore-go reports a missing entry as "not enough
// verified log entries from transparency log: 0 < 1" — the same words it reports
// for a bundle whose log key the trust root does not hold. Those are opposite
// faults: a signer that never logged is a misconfiguration to fix, a root that
// cannot check the log is a deployment problem, and a bad signature is an attack.
// A classifier reading that string would tell an operator to fix the wrong one.
func TestKeylessVerifierRefusesNoTransparencyEvidenceByName(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(`{"generation":1}`)
	entity, err := sigstore.Sign(testIdentity, testIssuer, payload)
	require.NoError(t, err)

	verifier := keylessVerifierFor(t, sigstore)
	_, err = infra.VerifySignedEntity(verifier, withoutTransparency{entity}, payload)
	require.ErrorIs(t, err, infra.ErrNoTransparencyEvidence)
	require.Contains(t, err.Error(), "NOT a signature failure",
		"the refusal must say what it is not, because the two are confused by default")
}

// An RFC 3161 signed timestamp does not stand in for transparency evidence: it
// attests WHEN something was signed, not THAT it was logged. This is the case a
// looser "has it got any evidence at all?" check admits silently — the field is
// populated, just not the field that answers the question.
func TestKeylessVerifierRefusesATimestampStandingInForTransparency(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(`{"generation":1}`)
	entity, err := sigstore.Sign(testIdentity, testIssuer, payload)
	require.NoError(t, err)

	verifier, err := infra.NewSolutionHostKeylessVerifierFrom(sigstore, acceptedPolicy())
	require.NoError(t, err)

	timestamped := withoutTransparency{entity}
	stamps, stampErr := timestamped.Timestamps()
	require.NoError(t, stampErr)
	require.NotEmpty(t, stamps,
		"the fixture must actually carry a signed timestamp, or this test proves nothing")

	_, err = infra.VerifySignedEntity(verifier, timestamped, payload)
	require.ErrorIs(t, err, infra.ErrNoTransparencyEvidence)
}

// withoutTransparency is a signed entity whose transparency-log entries have
// been dropped, keeping everything else — signature, certificate, timestamps.
//
// Dropping them rather than building a bundle from scratch is deliberate: the
// entity is otherwise exactly one that verifies, so a test using it cannot pass
// for some unrelated reason.
type withoutTransparency struct {
	infra.VerifySignedEntityInput
}

func (withoutTransparency) HasInclusionPromise() bool { return false }
func (withoutTransparency) HasInclusionProof() bool   { return false }

// ---------------------------------------------------------------------------
// The policy document, and the boot-time refusals
// ---------------------------------------------------------------------------

// Each of these is a policy that parses and reads as a policy while admitting
// far more than its author meant, so each is refused at BOOT rather than per
// document.
func TestVerificationPolicyRefusesWhatWouldAdmitTooMuch(t *testing.T) {
	for name, policy := range map[string]*infra.SolutionHostVerificationPolicy{
		"no signers": {},
		"no issuer": {Signers: []infra.SolutionHostSigner{{
			Name: "delivery", SubjectAlternativeName: testIdentity,
			SourceRepositoryURI: testRepository, Domains: []string{"acme"},
		}}},
		"no repository or build config": {Signers: []infra.SolutionHostSigner{{
			Name: "delivery", Issuer: testIssuer, SubjectAlternativeName: testIdentity,
			Domains: []string{"acme"},
		}}},
		"no domains": {Signers: []infra.SolutionHostSigner{{
			Name: "delivery", Issuer: testIssuer, SubjectAlternativeName: testIdentity,
			SourceRepositoryURI: testRepository,
		}}},
		"no subject alternative name": {Signers: []infra.SolutionHostSigner{{
			Name: "delivery", Issuer: testIssuer,
			SourceRepositoryURI: testRepository, Domains: []string{"acme"},
		}}},
		"no name": {Signers: []infra.SolutionHostSigner{{
			Issuer: testIssuer, SubjectAlternativeName: testIdentity,
			SourceRepositoryURI: testRepository, Domains: []string{"acme"},
		}}},
		"duplicate name": {Signers: []infra.SolutionHostSigner{
			{
				Name: "delivery", Issuer: testIssuer, SubjectAlternativeName: testIdentity,
				SourceRepositoryURI: testRepository, Domains: []string{"acme"},
			},
			{
				Name: "delivery", Issuer: testIssuer, SubjectAlternativeName: testIdentity,
				SourceRepositoryURI: testRepository, Domains: []string{"beta"},
			},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, policy.Validate())
		})
	}
	require.NoError(t, acceptedPolicy().Validate(), "the accepted policy must still validate")
}

// Keyless refuses at BOOT when the trust mount has no root, and that refusal is
// the correct behaviour rather than a gap.
//
// A host configured for a policy it cannot perform must not start and claim to
// verify: refusing per document instead makes "this host has no trust root" and
// "delivery is shipping something bad" the same observable, which are the two
// facts an operator most needs to tell apart.
// Driven through NewSolutionHostKeylessVerifier, which still takes a directory,
// rather than through NewSolutionHostBundleVerifier, which no longer does.
//
// The policy-level constructor now reads the fixed anchor path, and asserting
// refuse-at-boot through it would be asserting that a SYSTEM path is absent on
// the machine running the test — true on a workstation and silently untrue on a
// host that has the mount. A test whose verdict depends on the environment it
// runs in is not evidence of this property.
func TestKeylessRefusesAtBootWithoutATrustRoot(t *testing.T) {
	verifier, err := infra.NewSolutionHostKeylessVerifier(t.TempDir())
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.Nil(t, verifier, "an unusable policy must yield no verifier")
	require.Contains(t, err.Error(), "trusted_root.json")
}

// A trust root with no identity allowlist beside it is refused: it would accept
// any signer that Fulcio ever issued a certificate for.
func TestKeylessRefusesARootWithNoPolicy(t *testing.T) {
	directory := t.TempDir()
	writeTrustRoot(t, directory)

	// Same reason as above: the directory is the unit under test here, so this
	// goes through the keyless constructor rather than the policy dispatcher.
	_, err := infra.NewSolutionHostKeylessVerifier(directory)
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.Contains(t, err.Error(), "verification_policy.json")
}

// Decoding the policy is STRICT: a field the host does not model is an error,
// never a silently ignored intention. A policy carrying `allowAnyIssuer: true`
// must not be read as the policy without it.
func TestVerificationPolicyDecodingIsStrict(t *testing.T) {
	directory := t.TempDir()
	writeTrustRoot(t, directory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "verification_policy.json"),
		[]byte(`{"signers":[{"name":"d","issuer":"i","subjectAlternativeName":"s","sourceRepositoryUri":"r","domains":["acme"],"allowAnyIssuer":true}]}`),
		0o600))

	_, err := infra.NewSolutionHostKeylessVerifier(directory)
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.Contains(t, err.Error(), "does not decode")
}

// writeTrustRoot puts a syntactically valid trusted root in the mount. Its
// contents do not matter to the tests that use it — they are about the policy
// beside it — but it must parse, or the failure under test would be the root's.
func writeTrustRoot(t *testing.T, directory string) {
	t.Helper()
	root := map[string]any{
		"mediaType":              "application/vnd.dev.sigstore.trustedroot+json;version=0.1",
		"certificateAuthorities": []any{},
		"tlogs":                  []any{},
		"ctlogs":                 []any{},
		"timestampAuthorities":   []any{},
	}
	encoded, err := json.Marshal(root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "trusted_root.json"), encoded, 0o600))
}

// A ref prefix admits every release tag, because an exact ref makes withdrawal
// impossible.
//
// The failure it prevents, end to end: a binding is delivered under release tag
// A; withdrawing it needs a tombstone, which is signed under whatever tag is
// current — B; with the ref pinned to A that tombstone fails verification; and
// since a tombstone is the only way to withdraw and nothing deletes from the
// inbox, the binding stays applied forever. The CLI reports the same shape from
// its side.
func TestARefPrefixAdmitsEveryReleaseTag(t *testing.T) {
	policy := &infra.SolutionHostVerificationPolicy{Signers: []infra.SolutionHostSigner{{
		Name:                      "platform-delivery",
		Issuer:                    "https://token.actions.githubusercontent.com",
		SubjectAlternativeName:    "https://example.test/acme/infra/.github/workflows/deliver.yml@refs/tags/v2",
		SourceRepositoryURI:       "https://example.test/acme/infra",
		SourceRepositoryRefPrefix: "refs/tags/",
		Domains:                   []string{"acme"},
	}}}
	require.NoError(t, policy.Validate(), "a ref prefix under refs/ is a valid narrowing")
}

// An exact ref and a prefix together are refused, because the exact one wins and
// the prefix reads as though it had widened something.
func TestAnExactRefAndAPrefixTogetherAreRefused(t *testing.T) {
	policy := &infra.SolutionHostVerificationPolicy{Signers: []infra.SolutionHostSigner{{
		Name:                      "platform-delivery",
		Issuer:                    "https://token.actions.githubusercontent.com",
		SubjectAlternativeName:    "https://example.test/acme/infra/.github/workflows/deliver.yml@refs/tags/v2",
		SourceRepositoryURI:       "https://example.test/acme/infra",
		SourceRepositoryRef:       "refs/tags/v2",
		SourceRepositoryRefPrefix: "refs/tags/",
		Domains:                   []string{"acme"},
	}}}
	err := policy.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "would win")
}

// A prefix outside the ref namespace is refused: "" admits every ref and "r"
// admits half the ref space while looking like a constraint.
func TestAnUnanchoredRefPrefixIsRefused(t *testing.T) {
	for _, prefix := range []string{"r", "v", "tags/", "/refs/", "refs"} {
		policy := &infra.SolutionHostVerificationPolicy{Signers: []infra.SolutionHostSigner{{
			Name:                      "platform-delivery",
			Issuer:                    "https://token.actions.githubusercontent.com",
			SubjectAlternativeName:    "https://example.test/acme/infra/.github/workflows/deliver.yml@refs/tags/v2",
			SourceRepositoryURI:       "https://example.test/acme/infra",
			SourceRepositoryRefPrefix: prefix,
			Domains:                   []string{"acme"},
		}}}
		require.Error(t, policy.Validate(), "ref prefix %q must be refused", prefix)
	}
}
