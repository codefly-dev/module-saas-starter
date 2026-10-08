package main

import (
	"context"
	"net/http"
	runtimemetrics "runtime/metrics"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

type otelMetrics struct {
	provider *metric.MeterProvider
	handler  http.Handler
}

func enableOTELMetrics(ctx context.Context, serviceName, endpoint string) (*otelMetrics, error) {
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
		resource.WithService(),
		resource.WithFromEnv(),
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
	if err := startGCActivityMetrics(provider); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, err
	}
	otel.SetMeterProvider(provider)
	return &otelMetrics{
		provider: provider,
		handler:  promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	}, nil
}

func startGCActivityMetrics(provider *metric.MeterProvider) error {
	meter := provider.Meter("github.com/codefly-dev/module-saas-starter/go-runtime")
	cycleCount, err := meter.Int64ObservableCounter(
		"go.gc.cycle.count",
		otelmetric.WithUnit("{cycle}"),
		otelmetric.WithDescription("Completed Go garbage collection cycles."),
	)
	if err != nil {
		return err
	}
	pauseCPUTime, err := meter.Float64ObservableCounter(
		"go.gc.pause.cpu_time",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Estimated cumulative CPU time unavailable to application work during GC pauses."),
	)
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, observer otelmetric.Observer) error {
		samples := []runtimemetrics.Sample{
			{Name: "/gc/cycles/total:gc-cycles"},
			{Name: "/cpu/classes/gc/pause:cpu-seconds"},
		}
		runtimemetrics.Read(samples)
		observer.ObserveInt64(cycleCount, int64(samples[0].Value.Uint64()))
		observer.ObserveFloat64(pauseCPUTime, samples[1].Value.Float64())
		return nil
	}, cycleCount, pauseCPUTime)
	return err
}

func newGatewayHTTPHandler(gateway http.Handler, metrics *otelMetrics) http.Handler {
	if metrics == nil {
		return gateway
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("/", otelhttp.NewHandler(gateway, "auth-gateway.gateway"))
	return mux
}

func (m *otelMetrics) Handler() http.Handler {
	return m.handler
}

func (m *otelMetrics) Shutdown(ctx context.Context) error {
	return m.provider.Shutdown(ctx)
}
