//go:build pure

package infra

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"
)

// The ref prefix is ENFORCED against the verified certificate, not merely
// validated in the policy.
//
// In-package because `refMatches` is private, and private is right: it runs only
// after `Verify` succeeds, on the certificate the result reports. The external
// suite covers the policy's validation rules; this covers the comparison, and
// the two are different claims. A mutation disabling the comparison passed every
// validation test — which is exactly why this file exists.
func TestRefPrefixIsEnforcedAgainstTheVerifiedCertificate(t *testing.T) {
	signer := SolutionHostSigner{
		Name:                      "platform-delivery",
		SourceRepositoryRefPrefix: "refs/tags/",
	}
	resultWithRef := func(ref string) *verify.VerificationResult {
		return &verify.VerificationResult{
			Signature: &verify.SignatureVerificationResult{
				Certificate: &certificate.Summary{
					Extensions: certificate.Extensions{SourceRepositoryRef: ref},
				},
			},
		}
	}

	// Any release tag is admitted — the whole point, since a tombstone is signed
	// under whatever tag is current rather than the one the binding arrived on.
	require.Empty(t, signer.refMatches(resultWithRef("refs/tags/v1.0.0")))
	require.Empty(t, signer.refMatches(resultWithRef("refs/tags/v9.9.9-rc1")))

	// A branch is NOT a release tag, and this is the case the exact-ref pin was
	// protecting: without the prefix check the signer would admit it.
	refused := signer.refMatches(resultWithRef("refs/heads/main"))
	require.NotEmpty(t, refused, "a branch must not pass a refs/tags/ prefix")
	require.Contains(t, refused, "refs/heads/main")
	require.Contains(t, refused, "refs/tags/")

	// A ref that merely CONTAINS the prefix is refused: HasPrefix is anchored,
	// and a substring match here would admit `refs/heads/refs/tags/evil`.
	require.NotEmpty(t, signer.refMatches(resultWithRef("refs/heads/refs/tags/v1")))

	// A verified result reporting no certificate REFUSES rather than skipping. A
	// rule that silently does not run is worse than one that refuses, because
	// the policy still reads as though the ref were constrained.
	require.NotEmpty(t, signer.refMatches(&verify.VerificationResult{}))
	require.NotEmpty(t, signer.refMatches(nil))

	// A signer declaring no prefix is unconstrained on this axis — the policy's
	// own Validate decides whether that is allowed, not this comparison.
	require.Empty(t, SolutionHostSigner{Name: "n"}.refMatches(resultWithRef("refs/heads/main")))
}
