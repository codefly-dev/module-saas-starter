package collector_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"telemetry/collector"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/require"
)

func TestCollectorReceivesGRPCAndForwardsOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	var forwarded collectortracev1.ExportTraceServiceRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/traces", r.URL.Path)
		require.Equal(t, "Bearer test", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.NoError(t, proto.Unmarshal(body, &forwarded))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sink, err := collector.New(collector.Config{
		Exporter: "otlphttp",
		Endpoint: upstream.URL,
		Headers:  "Authorization=Bearer+test",
	})
	require.NoError(t, err)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	collectortracev1.RegisterTraceServiceServer(server, sink.TraceService())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	connection, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	require.NoError(t, err)
	defer connection.Close()
	client := collectortracev1.NewTraceServiceClient(connection)
	_, err = client.Export(t.Context(), &collectortracev1.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{{}},
	})
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, forwarded.GetResourceSpans(), 1)
}

func TestCollectorConfigurationFailsClosed(t *testing.T) {
	_, err := collector.New(collector.Config{
		Exporter: "debug", Endpoint: "https://collector.example",
	})
	require.ErrorContains(t, err, "cannot have")
	_, err = collector.New(collector.Config{
		Exporter: "otlphttp", Endpoint: "http://collector.example",
	})
	require.ErrorContains(t, err, "HTTPS")
}
