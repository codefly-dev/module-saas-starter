package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clearGatewayTrustEnvironment empties every source workspaceEnv reads the
// gateway-trust group from, so an ambient value cannot decide a case.
func clearGatewayTrustEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CODEFLY_GATEWAY_TOKEN",
		"CODEFLY_GATEWAY_TOKEN_PREVIOUS",
		"CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT",
	} {
		t.Setenv(key, "")
		for _, group := range []string{"GATEWAY-TRUST", "GATEWAY_TRUST"} {
			t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__"+group+"__"+key, "")
			t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__"+group+"__"+key, "")
		}
	}
}

func setGatewayTrust(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__GATEWAY-TRUST__"+key, value)
}

var gatewayTrustNow = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

func TestConfiguredGatewayTrustWithoutRotation(t *testing.T) {
	clearGatewayTrustEnvironment(t)
	setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN", "current-gateway-token")
	trust, err := configuredGatewayTrust(gatewayTrustNow)
	require.NoError(t, err)
	require.Equal(t, gatewayTrust{current: "current-gateway-token"}, trust)

	// An expiry left behind after the previous slot was emptied bounds nothing.
	setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT", "2020-01-01T00:00:00Z")
	trust, err = configuredGatewayTrust(gatewayTrustNow)
	require.NoError(t, err)
	require.Equal(t, gatewayTrust{current: "current-gateway-token"}, trust)
}

func TestConfiguredGatewayTrustAcceptsABoundedRotation(t *testing.T) {
	for name, test := range map[string]struct {
		expiresAt string
		want      time.Time
	}{
		"an hour ahead":            {"2026-03-01T13:00:00Z", gatewayTrustNow.Add(time.Hour)},
		"the whole window":         {"2026-03-02T12:00:00Z", gatewayTrustNow.Add(maxGatewayRotationWindow)},
		"an offset and a fraction": {"2026-03-01T14:30:00.5+02:00", gatewayTrustNow.Add(30*time.Minute + 500*time.Millisecond)},
	} {
		t.Run(name, func(t *testing.T) {
			clearGatewayTrustEnvironment(t)
			setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN", "current-gateway-token")
			setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN_PREVIOUS", "previous-gateway-token")
			setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT", test.expiresAt)
			trust, err := configuredGatewayTrust(gatewayTrustNow)
			require.NoError(t, err)
			require.Equal(t, "current-gateway-token", trust.current)
			require.Equal(t, "previous-gateway-token", trust.previous)
			require.True(t, test.want.Equal(trust.previousExpiresAt), "got %s", trust.previousExpiresAt)
		})
	}
}

// A previous gateway credential keeps the authority to assert any identity to
// accounts, so accounts refuses to start with one that has no end, an end it
// cannot read, an end already behind it, an end further ahead than one
// rotation needs, or one that is simply the current credential again.
func TestConfiguredGatewayTrustRefusesAnUnboundedRotation(t *testing.T) {
	for name, test := range map[string]struct {
		previous  string
		expiresAt string
		refusal   string
	}{
		"no expiry":                 {"previous-gateway-token", "", "is required"},
		"a blank expiry":            {"previous-gateway-token", "   ", "is required"},
		"a space for the T":         {"previous-gateway-token", "2026-03-01 13:00:00Z", "RFC 3339"},
		"a date without a time":     {"previous-gateway-token", "2026-03-01", "RFC 3339"},
		"unix seconds":              {"previous-gateway-token", "1772370000", "RFC 3339"},
		"a duration":                {"previous-gateway-token", "1h", "RFC 3339"},
		"no offset":                 {"previous-gateway-token", "2026-03-01T13:00:00", "RFC 3339"},
		"an expiry already past":    {"previous-gateway-token", "2026-03-01T11:59:59Z", "has passed"},
		"an expiry of now":          {"previous-gateway-token", "2026-03-01T12:00:00Z", "has passed"},
		"an expiry past the window": {"previous-gateway-token", "2026-03-02T12:00:01Z", "no more than 24h ahead"},
		"the current credential":    {"current-gateway-token", "2026-03-01T13:00:00Z", "must differ"},
	} {
		t.Run(name, func(t *testing.T) {
			clearGatewayTrustEnvironment(t)
			setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN", "current-gateway-token")
			setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN_PREVIOUS", test.previous)
			setGatewayTrust(t, "CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT", test.expiresAt)
			trust, err := configuredGatewayTrust(gatewayTrustNow)
			require.ErrorContains(t, err, "CODEFLY_GATEWAY_TOKEN_PREVIOUS")
			require.ErrorContains(t, err, test.refusal)
			require.Equal(t, gatewayTrust{}, trust, "a refused configuration installs nothing")
			require.NotContains(t, err.Error(), test.previous, "the refusal never echoes a credential")
		})
	}
}
