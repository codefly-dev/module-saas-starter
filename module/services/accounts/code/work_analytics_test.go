package main

import (
	"testing"

	"accounts/pkg/analytics"

	"github.com/stretchr/testify/require"
)

func TestConfiguredAnalyticsSinkDefaultsToDisabledNoop(t *testing.T) {
	t.Setenv("PRODUCT_ANALYTICS_MODE", "")
	sink, enabled, err := configuredAnalyticsSink()
	require.NoError(t, err)
	require.False(t, enabled)
	require.IsType(t, analytics.NoopSink{}, sink)
}

func TestConfiguredAnalyticsSinkRequiresExplicitValidPostHogConfig(t *testing.T) {
	t.Setenv("PRODUCT_ANALYTICS_MODE", "posthog")
	_, _, err := configuredAnalyticsSink()
	require.ErrorContains(t, err, "API key")

	t.Setenv("POSTHOG_PROJECT_API_KEY", "project-key")
	t.Setenv("POSTHOG_HOST", "http://localhost:8080")
	_, _, err = configuredAnalyticsSink()
	require.ErrorContains(t, err, "personal API key")

	t.Setenv("POSTHOG_PERSONAL_API_KEY", "personal-key")
	t.Setenv("POSTHOG_PROJECT_ID", "42")
	sink, enabled, err := configuredAnalyticsSink()
	require.NoError(t, err)
	require.True(t, enabled)
	require.IsType(t, &analytics.PostHog{}, sink)

	t.Setenv("PRODUCT_ANALYTICS_MODE", "unexpected")
	_, _, err = configuredAnalyticsSink()
	require.ErrorContains(t, err, "disabled, noop, or posthog")
}
