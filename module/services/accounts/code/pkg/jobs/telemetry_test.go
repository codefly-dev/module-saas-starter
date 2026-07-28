package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestWorkerTelemetryExportsOTelMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	telemetry, err := newWorkerTelemetry(provider.Meter("jobs-test"))
	require.NoError(t, err)

	ctx := context.Background()
	telemetry.recordPoll(ctx, "analytics", "success", 2)
	telemetry.addActive(ctx, "analytics", 1)
	telemetry.addActive(ctx, "analytics", -1)
	telemetry.recordProcess(ctx, "analytics", "succeeded", time.Now().Add(-time.Millisecond))

	var exported metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &exported))
	names := map[string]bool{}
	for _, scope := range exported.ScopeMetrics {
		for _, metric := range scope.Metrics {
			names[metric.Name] = true
		}
	}
	require.Equal(t, map[string]bool{
		"saas.jobs.polls":     true,
		"saas.jobs.claimed":   true,
		"saas.jobs.active":    true,
		"saas.jobs.completed": true,
		"saas.jobs.duration":  true,
	}, names)
}
