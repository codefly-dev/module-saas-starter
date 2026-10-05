package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A shipped default is a published value: these files ride verbatim in the
// immutable module package, out of a public repository. Outside local development
// a service holding one is a service whose perimeter credential everybody knows,
// and nothing said so.
func TestDeployedRefusesShippedPlaceholders(t *testing.T) {
	for _, value := range []string{
		"local-dev-only-replace-me",
		"local-dev-gateway-only-replace-me",
		"LOCAL-DEV-ONLY-REPLACE-ME",
		"prefixed-changeme-suffixed-and-long-enough-to-pass-the-length-floor",
		"a-placeholder-value-long-enough-to-pass-the-length-floor",
	} {
		err := refusePerimeterCredential("internal-auth/CODEFLY_INTERNAL_TOKEN", value)
		require.Error(t, err, "%q carries a shipped marker and must be refused", value)
		require.Contains(t, err.Error(), "internal-auth/CODEFLY_INTERNAL_TOKEN",
			"the refusal must name the key an operator has to provision")
		require.NotContains(t, err.Error(), value, "a refusal never prints the value")
	}
}

// The length floor refuses a short value that carries no marker at all, so the
// two checks are not one check spelled twice.
func TestDeployedRefusesAShortPerimeterCredential(t *testing.T) {
	err := refusePerimeterCredential("gateway-trust/CODEFLY_GATEWAY_TOKEN", "7Kq2Xp9Vb4Nf")
	require.Error(t, err)
	require.Contains(t, err.Error(), "gateway-trust/CODEFLY_GATEWAY_TOKEN")
	require.Contains(t, err.Error(), "at least 32")
	require.NotContains(t, err.Error(), "7Kq2Xp9Vb4Nf")
}

// The controls. A provisioned credential passes, and an ABSENT one is not refused
// here: whether a credential is required is each loader's own question, and
// answering it twice would make an optional group's absence read as a placeholder.
func TestPerimeterCredentialChecksAdmitWhatTheyShould(t *testing.T) {
	require.NoError(t, refusePerimeterCredential(
		"internal-auth/CODEFLY_INTERNAL_TOKEN", strings.Repeat("7Kq2Xp9Vb4Nf", 4)))
	require.NoError(t, refusePerimeterCredential(
		"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS", ""))
	require.NoError(t, refusePerimeterCredential(
		"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS", "   "))
}

// Local development runs on the shipped defaults, so the gate is the deployed
// runtime's and must not touch it.
func TestLocalRuntimeKeepsTheShippedDefaults(t *testing.T) {
	require.NoError(t, requirePerimeterCredentials(true))
}

// Every perimeter credential this service reads is covered. A fifth key added to
// the trust surface and not listed here would be unchecked and look checked.
func TestPerimeterCredentialsCoverEveryTrustDecidingKey(t *testing.T) {
	names := map[string]bool{}
	for _, credential := range perimeterCredentials() {
		names[credential.Name] = true
	}
	require.Equal(t, map[string]bool{
		"internal-auth/CODEFLY_INTERNAL_TOKEN":          true,
		"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS": true,
		"gateway-trust/CODEFLY_GATEWAY_TOKEN":           true,
		"gateway-trust/CODEFLY_GATEWAY_TOKEN_PREVIOUS":  true,
	}, names)
}

// A cell without a pinned public origin is a cell whose verified origin is a
// request parameter: unset, an OAuth redirect, the authenticator relying-party
// origin and every emailed link were bound to whatever origin the request's trusted
// hop forwarded — which that hop derived from the caller's own forwarding header.
func TestDeployedRequiresTheApplicationBaseURL(t *testing.T) {
	err := requireApplicationBaseURL(false)
	require.Error(t, err, "a deployed runtime must refuse an unset APP_BASE_URL")
	require.Contains(t, err.Error(), "APP_BASE_URL",
		"the refusal must name the key an operator has to set")

	require.NoError(t, requireApplicationBaseURL(true),
		"local development pins no runtime port")
}
