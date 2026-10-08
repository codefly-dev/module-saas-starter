package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// isolateOTLPEnvironment clears every OTLP variable the exporters read, so what a
// test sets is the whole configuration and nothing leaks in from the machine
// running it.
func isolateOTLPEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_CLIENT_KEY",
		"OTEL_EXPORTER_OTLP_INSECURE",
		"OTEL_EXPORTER_OTLP_HEADERS",
	} {
		t.Setenv(name, "")
	}
}

// collectorAt is the platform's whole delivery: the one variable, set to the
// collector's address.
func collectorAt(t *testing.T, scheme, hostPort string) {
	t.Helper()
	isolateOTLPEnvironment(t)
	t.Setenv(otlpEndpointVariable, scheme+"://"+hostPort)
}

// shippedObservabilityProfile is what the module's own `observability` profile
// contributes to the group, read from the file it ships.
func shippedObservabilityProfile(t *testing.T) map[string]string {
	t.Helper()
	contents, err := os.ReadFile("../../../configurations/local/observability.env")
	require.NoError(t, err)
	values := map[string]string{}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok)
		values[key] = value
	}
	return values
}

// Module defaults also reach deployed cells, so the profile names no collector:
// the group exists (a composing workspace inherits a group from the key it
// assigns), and the one variable it assigns is empty, which is no endpoint.
func TestShippedObservabilityProfileNamesNoCollector(t *testing.T) {
	require.Equal(t, map[string]string{otlpEndpointVariable: ""}, shippedObservabilityProfile(t))
}

func TestOTLPEndpointConfigured(t *testing.T) {
	isolateOTLPEnvironment(t)
	require.False(t, otlpEndpointConfigured(), "unset")
	t.Setenv(otlpEndpointVariable, "   ")
	require.False(t, otlpEndpointConfigured(), "a blank value is no endpoint: the metrics exporter trims it to unset")
	t.Setenv(otlpEndpointVariable, "http://otel-collector.otel-collector.svc.cluster.local:4317")
	require.True(t, otlpEndpointConfigured())
}

// service.name is never a literal typed here: the environment wins, then the
// service's own Codefly identity.
func TestTelemetryResourceServiceName(t *testing.T) {
	resolve := func(t *testing.T, identity string) string {
		t.Helper()
		res, err := telemetryResource(t.Context(), identity)
		require.NoError(t, err)
		return telemetryServiceName(res)
	}

	t.Run("falls back to the Codefly service identity", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		require.Equal(t, "saas-starter/accounts", resolve(t, "saas-starter/accounts"))
	})
	t.Run("OTEL_SERVICE_NAME wins over the identity", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "named-by-the-platform")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		require.Equal(t, "named-by-the-platform", resolve(t, "saas-starter/accounts"))
	})
	t.Run("OTEL_RESOURCE_ATTRIBUTES can set it", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=attributed-by-the-platform,deployment.environment=test")
		require.Equal(t, "attributed-by-the-platform", resolve(t, "saas-starter/accounts"))
	})
	t.Run("without either, the SDK default rather than a literal", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		require.True(t, strings.HasPrefix(resolve(t, ""), "unknown_service"), resolve(t, ""))
	})
}

// The identity is the one the SDK built its provider with, the same one wool
// stamps on this process's log lines — read from the provider, not from the
// runtime's environment carrier.
func TestCodeflyServiceNameIsTheSDKProviderIdentity(t *testing.T) {
	carrying := func(unique string) context.Context {
		return wool.New(t.Context(), &wool.Resource{Kind: "service", Unique: unique}).Inject(t.Context())
	}
	require.Equal(t, "saas-starter/accounts", codeflyServiceName(carrying("saas-starter/accounts")))
	require.Equal(t, "accounts", codeflyServiceName(carrying("/accounts")), "an identity with no module")
	require.Empty(t, codeflyServiceName(carrying("/")), "an identity with neither")
	require.Empty(t, codeflyServiceName(t.Context()), "no provider: the SDK was not initialised")
}

func restoreTracing(t *testing.T) {
	t.Helper()
	previous := otel.GetTracerProvider()
	t.Cleanup(func() {
		wool.RegisterTelemetry(nil)
		otel.SetTracerProvider(previous)
	})
}

// With no endpoint delivered the process registers no OTLP exporter: nothing
// is exported, and nothing dials a default address.
func TestEnableTracingWithoutAnEndpointRegistersNothingOutsideALocalRun(t *testing.T) {
	restoreTracing(t)
	isolateOTLPEnvironment(t)
	provider, err := enableTracing(t.Context(), false)
	require.NoError(t, err)
	require.Nil(t, provider)
	require.False(t, wool.TelemetryEnabled())
}

