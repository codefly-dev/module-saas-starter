package main

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

func enableOTLPMetrics(ctx context.Context, serviceName string) (*metric.MeterProvider, error) {
	exporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(
		ctx,
		resource.WithAttributes(attribute.String("service.name", serviceName)),
	)
	if err != nil {
		return nil, err
	}
	provider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(exporter, metric.WithInterval(30*time.Second))),
	)
	otel.SetMeterProvider(provider)
	return provider, nil
}
