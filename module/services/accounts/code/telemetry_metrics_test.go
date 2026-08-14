package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"
	genconnect "accounts/pkg/gen/saas/accounts/v1/accountsv1connect"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	wooltel "github.com/codefly-dev/core/wool/otel"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type metricCapture struct {
	collectormetricsv1.UnimplementedMetricsServiceServer
	requests chan *collectormetricsv1.ExportMetricsServiceRequest
}

func (c *metricCapture) Export(
	_ context.Context,
	request *collectormetricsv1.ExportMetricsServiceRequest,
) (*collectormetricsv1.ExportMetricsServiceResponse, error) {
	c.requests <- request
	return &collectormetricsv1.ExportMetricsServiceResponse{}, nil
}

type versionConnectHandler struct {
	genconnect.UnimplementedUserServiceHandler
}

func (versionConnectHandler) Version(
	_ context.Context,
	request *connect.Request[gen.VersionRequest],
) (*connect.Response[gen.VersionResponse], error) {
	if request.Header().Get("X-Test-Error") != "" {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	}
	return connect.NewResponse(&gen.VersionResponse{Version: "test"}), nil
}

func TestEnableOTELMetricsNoopsWithoutEndpoint(t *testing.T) {
	before := otel.GetMeterProvider()
	metrics, err := enableOTELMetrics(t.Context(), "test-service", "  ")
	require.NoError(t, err)
	require.Nil(t, metrics)
	require.Equal(t, before, otel.GetMeterProvider())
}

func TestTelemetryMetricsExportRuntimeAndUnsampledRED(t *testing.T) {
	endpoint, capture := startMetricCapture(t)

	previousMeterProvider := otel.GetMeterProvider()
	previousTracerProvider := otel.GetTracerProvider()
	spanExporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.NeverSample()),
		sdktrace.WithSyncer(spanExporter),
	)
	otel.SetTracerProvider(tracerProvider)

	metrics, err := enableOTELMetrics(t.Context(), "test-service", endpoint)
	require.NoError(t, err)
	require.NotNil(t, metrics)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, metrics.Shutdown(shutdownCtx))
		require.NoError(t, tracerProvider.Shutdown(shutdownCtx))
		otel.SetMeterProvider(previousMeterProvider)
		otel.SetTracerProvider(previousTracerProvider)
	})

	recordGRPCRequests(t)
	recordConnectRequests(t)

	require.NoError(t, metrics.provider.ForceFlush(t.Context()))
	request := receiveMetrics(t, capture.requests)
	metricNames := exportedMetricNames(request)
	require.Contains(t, metricNames, "go.goroutine.count")
	require.Contains(t, metricNames, "go.memory.allocated")
	require.Contains(t, metricNames, "go.memory.gc.goal")

	grpcCounts := statusCounts(request, "rpc.server.call.duration", "rpc.response.status_code")
	require.Equal(t, uint64(3), grpcCounts["OK"])
	require.Equal(t, uint64(1), grpcCounts["NOT_FOUND"])

	connectCounts := statusCounts(request, "rpc.server.duration", "rpc.connect_rpc.error_code")
	require.Equal(t, uint64(3), metricCount(request, "rpc.server.duration"))
	require.Equal(t, uint64(1), connectCounts["permission_denied"])

	require.Empty(t, spanExporter.GetSpans())

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "http://accounts.internal/metrics", nil),
	)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/plain")
	body := recorder.Body.String()
	require.Contains(t, body, "go_goroutine_count")
	require.Contains(t, body, "go_memory_allocated")
	require.Contains(t, body, "go_memory_gc_goal")
	require.Contains(t, body, "rpc_server_call_duration_seconds_count")
	require.Contains(t, body, `rpc_method="grpc.health.v1.Health/Check"`)
	require.Contains(t, body, `rpc_response_status_code="OK"`)
	require.Contains(t, body, `service_name="test-service"`)
}

func startMetricCapture(t *testing.T) (string, *metricCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	capture := &metricCapture{requests: make(chan *collectormetricsv1.ExportMetricsServiceRequest, 4)}
	server := grpc.NewServer()
	collectormetricsv1.RegisterMetricsServiceServer(server, capture)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), capture
}

func recordGRPCRequests(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(wooltel.GRPCServerOptions()...)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(server, healthServer)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	connection, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	client := healthv1.NewHealthClient(connection)
	for range 3 {
		_, err = client.Check(t.Context(), &healthv1.HealthCheckRequest{})
		require.NoError(t, err)
	}
	_, err = client.Check(t.Context(), &healthv1.HealthCheckRequest{Service: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func recordConnectRequests(t *testing.T) {
	t.Helper()
	interceptor, err := otelconnect.NewInterceptor()
	require.NoError(t, err)
	path, handler := genconnect.NewUserServiceHandler(
		versionConnectHandler{},
		connect.WithInterceptors(interceptor),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := genconnect.NewUserServiceClient(server.Client(), server.URL)
	for range 2 {
		_, err = client.Version(t.Context(), connect.NewRequest(&gen.VersionRequest{}))
		require.NoError(t, err)
	}
	request := connect.NewRequest(&gen.VersionRequest{})
	request.Header().Set("X-Test-Error", "true")
	_, err = client.Version(t.Context(), request)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func receiveMetrics(
	t *testing.T,
	requests <-chan *collectormetricsv1.ExportMetricsServiceRequest,
) *collectormetricsv1.ExportMetricsServiceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OTLP metrics")
		return nil
	}
}

func exportedMetricNames(request *collectormetricsv1.ExportMetricsServiceRequest) map[string]bool {
	names := make(map[string]bool)
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				names[metric.GetName()] = true
			}
		}
	}
	return names
}

func metricCount(request *collectormetricsv1.ExportMetricsServiceRequest, name string) uint64 {
	var count uint64
	for _, point := range histogramPoints(request, name) {
		count += point.GetCount()
	}
	return count
}

func statusCounts(
	request *collectormetricsv1.ExportMetricsServiceRequest,
	name string,
	statusKey string,
) map[string]uint64 {
	counts := make(map[string]uint64)
	for _, point := range histogramPoints(request, name) {
		for _, attr := range point.GetAttributes() {
			if attr.GetKey() == statusKey {
				counts[attr.GetValue().GetStringValue()] += point.GetCount()
			}
		}
	}
	return counts
}

func histogramPoints(
	request *collectormetricsv1.ExportMetricsServiceRequest,
	name string,
) []*metricsv1.HistogramDataPoint {
	var points []*metricsv1.HistogramDataPoint
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				if metric.GetName() == name {
					points = append(points, metric.GetHistogram().GetDataPoints()...)
				}
			}
		}
	}
	return points
}
