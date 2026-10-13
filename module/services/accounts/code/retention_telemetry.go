package main

import (
	"context"
	"errors"
	"sync"

	"github.com/codefly-dev/core/wool"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"accounts/pkg/business"
)

// retentionFailingMetric is 1 for each part of the most recent scheduled
// retention run that failed, by resource and step, and 0 for a part an earlier
// run failed and the most recent did not. The alert pack's retention_failures
// alert reads it, as saas_retention_failing, so a run that did not do all it
// should reaches someone instead of ending as a log line. It is a gauge because
// a counter cannot alert on a first failure: its first sample is already 1, and
// Prometheus sees no increase in a series it never saw lower.
const retentionFailingMetric = "saas.retention.failing"

// retentionPart is one part of a retention run, as the gauge labels it.
type retentionPart struct {
	resource, step string
}

func (p retentionPart) attributes() otelmetric.MeasurementOption {
	return otelmetric.WithAttributes(attribute.String("resource", p.resource), attribute.String("step", p.step))
}

type retentionTelemetry struct {
	failing otelmetric.Int64Gauge

	mu sync.Mutex
	// failed holds the parts the last reported run failed, which the next
	// report sets back to 0 if its run does not fail them.
	failed map[retentionPart]bool
}

func newRetentionTelemetry(meter otelmetric.Meter) (*retentionTelemetry, error) {
	failing, err := meter.Int64Gauge(retentionFailingMetric,
		otelmetric.WithDescription("1 for each part of the most recent scheduled retention run that failed, by resource and step."))
	if err != nil {
		return nil, err
	}
	return &retentionTelemetry{failing: failing}, nil
}

// runScheduledRetention runs one retention pass and reports it: what it removed
// as information, a run that failed in any part as an error, and every run to
// the gauge the alert pack watches.
func runScheduledRetention(ctx context.Context, service *business.Service, metrics *retentionTelemetry) {
	w := wool.Get(ctx).In("retention")
	deleted, err := service.RunRetention(ctx)
	for kind, count := range deleted {
		if count > 0 {
			w.Info("deleted records", wool.Field("count", count), wool.Field("kind", kind))
		}
	}
	metrics.report(ctx, err)
	if err != nil {
		w.Error("scheduled run failed", wool.ErrField(err))
	}
}

// report sets the gauge for a run that ended with err: 1 for each part it
// failed -- each failure of a *business.RetentionError, or the run as a whole
// for any other error -- and 0 for each part the run before failed and this
// one did not.
func (t *retentionTelemetry) report(ctx context.Context, err error) {
	failed := map[retentionPart]bool{}
	var run *business.RetentionError
	switch {
	case err == nil:
	case errors.As(err, &run):
		for _, failure := range run.Failures {
			failed[retentionPart{failure.Resource, string(failure.Step)}] = true
		}
	default:
		failed[retentionPart{"retention", "run"}] = true
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for part := range t.failed {
		if !failed[part] {
			t.failing.Record(ctx, 0, part.attributes())
		}
	}
	for part := range failed {
		t.failing.Record(ctx, 1, part.attributes())
	}
	t.failed = failed
}
