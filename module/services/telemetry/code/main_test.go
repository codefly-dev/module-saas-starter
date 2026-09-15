package main

import (
	"context"
	"testing"

	"telemetry/collector"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// clearWorkspaceObservability removes every carrier the observability group can
// arrive under, so a test that means "no workspace configuration" actually gets
// it. Without this a test passes only because a neighbouring test happened to
// restore these first, and fails wherever they are genuinely exported — which is
// any process started by `codefly run`.
func clearWorkspaceObservability(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{
		"CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__OBSERVABILITY__",
	} {
		for _, key := range []string{
			"OBSERVABILITY_EXPORTER",
			"OTEL_EXPORTER_OTLP_ENDPOINT",
			"OTEL_EXPORTER_OTLP_HEADERS",
		} {
			t.Setenv(prefix+key, "")
		}
	}
}

// TestResolveExporterConfigPrefersWorkspaceValue is the regression test for the
// boundary that broke in deployment: workspace configuration reaches the pod
// under prefixed names only, so reading the bare name returned "" and the
// collector silently downgraded itself to the debug exporter.
func TestResolveExporterConfigPrefersWorkspaceValue(t *testing.T) {
	clearWorkspaceObservability(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_EXPORTER", "otlphttp")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OTEL_EXPORTER_OTLP_ENDPOINT",
		"https://collector.example")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__OBSERVABILITY__OTEL_EXPORTER_OTLP_HEADERS",
		"Authorization=Bearer+workspace")
	// The bare names are what main.go used to read; they must lose to the
	// prefixed workspace values above — including the header, which is the value
	// that carries the credential.
	t.Setenv("OBSERVABILITY_EXPORTER", "debug")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://wrong-backend.example")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer+ambient")
	require.NoError(t, codefly.LoadEnvironmentVariables())

	config := resolveExporterConfig(context.Background())
	require.Equal(t, "otlphttp", config.Exporter)
	require.Equal(t, "https://collector.example", config.Endpoint)
	require.Equal(t, "Authorization=Bearer+workspace", config.Headers)
}

// TestResolveExporterConfigTakesWholeTupleFromOneAuthority is the regression
// test for per-key resolution. The workspace group selects debug and says
// nothing about an endpoint or headers; the process environment carries both.
// Resolving key by key produced debug+endpoint — a combination collector.New
// refuses — so a developer who had exported these to drive
// scripts/setup/otel.sh could no longer start the collector at all. It also
// stopped an ambient OTEL_EXPORTER_OTLP_HEADERS credential from being attached
// to an endpoint it was never issued for.
func TestResolveExporterConfigTakesWholeTupleFromOneAuthority(t *testing.T) {
	clearWorkspaceObservability(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_EXPORTER", "debug")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://shell-backend.example")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer+ambient")
	require.NoError(t, codefly.LoadEnvironmentVariables())

	config := resolveExporterConfig(context.Background())
	require.Equal(t, "debug", config.Exporter)
	require.Empty(t, config.Endpoint, "endpoint must not be mixed in from the process environment")
	require.Empty(t, config.Headers, "an ambient credential must not reach a workspace-selected exporter")

	// The consequence the mixing produced: the collector refused to start.
	_, err := collector.New(config)
	require.NoError(t, err)
}

// TestResolveExporterConfigFallsBackToProcessEnvironment keeps a local or
// non-Codefly run working: with no workspace configuration injected, the bare
// process variables are still honoured — as a complete tuple.
func TestResolveExporterConfigFallsBackToProcessEnvironment(t *testing.T) {
	clearWorkspaceObservability(t)
	t.Setenv("OBSERVABILITY_EXPORTER", "otlphttp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer+ambient")
	require.NoError(t, codefly.LoadEnvironmentVariables())

	config := resolveExporterConfig(context.Background())
	require.Equal(t, "otlphttp", config.Exporter)
	require.Equal(t, "https://collector.example", config.Endpoint)
	require.Equal(t, "Authorization=Bearer+ambient", config.Headers)
}
