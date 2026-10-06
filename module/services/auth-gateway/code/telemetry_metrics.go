package main

import (
	"context"
	"net/http"
	runtimemetrics "runtime/metrics"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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
	options := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(destination.Endpoint)}
	if destination.Insecure {
		// http://: plaintext on the wire, because the mesh supplies mTLS. An
		// https:// endpoint takes the exporter's default, TLS.
		options = append(options, otlpmetricgrpc.WithInsecure())
	}
	exporter, err := otlpmetricgrpc.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	res, err := currentTelemetryResource(ctx)
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

// newGatewayHTTPHandler instruments the gateway's HTTP listener when metrics are
// on. It adds no route of its own: the listener serves the gateway's routes and
// nothing else, with or without telemetry.
func newGatewayHTTPHandler(gateway http.Handler, metrics *otelMetrics) http.Handler {
	if metrics == nil {
		return gateway
	}
	return otelhttp.NewHandler(gateway, "auth-gateway.gateway")
}

func (m *otelMetrics) Shutdown(ctx context.Context) error {
	return m.provider.Shutdown(ctx)
}
