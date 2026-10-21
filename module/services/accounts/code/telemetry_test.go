package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/resources"
	codefly "github.com/codefly-dev/sdk-go"
	"go.opentelemetry.io/otel"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
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
		value, _ := res.Set().Value("service.name")
		return value.AsString()
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

// SDK Init must deliver a real span with the supplied service identity; its
// public shutdown flushes the batch before the process exits.
func TestSDKExportsTracesAndFlushesOnShutdown(t *testing.T) {
	isolateOTLPEnvironment(t)
	t.Setenv(resources.ModulePrefix, "saas-starter")
	t.Setenv(resources.ServicePrefix, "accounts")
	t.Setenv("OTEL_SERVICE_NAME", "named-by-the-platform")
	previousTracer := otel.GetTracerProvider()
	previousTelemetry := wool.GetTelemetry()
	t.Cleanup(func() {
		_ = codefly.ShutdownTelemetry(context.Background())
		otel.SetTracerProvider(previousTracer)
		wool.RegisterTelemetry(previousTelemetry)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	capture := &sdkTraceCapture{requests: make(chan *collectortracev1.ExportTraceServiceRequest, 1)}
	server := grpc.NewServer()
	collectortracev1.RegisterTraceServiceServer(server, capture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	t.Setenv(otlpEndpointVariable, "http://"+listener.Addr().String())
	_, err = codefly.Init(t.Context())
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(t.Context(), "operation")
	span.End()
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, codefly.ShutdownTelemetry(shutdownCtx))
	select {
	case request := <-capture.requests:
		require.Len(t, request.ResourceSpans, 1)
		for _, attribute := range request.ResourceSpans[0].Resource.Attributes {
			if attribute.Key == "service.name" {
				require.Equal(t, "named-by-the-platform", attribute.Value.GetStringValue())
				return
			}
		}
		t.Fatal("exported trace has no service.name")
	case <-time.After(5 * time.Second):
		t.Fatal("SDK shutdown did not deliver the span")
	}
}

type sdkTraceCapture struct {
	collectortracev1.UnimplementedTraceServiceServer
	requests chan *collectortracev1.ExportTraceServiceRequest
}

func (capture *sdkTraceCapture) Export(_ context.Context, request *collectortracev1.ExportTraceServiceRequest) (*collectortracev1.ExportTraceServiceResponse, error) {
	capture.requests <- request
	return &collectortracev1.ExportTraceServiceResponse{}, nil
}
