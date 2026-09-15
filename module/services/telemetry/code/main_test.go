package main

import (
	"context"
	"testing"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// TestResolveExporterConfigPrefersWorkspaceValue is the regression test for the
// boundary that broke in deployment: workspace configuration reaches the pod
// under prefixed names only, so reading the bare name returned "" and the
// collector silently downgraded itself to the debug exporter.
func TestResolveExporterConfigPrefersWorkspaceValue(t *testing.T) {
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_EXPORTER", "otlphttp")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OTEL_EXPORTER_OTLP_ENDPOINT",
		"http://otel-collector.otel-collector.svc.cluster.local:4318")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OTEL_EXPORTER_OTLP_HEADERS", "")
	// The bare names are what main.go used to read; they must lose to the
	// prefixed workspace values above.
	t.Setenv("OBSERVABILITY_EXPORTER", "debug")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	require.NoError(t, codefly.LoadEnvironmentVariables())

	config := resolveExporterConfig(context.Background())
	require.Equal(t, "otlphttp", config.Exporter)
	require.Equal(t, "http://otel-collector.otel-collector.svc.cluster.local:4318", config.Endpoint)
}

// TestResolveExporterConfigFallsBackToProcessEnvironment keeps a local or
// non-Codefly run working: with no workspace configuration injected, the bare
// process variables are still honoured.
func TestResolveExporterConfigFallsBackToProcessEnvironment(t *testing.T) {
	t.Setenv("OBSERVABILITY_EXPORTER", "debug")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	require.NoError(t, codefly.LoadEnvironmentVariables())

	config := resolveExporterConfig(context.Background())
	require.Equal(t, "debug", config.Exporter)
	require.Empty(t, config.Endpoint)
}
