package jobs

import (
	"context"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type operationsSourceFake struct {
	Operations
	response *jobsv1.GetJobOperationsResponse
}

func (f operationsSourceFake) GetJobOperations(
	context.Context,
	*jobsv1.GetJobOperationsRequest,
) (*jobsv1.GetJobOperationsResponse, error) {
	return f.response, nil
}

func TestOperationsMonitorExportsDurableQueueDepthAndAge(t *testing.T) {
	observedAt := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })
	monitor, err := NewOperationsMonitor(
		operationsSourceFake{response: &jobsv1.GetJobOperationsResponse{
			ObservedAt: timestamppb.New(observedAt),
			Queues: []*jobsv1.JobQueueSnapshot{{
				Queue: "analytics", Pending: 3, Processing: 2, Retrying: 1,
				DeadLetter: 4, Ready: 5, Scheduled: 6, ExpiredLeases: 7,
				OldestReadyAt: timestamppb.New(observedAt.Add(-30 * time.Second)),
			}},
		}},
		provider.Meter("operations-test"),
		time.Minute,
	)
	require.NoError(t, err)
	require.NoError(t, monitor.RunOnce(t.Context()))

	var exported metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &exported))
	names := map[string]bool{}
	var depthValues []int64
	var ageValues []float64
	for _, scope := range exported.ScopeMetrics {
		for _, metric := range scope.Metrics {
			names[metric.Name] = true
			switch data := metric.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, point := range data.DataPoints {
					depthValues = append(depthValues, point.Value)
				}
			case metricdata.Gauge[float64]:
				for _, point := range data.DataPoints {
					ageValues = append(ageValues, point.Value)
				}
			}
		}
	}
	require.Equal(t, map[string]bool{
		"saas.jobs.depth":            true,
		"saas.jobs.oldest_ready_age": true,
	}, names)
	require.ElementsMatch(t, []int64{3, 2, 1, 4, 5, 6, 7}, depthValues)
	require.Equal(t, []float64{30}, ageValues)
}
