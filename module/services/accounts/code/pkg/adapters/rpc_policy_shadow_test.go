package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// The shadow signal must record a data point for every admitted call,
// including fully covered (ok) ones. Without the ok heartbeat, an absence of
// gap/unsupported points is indistinguishable from a dark instrument, and the
// "zero gap over real traffic" precondition for enforcement can read a false
// green.
func TestShadowPolicyCoverageRecordsEveryOutcomeIncludingOK(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(metricnoop.NewMeterProvider()) })

	// GetUser is a covered self-service read (ok); CreateAPIKey requires an org
	// admin the interceptor does not yet resolve (gap).
	shadowPolicyCoverage(context.Background(), "/saas.accounts.v1.UserService/GetUser")
	shadowPolicyCoverage(context.Background(), "/saas.accounts.v1.APIKeyService/CreateAPIKey")

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))

	byCoverage := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "saas.accounts.rpc_policy.coverage" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "coverage metric is an int64 sum")
			for _, point := range sum.DataPoints {
				coverage, ok := point.Attributes.Value("coverage")
				require.True(t, ok, "data point carries a coverage attribute")
				byCoverage[coverage.AsString()] += point.Value
			}
		}
	}

	require.Equal(t, int64(1), byCoverage["ok"], "the ok heartbeat must be recorded")
	require.Equal(t, int64(1), byCoverage["gap"], "the gap signal must be recorded")
}
