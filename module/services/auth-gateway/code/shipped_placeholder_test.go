package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The gateway decides perimeter membership on these two credentials and accepted
// a value published in this repository's own local defaults.
func TestDeployedRefusesShippedPlaceholders(t *testing.T) {
	for _, value := range []string{
		"local-dev-only-replace-me",
		"local-dev-gateway-only-replace-me",
		"LOCAL-DEV-GATEWAY-ONLY-REPLACE-ME",
		"prefixed-changeme-suffixed-and-long-enough-to-pass-the-length-floor",
	} {
		err := refusePerimeterCredential("gateway-trust/CODEFLY_GATEWAY_TOKEN", value)
		require.Error(t, err, "%q carries a shipped marker and must be refused", value)
		require.Contains(t, err.Error(), "gateway-trust/CODEFLY_GATEWAY_TOKEN")
		require.NotContains(t, err.Error(), value, "a refusal never prints the value")
	}
}

func TestDeployedRefusesAShortPerimeterCredential(t *testing.T) {
	err := refusePerimeterCredential("internal-auth/CODEFLY_INTERNAL_TOKEN", "7Kq2Xp9Vb4Nf")
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 32")
	require.NotContains(t, err.Error(), "7Kq2Xp9Vb4Nf")
}

func TestPerimeterCredentialChecksAdmitWhatTheyShould(t *testing.T) {
	require.NoError(t, refusePerimeterCredential(
		"gateway-trust/CODEFLY_GATEWAY_TOKEN", strings.Repeat("7Kq2Xp9Vb4Nf", 4)))
	require.NoError(t, refusePerimeterCredential(
		"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS", ""))
}

func TestLocalRuntimeKeepsTheShippedDefaults(t *testing.T) {
	require.NoError(t, requirePerimeterCredentials(true))
}

// Every credential the ext_authz trust surface reads is covered, so a third key
// added there cannot be unchecked while looking checked.
func TestPerimeterCredentialsCoverEveryTrustDecidingKey(t *testing.T) {
	names := map[string]bool{}
	for _, credential := range perimeterCredentials() {
		names[credential.Name] = true
	}
	require.Equal(t, map[string]bool{
		"internal-auth/CODEFLY_INTERNAL_TOKEN":          true,
		"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS": true,
		"gateway-trust/CODEFLY_GATEWAY_TOKEN":           true,
	}, names)
}
