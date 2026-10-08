package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceEnvResolvesConfigurationSecretAndFallback(t *testing.T) {
	const key = "CODEFLY_GATEWAY_TOKEN"
	configurationKey := "CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL-AUTH__" + key
	secretKey := "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL-AUTH__" + key
	legacySecretKey := "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL_AUTH__" + key

	t.Setenv(key, "plain")
	t.Setenv(configurationKey, "")
	t.Setenv(secretKey, "")
	t.Setenv(legacySecretKey, "legacy-secret")
	require.Equal(t, "legacy-secret", workspaceEnv("internal-auth", key))

	t.Setenv(legacySecretKey, "")
	require.Equal(t, "plain", workspaceEnv("internal-auth", key))

	t.Setenv(secretKey, "secret")
	require.Equal(t, "secret", workspaceEnv("internal-auth", key))

	t.Setenv(configurationKey, "configuration")
	require.Equal(t, "configuration", workspaceEnv("internal-auth", key))
}

func TestObservabilityEnabledRequiresExternalOTLPEndpoint(t *testing.T) {
	const key = "OTEL_EXPORTER_OTLP_ENDPOINT"
	configurationKey := "CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__" + key
	secretKey := "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__OBSERVABILITY__" + key
	t.Setenv(key, "")
	t.Setenv(configurationKey, "")
	t.Setenv(secretKey, "")
	require.False(t, observabilityEnabled())

	t.Setenv(key, "  ")
	require.False(t, observabilityEnabled())
	t.Setenv(key, "https://otel.example")
	require.True(t, observabilityEnabled())

	t.Setenv(key, "")
	t.Setenv(configurationKey, "https://workspace-otel.example")
	require.True(t, observabilityEnabled())
}
