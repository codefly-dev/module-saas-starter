package main

import (
	"context"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/stretchr/testify/require"
)

// stubJobOperations satisfies jobs.Operations without touching a database. The
// durable-metrics monitor never calls these during construction, so returning
// zero values is enough to exercise newDurableJobMetricsMonitor.
type stubJobOperations struct{}

func (stubJobOperations) GetJobOperations(context.Context, *jobsv1.GetJobOperationsRequest) (*jobsv1.GetJobOperationsResponse, error) {
	return nil, nil
}

func (stubJobOperations) ListJobs(context.Context, *jobsv1.ListJobsRequest) (*jobsv1.ListJobsResponse, error) {
	return nil, nil
}

func (stubJobOperations) GetJob(context.Context, *jobsv1.GetJobRequest) (*jobsv1.GetJobResponse, error) {
	return nil, nil
}

func (stubJobOperations) ReplayJob(context.Context, *jobsv1.ReplayJobRequest) (*jobsv1.ReplayJobResponse, error) {
	return nil, nil
}

func TestDurableJobMetricsMonitorGatedOnEnabledMetrics(t *testing.T) {
	// Observability off (otelMetricProvider == nil): no monitor may be built,
	// otherwise a background goroutine polls the job store every interval into a
	// no-op meter in local dev, tests, and endpoint-less deployments.
	monitor, err := newDurableJobMetricsMonitor(false, stubJobOperations{})
	require.NoError(t, err)
	require.Nil(t, monitor, "durable job metrics monitor must not be created when OTEL metrics are disabled")

	// Observability on: the monitor is constructed against the provided source.
	monitor, err = newDurableJobMetricsMonitor(true, stubJobOperations{})
	require.NoError(t, err)
	require.NotNil(t, monitor, "durable job metrics monitor must be created when OTEL metrics are enabled")
}
