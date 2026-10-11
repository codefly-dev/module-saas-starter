package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"accounts/pkg/business"
	"accounts/pkg/metrics"
)

// A scheduled retention run that fails is counted once for each part that
// failed, by resource and step, and any other error once for the run, so the
// alert pack sees it.
func TestARetentionRunThatFailsIsCountedForTheAlert(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	telemetry, err := newRetentionTelemetry(provider.Meter("retention-test"))
	require.NoError(t, err)

	telemetry.recordFailure(context.Background(), &business.RetentionError{Failures: []business.RetentionFailure{
		{Resource: "audit_events", Step: business.RetentionStepRemove, Err: errors.New("lock not available")},
		{Resource: "sessions", Step: business.RetentionStepPolicy, Err: errors.New("not a positive number of days")},
	}})
	telemetry.recordFailure(context.Background(), errors.New("anything else"))

	require.Equal(t, map[string]int64{"audit_events remove": 1, "sessions policy": 1, "retention run": 1},
		retentionFailuresCounted(t, reader))
}

// failingRetentionStore is a store whose audit partition provisioning fails and
// which holds no retention policy.
type failingRetentionStore struct {
	business.Store
}

func (failingRetentionStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (failingRetentionStore) EnsureAuditPartitions(context.Context, int) error {
	return errors.New("provisioning waited ten seconds for its locks")
}

func (failingRetentionStore) DatabaseNow(context.Context) (time.Time, error) {
	return time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC), nil
}

func (failingRetentionStore) GetRetentionPolicies(context.Context) ([]*business.RetentionPolicy, error) {
	return nil, nil
}

// The scheduler reports a run that failed in any part to the counter the alert
// pack watches, not only to its log.
func TestAScheduledRetentionRunThatFailsIsCounted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	telemetry, err := newRetentionTelemetry(provider.Meter("retention-test"))
	require.NoError(t, err)
	service, err := business.NewService(failingRetentionStore{})
	require.NoError(t, err)

	runScheduledRetention(context.Background(), service, telemetry)

	require.Equal(t, map[string]int64{"audit_events provision": 1}, retentionFailuresCounted(t, reader))
}

// retentionFailuresCounted reads the failure counter, by "resource step".
func retentionFailuresCounted(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	counted := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != retentionFailuresMetric {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "%s is a counter", retentionFailuresMetric)
			for _, point := range sum.DataPoints {
				resource, _ := point.Attributes.Value("resource")
				step, _ := point.Attributes.Value("step")
				counted[resource.AsString()+" "+step.AsString()] += point.Value
			}
		}
	}
	return counted
}

// The alert pack watches the counter the scheduler writes, under the name the
// Prometheus exporter gives a counter, and sends whoever it pages to the
// retention runbook.
func TestTheAlertPackWatchesRetentionFailures(t *testing.T) {
	pack, err := metrics.DefaultSLOPack()
	require.NoError(t, err)
	exported := strings.ReplaceAll(retentionFailuresMetric, ".", "_") + "_total"
	for _, alert := range pack.Alerts {
		if strings.Contains(alert.Expression, exported) {
			require.Equal(t, "retention", alert.Runbook)
			return
		}
	}
	t.Fatalf("no alert in the pack watches %s", exported)
}
