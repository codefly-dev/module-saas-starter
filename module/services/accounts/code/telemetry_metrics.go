package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

type otelMetrics struct {
	provider *metric.MeterProvider
	handler  http.Handler
}

func enableOTELMetrics(ctx context.Context, serviceName, endpoint string) (*otelMetrics, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}

	exporter, err := otlpmetricgrpc.New(
		ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	registry := prometheus.NewRegistry()
	scrapeExporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registry),
		otelprometheus.WithResourceAsConstantLabels(
			attribute.NewAllowKeysFilter("service.name"),
		),
	)
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
		metric.WithReader(scrapeExporter),
	)
	if err := otelruntime.Start(otelruntime.WithMeterProvider(provider)); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, err
	}
	otel.SetMeterProvider(provider)
	return &otelMetrics{
		provider: provider,
		handler:  promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	}, nil
}

func (m *otelMetrics) Handler() http.Handler {
	return m.handler
}

func (m *otelMetrics) Shutdown(ctx context.Context) error {
	return m.provider.Shutdown(ctx)
}
