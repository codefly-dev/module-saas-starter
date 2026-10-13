package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"accounts/pkg/business"
	"accounts/pkg/metrics"
)

// retentionTelemetryFor registers the retention gauge with a provider whose
// reader the test collects from.
func retentionTelemetryFor(t *testing.T) (*sdkmetric.ManualReader, *retentionTelemetry) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	telemetry, err := newRetentionTelemetry(provider.Meter("retention-test"))
	require.NoError(t, err)
	return reader, telemetry
}

// retentionFailing reads the gauge, by "resource step".
func retentionFailing(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	failing := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != retentionFailingMetric {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "%s is a gauge", retentionFailingMetric)
			for _, point := range gauge.DataPoints {
				resource, _ := point.Attributes.Value("resource")
				step, _ := point.Attributes.Value("step")
				failing[resource.AsString()+" "+step.AsString()] = point.Value
			}
		}
	}
	return failing
}

// Each part the most recent run failed reads 1, by resource and step, and a
// part an earlier run failed reads 0 once a run no longer fails it; any other
// error is the run as a whole failing. Nothing is reported for a part that
// never failed.
func TestTheGaugeReadsOneForEachPartTheLastRunFailed(t *testing.T) {
	reader, telemetry := retentionTelemetryFor(t)
	ctx := context.Background()
	removal := business.RetentionFailure{Resource: "audit_events", Step: business.RetentionStepRemove, Err: errors.New("lock not available")}
	policy := business.RetentionFailure{Resource: "sessions", Step: business.RetentionStepPolicy, Err: errors.New("not a positive number of days")}

	telemetry.report(ctx, nil)
	require.Empty(t, retentionFailing(t, reader))

	telemetry.report(ctx, &business.RetentionError{Failures: []business.RetentionFailure{removal, policy}})
	require.Equal(t, map[string]int64{"audit_events remove": 1, "sessions policy": 1}, retentionFailing(t, reader))

	telemetry.report(ctx, &business.RetentionError{Failures: []business.RetentionFailure{policy}})
	require.Equal(t, map[string]int64{"audit_events remove": 0, "sessions policy": 1}, retentionFailing(t, reader))

	telemetry.report(ctx, nil)
	require.Equal(t, map[string]int64{"audit_events remove": 0, "sessions policy": 0}, retentionFailing(t, reader))

	telemetry.report(ctx, errors.New("anything else"))
	require.Equal(t, map[string]int64{"audit_events remove": 0, "sessions policy": 0, "retention run": 1}, retentionFailing(t, reader))
}

// scheduledRetentionStore is a store which holds no retention policy and whose
// audit partition provisioning fails while provisionErr is set.
type scheduledRetentionStore struct {
	business.Store
	provisionErr error
}

func (*scheduledRetentionStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *scheduledRetentionStore) EnsureAuditPartitions(context.Context, int) error {
	return s.provisionErr
}

func (*scheduledRetentionStore) DatabaseNow(context.Context) (time.Time, error) {
	return time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC), nil
}

func (*scheduledRetentionStore) GetRetentionPolicies(context.Context) ([]*business.RetentionPolicy, error) {
	return nil, nil
}

// The scheduler reports every run to the gauge the alert pack watches, not only
// to its log: a run that failed sets it, and the next run that does not clears
// it.
func TestAScheduledRetentionRunReportsToTheGauge(t *testing.T) {
	reader, telemetry := retentionTelemetryFor(t)
	store := &scheduledRetentionStore{provisionErr: errors.New("provisioning waited ten seconds for its locks")}
	service, err := business.NewService(store)
	require.NoError(t, err)

	runScheduledRetention(context.Background(), service, telemetry)
	require.Equal(t, map[string]int64{"audit_events provision": 1}, retentionFailing(t, reader))

	store.provisionErr = nil
	runScheduledRetention(context.Background(), service, telemetry)
	require.Equal(t, map[string]int64{"audit_events provision": 0}, retentionFailing(t, reader))
}

// A failed retention run pushes its gauge to the collector. The alert pack and
// its promtool fixtures use the Prometheus translation of that instrument name.
func TestTheRetentionAlertReadsTheGaugeExportedOverOTLP(t *testing.T) {
	endpoint, capture := startMetricCapture(t)
	t.Setenv(otlpEndpointVariable, "http://"+endpoint)
	metricsProvider, err := enableOTELMetrics(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsProvider.Shutdown(context.Background()) })
	telemetry, err := newRetentionTelemetry(metricsProvider.provider.Meter("github.com/codefly-dev/module-saas-starter/accounts"))
	require.NoError(t, err)
	telemetry.report(context.Background(), &business.RetentionError{Failures: []business.RetentionFailure{
		{Resource: "audit_events", Step: business.RetentionStepRemove, Err: errors.New("lock not available")},
	}})

	require.NoError(t, metricsProvider.provider.ForceFlush(t.Context()))
	request := receiveMetrics(t, capture.requests)
	found := false
	for _, resource := range request.ResourceMetrics {
		for _, scope := range resource.ScopeMetrics {
			for _, instrument := range scope.Metrics {
				if instrument.Name != retentionFailingMetric {
					continue
				}
				for _, point := range instrument.GetGauge().DataPoints {
					labels := map[string]string{}
					for _, attribute := range point.Attributes {
						labels[attribute.Key] = attribute.Value.GetStringValue()
					}
					if labels["resource"] == "audit_events" && labels["step"] == "remove" {
						require.EqualValues(t, 1, point.GetAsInt())
						found = true
					}
				}
			}
		}
	}
	require.True(t, found, "the failed retention part is exported as a gauge")
	scraped := strings.ReplaceAll(retentionFailingMetric, ".", "_")

	pack, err := metrics.DefaultSLOPack()
	require.NoError(t, err)
	var expression string
	for _, alert := range pack.Alerts {
		if alert.ID == "retention_failures" {
			require.Equal(t, "retention", alert.Runbook)
			expression = alert.Expression
		}
	}
	require.Contains(t, expression, scraped, "the alert reads the gauge by its scraped name")
	tests, err := os.ReadFile("pkg/metrics/slo_pack_test.yml")
	require.NoError(t, err)
	require.Contains(t, string(tests), scraped+"{", "the promtool tests feed the alert the series the scrape serves")
}