// A local run with no endpoint keeps wool's own stdout tracer, which exports
// nothing over OTLP.
func TestEnableTracingWithoutAnEndpointUsesWoolsStdoutTracerInALocalRun(t *testing.T) {
	restoreTracing(t)
	isolateOTLPEnvironment(t)
	provider, err := enableTracing(t.Context(), true)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.True(t, wool.TelemetryEnabled())
	shutdown(t, provider)
}

// The endpoint decides, not where the process runs: a pod deployed into an
// environment named `local` that is handed a collector exports to it, rather
// than falling back to the stdout tracer.
func TestEnableTracingExportsToTheDeliveredEndpointWhetherOrNotTheRunIsLocal(t *testing.T) {
	for name, local := range map[string]bool{"deployed": false, "local environment": true} {
		t.Run(name, func(t *testing.T) {
			restoreTracing(t)
			t.Setenv("OTEL_SERVICE_NAME", "named-by-the-platform")
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
			endpoint, capture := startTraceCapture(t)
			collectorAt(t, "http", endpoint)
			provider, err := enableTracing(t.Context(), local)
			require.NoError(t, err)
			require.NotNil(t, provider)
			require.True(t, wool.TelemetryEnabled())

			_, span := otel.Tracer("test").Start(t.Context(), "operation")
			span.End()
			shutdown(t, provider) // flushes the batch

			select {
			case request := <-capture.requests:
				var name string
				for _, resourceSpans := range request.GetResourceSpans() {
					for _, attribute := range resourceSpans.GetResource().GetAttributes() {
						if attribute.GetKey() == "service.name" {
							name = attribute.GetValue().GetStringValue()
						}
					}
				}
				require.Equal(t, "named-by-the-platform", name)
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for OTLP spans")
			}
		})
	}
}

// core's wool/otel owns what an endpoint may be, and a value it refuses stops
// the process with its reason and registers nothing — never a tracer pointed at a
// default address.
func TestEnableTracingReturnsWhatWoolRefusesAndRegistersNothing(t *testing.T) {
	for name, value := range map[string]string{
		"no port":  "http://otel-collector.otel-collector.svc.cluster.local",
		"userinfo": "http://user:secret-pass@collector.example:4317",
	} {
		t.Run(name, func(t *testing.T) {
			restoreTracing(t)
			isolateOTLPEnvironment(t)
			t.Setenv(otlpEndpointVariable, value)
			provider, err := enableTracing(t.Context(), false)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-pass", "the refusal never echoes the URL")
			require.Nil(t, provider)
			require.False(t, wool.TelemetryEnabled())
		})
	}
}

// wool takes a value that is not an http:// or https:// URL for a bare
// host:port, the shape it accepted before it read URLs, and dials it in
// plaintext. The metrics exporter reads only a URL and, given anything else,
// keeps its default, localhost:4317. So the tracer would accept what the
// metrics exporter silently misreads, which is why the metrics call refuses it:
// both signals follow the one variable, and neither is pointed at a default.
func TestAValueWoolAcceptsAsHostPortIsRefusedByTheMetricsExporterGuard(t *testing.T) {
	restoreTracing(t)
	isolateOTLPEnvironment(t)
	t.Setenv(otlpEndpointVariable, "collector.example:4317")

	provider, err := enableTracing(t.Context(), false)
	require.NoError(t, err)
	require.NotNil(t, provider)
	shutdown(t, provider)

	metrics, err := enableOTELMetrics(t.Context())
	require.ErrorContains(t, err, "http:// or https://")
	require.Nil(t, metrics)
}

type traceCapture struct {
	collectortracev1.UnimplementedTraceServiceServer
	requests chan *collectortracev1.ExportTraceServiceRequest
}

func (c *traceCapture) Export(
	_ context.Context,
	request *collectortracev1.ExportTraceServiceRequest,
) (*collectortracev1.ExportTraceServiceResponse, error) {
	c.requests <- request
	return &collectortracev1.ExportTraceServiceResponse{}, nil
}

// startTraceCapture serves an in-process OTLP/gRPC trace receiver on loopback.
func startTraceCapture(t *testing.T) (string, *traceCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	capture := &traceCapture{requests: make(chan *collectortracev1.ExportTraceServiceRequest, 4)}
	server := grpc.NewServer()
	collectortracev1.RegisterTraceServiceServer(server, capture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), capture
}

func shutdown(t *testing.T, provider interface{ Shutdown(context.Context) error }) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, provider.Shutdown(ctx))
}
