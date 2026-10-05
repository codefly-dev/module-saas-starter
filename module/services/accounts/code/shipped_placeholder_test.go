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
	blankWorkspaceKey(t, "application", "APP_BASE_URL")

	err := requireApplicationBaseURL(false)
	require.Error(t, err, "a deployed runtime must refuse an unset APP_BASE_URL")
	require.Contains(t, err.Error(), "APP_BASE_URL",
		"the refusal must name the key an operator has to set")

	require.NoError(t, requireApplicationBaseURL(true),
		"local development pins no runtime port")
}

// Every perimeter credential and the application base URL are refused when a
// deployed runtime holds a placeholder, which is also what the shipped local
// defaults hold — so a test that drives these reads has to blank BOTH carriers.
// `codefly ci run` injects this module's own local groups as
// CODEFLY__WORKSPACE_CONFIGURATION__<GROUP>__<KEY> (and the secret form), while a
// bare `go test` injects none and only the plain fallback can carry a stray value.
// Blanking one passes locally and fails in CI, or the reverse.
func blankWorkspaceKey(t *testing.T, group, key string) {
	t.Helper()
	upper := strings.ToUpper(group)
	upper = strings.ReplaceAll(upper, "-", "_")
	t.Setenv(key, "")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__"+upper+"__"+key, "")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__"+upper+"__"+key, "")
}

// The deployed perimeter check over the REAL carriers, not only over the pure
// comparison: with the shipped local default in the group a deployed runtime
// refuses, and with a provisioned value it starts.
func TestDeployedPerimeterCheckReadsTheConfigurationGroup(t *testing.T) {
	for _, key := range []string{"CODEFLY_INTERNAL_TOKEN", "CODEFLY_INTERNAL_TOKEN_PREVIOUS"} {
		blankWorkspaceKey(t, "internal-auth", key)
	}
	for _, key := range []string{"CODEFLY_GATEWAY_TOKEN", "CODEFLY_GATEWAY_TOKEN_PREVIOUS"} {
		blankWorkspaceKey(t, "gateway-trust", key)
	}

	// Nothing provisioned: each loader's own required-or-optional question, not
	// this one's.
	require.NoError(t, requirePerimeterCredentials(false))

	// The value this module ships in its public local defaults.
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL_AUTH__CODEFLY_INTERNAL_TOKEN",
		"local-dev-only-replace-me")
	err := requirePerimeterCredentials(false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "internal-auth/CODEFLY_INTERNAL_TOKEN")

	// A provisioned one.
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL_AUTH__CODEFLY_INTERNAL_TOKEN",
		strings.Repeat("7Kq2Xp9Vb4Nf", 4))
	require.NoError(t, requirePerimeterCredentials(false))
}
