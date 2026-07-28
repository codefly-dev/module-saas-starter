package jobs

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type workerTelemetry struct {
	polls      metric.Int64Counter
	claimed    metric.Int64Counter
	active     metric.Int64UpDownCounter
	completed  metric.Int64Counter
	durationMS metric.Float64Histogram
}

func newWorkerTelemetry(meter metric.Meter) (workerTelemetry, error) {
	polls, err := meter.Int64Counter(
		"saas.jobs.polls",
		metric.WithDescription("Job worker polling attempts by queue and result."),
	)
	if err != nil {
		return workerTelemetry{}, err
	}
	claimed, err := meter.Int64Counter(
		"saas.jobs.claimed",
		metric.WithDescription("Jobs claimed for processing."),
	)
	if err != nil {
		return workerTelemetry{}, err
	}
	active, err := meter.Int64UpDownCounter(
		"saas.jobs.active",
		metric.WithDescription("Jobs currently executing."),
	)
	if err != nil {
		return workerTelemetry{}, err
	}
	completed, err := meter.Int64Counter(
		"saas.jobs.completed",
		metric.WithDescription("Job processing outcomes."),
	)
	if err != nil {
		return workerTelemetry{}, err
	}
	durationMS, err := meter.Float64Histogram(
		"saas.jobs.duration",
		metric.WithDescription("Job processing duration."),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return workerTelemetry{}, err
	}
	return workerTelemetry{
		polls: polls, claimed: claimed, active: active,
		completed: completed, durationMS: durationMS,
	}, nil
}

func (t workerTelemetry) recordPoll(
	ctx context.Context,
	queue string,
	result string,
	claimed int,
) {
	t.polls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("queue", queue),
		attribute.String("result", result),
	))
	if claimed > 0 {
		t.claimed.Add(ctx, int64(claimed), metric.WithAttributes(
			attribute.String("queue", queue),
		))
	}
}

func (t workerTelemetry) addActive(ctx context.Context, queue string, delta int64) {
	t.active.Add(ctx, delta, metric.WithAttributes(attribute.String("queue", queue)))
}

func (t workerTelemetry) recordProcess(
	ctx context.Context,
	queue string,
	outcome string,
	startedAt time.Time,
) {
	attributes := metric.WithAttributes(
		attribute.String("queue", queue),
		attribute.String("outcome", outcome),
	)
	t.completed.Add(ctx, 1, attributes)
	t.durationMS.Record(ctx, float64(time.Since(startedAt).Microseconds())/1000, attributes)
}
