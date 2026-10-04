package business

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type auditQueueSnapshotFake struct{ snapshot AuditQueueSnapshot }

func (f *auditQueueSnapshotFake) Snapshot(context.Context) (AuditQueueSnapshot, error) {
	return f.snapshot, nil
}

func TestAuditRelayMonitorReportsLagUntilTheQueueClears(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-6 * time.Minute)
	source := &auditQueueSnapshotFake{snapshot: AuditQueueSnapshot{Depth: 7, OldestEnqueuedAt: &oldest}}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })
	monitor, err := NewAuditRelayMonitor(source, provider.Meter("audit-relay-test"), time.Second)
	require.NoError(t, err)
	monitor.now = func() time.Time { return now }

	require.NoError(t, monitor.RunOnce(t.Context()))
	require.Equal(t, int64(7), auditDepth(t, reader))
	require.Equal(t, 360.0, auditAge(t, reader))

	source.snapshot = AuditQueueSnapshot{}
	require.NoError(t, monitor.RunOnce(t.Context()))
	require.Equal(t, int64(0), auditDepth(t, reader))
	require.Equal(t, 0.0, auditAge(t, reader))
}

func auditDepth(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	var exported metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &exported))
	for _, scope := range exported.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name == "saas.audit_queue.depth" {
				return item.Data.(metricdata.Gauge[int64]).DataPoints[0].Value
			}
		}
	}
	t.Fatal("audit queue depth was not exported")
	return 0
}

func auditAge(t *testing.T, reader *sdkmetric.ManualReader) float64 {
	t.Helper()
	var exported metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &exported))
	for _, scope := range exported.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name == "saas.audit_queue.oldest_age" {
				return item.Data.(metricdata.Gauge[float64]).DataPoints[0].Value
			}
		}
	}
	t.Fatal("audit queue age was not exported")
	return 0
}
