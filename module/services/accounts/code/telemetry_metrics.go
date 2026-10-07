package main

import (
	"context"
	"errors"
	runtimemetrics "runtime/metrics"
	"time"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
)

// otelMetrics owns the process's MeterProvider. Metrics leave by one path only:
// an OTLP push to the cell's collector, which is the record for traces and
// metrics alike. There is no scrape endpoint to mount, expose or exempt.
type otelMetrics struct {
	provider *metric.MeterProvider
}

func enableOTELMetrics(ctx context.Context, destination telemetryDestination) (*otelMetrics, error) {
	if destination.URL == "" {
		// WithEndpointURL("") would fall back to the exporter's own default,
		// localhost:4317: a process that exports to nowhere and reports success.
		return nil, errors.New("telemetry: no collector URL to export metrics to")
	}
	// The resource comes before the exporter, so a failure here leaves nothing
	// to shut down: an exporter holds a client connection that only Shutdown
	// releases.
	res, err := currentTelemetryResource(ctx)
	if err != nil {
		return nil, err
	}
	// The exporter reads the transport off the URL's scheme itself: http:// is
	// plaintext on the wire, because the mesh supplies mTLS, and https:// is TLS.
	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(destination.URL))
	if err != nil {
		return nil, err
	}
	provider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(exporter, metric.WithInterval(30*time.Second))),
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
	return &otelMetrics{provider: provider}, nil
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

func (m *otelMetrics) Shutdown(ctx context.Context) error {
	return m.provider.Shutdown(ctx)
}
