package main

import (
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

// R1019-N12/f1: APP_BASE_URL was validated at boot by url.Parse plus field rules and
// at request time by IDNA canonicalization. An origin the first accepted and the
// second could not canonicalize made a cell boot green and then refuse every
// authentication — sign-in, the OAuth redirect, the relying-party origin, emailed
// links and the solution proxy's same-origin check — with nothing at startup saying so.
//
// The assertion is the EQUIVALENCE, not a list of bad hosts: whatever
// CanonicalPublicOrigin refuses, boot must refuse. A second validator cannot drift
// back in without failing this.
func TestR1019StartupRefusesAnyOriginRequestTimeCannotCanonicalize(t *testing.T) {
	for _, candidate := range []string{
		"https://app.example",     // usable
		"https://app.example:443", // usable, explicit default port
		"https://my_app.example",  // usable: an underscore is a legal host for a browser
		"https://-app.example",    // NOT usable: a label may not begin with a hyphen
		"https://app-.example",    // NOT usable: nor end with one
		"https://app.example:0",   // NOT usable: port 0 serves nothing
		"https://:443",            // NOT usable: no host
	} {
		t.Run(candidate, func(t *testing.T) {
			t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__APP_BASE_URL", candidate)
			t.Setenv("APP_BASE_URL", candidate)

			_, requestTime := auth.CanonicalPublicOrigin(candidate)
			_, boot := configuredApplicationBaseURL()

			if requestTime != nil {
				require.Error(t, boot,
					"request time cannot canonicalize %q, so boot must refuse it rather than "+
						"start a cell whose every origin comparison fails", candidate)
				return
			}
			require.NoError(t, boot,
				"request time canonicalizes %q, so boot must not refuse a usable origin", candidate)
		})
	}
}

// The raw spelling is what boot returns, because it is also the token issuer and the
// published authorization-server metadata, where a verifier compares it exactly.
// Canonicalizing it here would silently change `iss` for a deployment whose configured
// origin carries an explicit default port.
func TestR1019StartupKeepsTheConfiguredSpellingForTheIssuer(t *testing.T) {
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__APP_BASE_URL", "https://app.example:443")
	t.Setenv("APP_BASE_URL", "https://app.example:443")

	base, err := configuredApplicationBaseURL()
	require.NoError(t, err)
	require.Equal(t, "https://app.example:443", base,
		"the issuer keeps the operator's spelling; only the comparison canonicalizes")
}
