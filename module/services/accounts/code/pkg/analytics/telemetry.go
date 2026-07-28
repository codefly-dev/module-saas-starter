package analytics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type exportTelemetry struct {
	rejected         metric.Int64Counter
	delivered        metric.Int64Counter
	duplicates       metric.Int64Counter
	providerDuration metric.Float64Histogram
}

func newExportTelemetry(meter metric.Meter) (exportTelemetry, error) {
	rejected, err := meter.Int64Counter(
		"saas.analytics.rejected",
		metric.WithDescription("Analytics commands rejected before provider delivery."),
	)
	if err != nil {
		return exportTelemetry{}, err
	}
	delivered, err := meter.Int64Counter(
		"saas.analytics.delivered",
		metric.WithDescription("Analytics commands accepted by the configured provider."),
	)
	if err != nil {
		return exportTelemetry{}, err
	}
	duplicates, err := meter.Int64Counter(
		"saas.analytics.duplicates",
		metric.WithDescription("Logical analytics duplicates acknowledged by the provider."),
	)
	if err != nil {
		return exportTelemetry{}, err
	}
	providerDuration, err := meter.Float64Histogram(
		"saas.analytics.provider.duration",
		metric.WithDescription("Analytics provider request latency."),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return exportTelemetry{}, err
	}
	return exportTelemetry{
		rejected: rejected, delivered: delivered, duplicates: duplicates,
		providerDuration: providerDuration,
	}, nil
}

func (t exportTelemetry) recordRejected(ctx context.Context, reason string) {
	t.rejected.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (t exportTelemetry) recordDelivery(
	ctx context.Context,
	kind string,
	delivery Delivery,
) {
	attributes := metric.WithAttributes(attribute.String("kind", kind))
	t.delivered.Add(ctx, 1, attributes)
	if delivery.Duplicate {
		t.duplicates.Add(ctx, 1, attributes)
	}
}

func (t exportTelemetry) recordProviderDuration(
	ctx context.Context,
	kind string,
	startedAt time.Time,
) {
	t.providerDuration.Record(
		ctx,
		float64(time.Since(startedAt).Microseconds())/1000,
		metric.WithAttributes(attribute.String("kind", kind)),
	)
}
