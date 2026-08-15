package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	wooltel "github.com/codefly-dev/core/wool/otel"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type authMetricCapture struct {
	collectormetricsv1.UnimplementedMetricsServiceServer
	requests chan *collectormetricsv1.ExportMetricsServiceRequest
}

func (c *authMetricCapture) Export(
	_ context.Context,
	request *collectormetricsv1.ExportMetricsServiceRequest,
) (*collectormetricsv1.ExportMetricsServiceResponse, error) {
	c.requests <- request
	return &collectormetricsv1.ExportMetricsServiceResponse{}, nil
}

func TestAuthSidecarTelemetryExportsGatewayAndGRPCRED(t *testing.T) {
	endpoint, capture := startAuthMetricCapture(t)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=auth-test-instance")
	previousMeterProvider := otel.GetMeterProvider()
	metrics, err := enableOTELMetrics(t.Context(), "test-auth-sidecar", endpoint)
	require.NoError(t, err)
	require.NotNil(t, metrics)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, metrics.Shutdown(shutdownCtx))
		otel.SetMeterProvider(previousMeterProvider)
	})

	recordAuthGRPCRequests(t)
	httpServer := startInstrumentedGateway(t, metrics)
	require.Equal(t, http.StatusOK, getStatus(t, httpServer.URL+"/health"))
	require.Equal(t, http.StatusOK, getStatus(t, httpServer.URL+"/health"))
	require.Equal(t, http.StatusNotFound, getStatus(t, httpServer.URL+"/missing"))
	runtime.GC()

	response, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, http.StatusOK, response.StatusCode)
	prometheusBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(prometheusBody), "http_server_request_duration_seconds_count")
	require.Contains(t, string(prometheusBody), `http_route="/health"`)
	require.Contains(t, string(prometheusBody), `service_name="test-auth-sidecar"`)

	require.NoError(t, metrics.provider.ForceFlush(t.Context()))
	request := receiveAuthMetrics(t, capture.requests)
	require.Equal(t, "auth-test-instance", authResourceAttribute(request, "service.instance.id"))
	metricNames := authExportedMetricNames(request)
	require.Contains(t, metricNames, "go.goroutine.count")
	require.Contains(t, metricNames, "go.gc.cycle.count")
	require.Contains(t, metricNames, "go.gc.pause.cpu_time")
	require.Positive(t, authInt64SumValue(request, "go.gc.cycle.count"))
	require.Positive(t, authFloat64SumValue(request, "go.gc.pause.cpu_time"))

	grpcCounts := authStringAttributeCounts(request, "rpc.server.call.duration", "rpc.response.status_code")
	require.Equal(t, uint64(2), grpcCounts["OK"])
	require.Equal(t, uint64(1), grpcCounts["NOT_FOUND"])

	require.Equal(t, uint64(3), authMetricCount(request, "http.server.request.duration"))
	httpStatuses := authIntAttributeCounts(request, "http.server.request.duration", "http.response.status_code")
	require.Equal(t, uint64(1), httpStatuses[http.StatusNotFound])
	httpRoutes := authStringAttributeCounts(request, "http.server.request.duration", "http.route")
	require.Equal(t, uint64(2), httpRoutes["/health"])
}

func TestGatewayHandlerHasNoMetricsRouteWhenTelemetryIsDisabled(t *testing.T) {
	matcher := NewRouteMatcher([]*RouteEntry{{
		Service: "self",
		Method:  http.MethodGet,
		Path:    "/health",
	}}, nil)
	gateway := NewGateway(nil, matcher, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()

	newGatewayHTTPHandler(gateway, nil).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func authInt64SumValue(request *collectormetricsv1.ExportMetricsServiceRequest, name string) int64 {
	var value int64
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				if metric.GetName() == name {
					for _, point := range metric.GetSum().GetDataPoints() {
						value += point.GetAsInt()
					}
				}
			}
		}
	}
	return value
}

func authFloat64SumValue(request *collectormetricsv1.ExportMetricsServiceRequest, name string) float64 {
	var value float64
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				if metric.GetName() == name {
					for _, point := range metric.GetSum().GetDataPoints() {
						value += point.GetAsDouble()
					}
				}
			}
		}
	}
	return value
}

func startAuthMetricCapture(t *testing.T) (string, *authMetricCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	capture := &authMetricCapture{requests: make(chan *collectormetricsv1.ExportMetricsServiceRequest, 4)}
	server := grpc.NewServer()
	collectormetricsv1.RegisterMetricsServiceServer(server, capture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), capture
}

func recordAuthGRPCRequests(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(wooltel.GRPCServerOptions()...)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(server, healthServer)
	go func() { _ = server.Serve(listener) }()
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
	for range 2 {
		_, err = client.Check(t.Context(), &healthv1.HealthCheckRequest{})
		require.NoError(t, err)
	}
	_, err = client.Check(t.Context(), &healthv1.HealthCheckRequest{Service: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func startInstrumentedGateway(t *testing.T, metrics *otelMetrics) *httptest.Server {
	t.Helper()
	matcher := NewRouteMatcher([]*RouteEntry{{
		Service: "self",
		Method:  http.MethodGet,
		Path:    "/health",
	}}, nil)
	gateway := NewGateway(nil, matcher, nil, nil)
	server := httptest.NewServer(newGatewayHTTPHandler(gateway, metrics))
	t.Cleanup(server.Close)
	return server
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	response, err := http.Get(url)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	return response.StatusCode
}

func receiveAuthMetrics(
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

func authResourceAttribute(request *collectormetricsv1.ExportMetricsServiceRequest, key string) string {
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, attr := range resourceMetrics.GetResource().GetAttributes() {
			if attr.GetKey() == key {
				return attr.GetValue().GetStringValue()
			}
		}
	}
	return ""
}

func authExportedMetricNames(request *collectormetricsv1.ExportMetricsServiceRequest) map[string]bool {
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

func authMetricCount(request *collectormetricsv1.ExportMetricsServiceRequest, name string) uint64 {
	var count uint64
	for _, point := range authHistogramPoints(request, name) {
		count += point.GetCount()
	}
	return count
}

func authStringAttributeCounts(
	request *collectormetricsv1.ExportMetricsServiceRequest,
	name string,
	attributeKey string,
) map[string]uint64 {
	counts := make(map[string]uint64)
	for _, point := range authHistogramPoints(request, name) {
		for _, attr := range point.GetAttributes() {
			if attr.GetKey() == attributeKey {
				counts[attr.GetValue().GetStringValue()] += point.GetCount()
			}
		}
	}
	return counts
}

func authIntAttributeCounts(
	request *collectormetricsv1.ExportMetricsServiceRequest,
	name string,
	attributeKey string,
) map[int]uint64 {
	counts := make(map[int]uint64)
	for _, point := range authHistogramPoints(request, name) {
		for _, attr := range point.GetAttributes() {
			if attr.GetKey() == attributeKey {
				counts[int(attr.GetValue().GetIntValue())] += point.GetCount()
			}
		}
	}
	return counts
}

func authHistogramPoints(
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
