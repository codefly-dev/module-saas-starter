package main

import (
	"os"
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

// The boot refusals are only refusals if main WIRES them. Removing a call site leaves
// every unit test above green while a deployed gateway starts on a published
// credential, with no shared store for revocation or rate limits, and with no
// trusted-proxy range — so the wiring is asserted from the source.
//
// A fourth refusal stood here, for a durable module prefix-claim store. It is gone
// because its subject is: a module route is no longer claimed at runtime but read
// per request from the delivered registry (gateway_module_routes.go), which is
// shared and durable by construction and fails closed when no snapshot has loaded.
//
// Each is matched with codefly.IsLocal() specifically: wiring one of these to a
// constant, or to a request-derived value, is the same defect as not wiring it.
func TestR1019DeployedRefusalsAreWiredAtBoot(t *testing.T) {
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	text := string(source)

	for _, wiring := range []struct{ what, call string }{
		{"the shipped-placeholder refusal", "requirePerimeterCredentials(codefly.IsLocal())"},
		{"the trusted-proxy range refusal", "requireProxyTrust(workspaceEnv(\"gateway\", \"TRUSTED_PROXY_CIDRS\"), codefly.IsLocal())"},
		{"the revocation-store refusal", "newRevoker(redisURL, revocationTTL, codefly.IsLocal())"},
		{"the rate-limit store refusal", "WithInProcessBackendAllowed(codefly.IsLocal())"},
	} {
		require.Contains(t, text, wiring.call,
			"%s must be wired at boot from the runtime, not from a constant", wiring.what)
	}

	// And the refusal has to stop the process. A call whose error is ignored is not a
	// refusal; every one of these is fatal at boot.
	require.NotContains(t, text, "_ = requirePerimeterCredentials",
		"the placeholder refusal's error must not be discarded")
}

// The per-replica limiter is reachable only through a distinct constructor, so a
// deployed wiring cannot opt into it by passing a flag. This is the claim the review
// found overstated: the option EXISTS and any caller could pass it, so what holds is
// that the only production call site passes codefly.IsLocal() — which is what the
// source assertion above actually establishes.
func TestR1019InProcessLimiterIsNotTheProductionWiring(t *testing.T) {
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "WithInProcessBackendAllowed(true)",
		"production must not hard-enable the per-replica limiter")
	require.NotContains(t, string(source), "newInProcessRateLimiter(",
		"the per-replica constructor is for tests and local development only")
}
