package main

import (
	"context"
	"errors"

	"github.com/codefly-dev/core/wool"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"accounts/pkg/business"
)

// retentionFailuresMetric counts the parts of scheduled retention runs that
// failed. The alert pack's retention_failures alert reads it, as
// saas_retention_failures_total, so a run that did not do all it should reaches
// someone instead of ending as a log line.
const retentionFailuresMetric = "saas.retention.failures"

type retentionTelemetry struct {
	failures otelmetric.Int64Counter
}

func newRetentionTelemetry(meter otelmetric.Meter) (retentionTelemetry, error) {
	failures, err := meter.Int64Counter(retentionFailuresMetric,
		otelmetric.WithDescription("Parts of scheduled retention runs that failed, by resource and step."))
	if err != nil {
		return retentionTelemetry{}, err
	}
	return retentionTelemetry{failures: failures}, nil
}

// runScheduledRetention runs one retention pass and reports it: what it removed
// as information, and a run that failed in any part as an error, counted for the
// alert pack.
func runScheduledRetention(ctx context.Context, service *business.Service, metrics retentionTelemetry) {
	w := wool.Get(ctx).In("retention")
	deleted, err := service.RunRetention(ctx)
	for kind, count := range deleted {
		if count > 0 {
			w.Info("deleted records", wool.Field("count", count), wool.Field("kind", kind))
		}
	}
	if err != nil {
		metrics.recordFailure(ctx, err)
		w.Error("scheduled run failed", wool.ErrField(err))
	}
}

// recordFailure counts a failed run: once for each failed part of a
// *business.RetentionError, and once for the run as a whole for any other error.
func (t retentionTelemetry) recordFailure(ctx context.Context, err error) {
	var run *business.RetentionError
	if !errors.As(err, &run) {
		t.failures.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("resource", "retention"), attribute.String("step", "run")))
		return
	}
	for _, failure := range run.Failures {
		t.failures.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("resource", failure.Resource), attribute.String("step", string(failure.Step))))
	}
}
