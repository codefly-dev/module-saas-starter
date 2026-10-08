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
	defer func() { _ = connection.Close() }()
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

// TestCollectorRequiresExplicitExporter pins the fail-closed behaviour: an empty
// OBSERVABILITY_EXPORTER used to fall back to "debug", which turned a broken
// configuration carrier into silent trace loss instead of a startup failure.
func TestCollectorRequiresExplicitExporter(t *testing.T) {
	_, err := collector.New(collector.Config{})
	require.ErrorContains(t, err, "OBSERVABILITY_EXPORTER is required")

	_, err = collector.New(collector.Config{Exporter: "   "})
	require.ErrorContains(t, err, "OBSERVABILITY_EXPORTER is required")
}

// TestCollectorEndpointTransportPolicy pins which OTLP/HTTP destinations may be
// reached in plaintext: loopback only, because loopback never leaves the pod.
//
// The cluster-internal cases are the regression. Exempting *.svc /
// *.svc.cluster.local from the HTTPS requirement looks safe — those names
// resolve only inside the cluster — but this module grants telemetry exactly one
// egress path: public_egress_ports [443] in
// services/telemetry/service.codefly.yaml (spec.deployment), rendered as
// allow-telemetry-public-egress (TCP 443 to public IP space, private ranges
// excepted) over a namespace-wide default-deny with no allow-intra-namespace
// rule. A ClusterIP on 4318 is denied there no matter what this package accepts,
// so accepting it only converts a startup error into a 10s export timeout per
// batch. Reaching an in-cluster collector is a topology change, not a transport
// exemption in application code.
func TestCollectorEndpointTransportPolicy(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		endpoint string
		accepted bool
	}{
		{"loopback name", "http://localhost:4318", true},
		{"loopback address", "http://127.0.0.1:4318", true},
		{"external TLS", "https://example.com", true},
		{"in-cluster collector FQDN is not reachable and not exempt",
			"http://otel-collector.otel-collector.svc.cluster.local:4318", false},
		{"in-cluster collector short form", "http://otel-collector.otel-collector.svc:4318", false},
		{"external plaintext", "http://example.com:4318", false},
		{"external plaintext subdomain", "http://otel.internal.ops.example.com:4318", false},
		{"host merely containing svc", "http://svc.example.com:4318", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := collector.New(collector.Config{
				Exporter: "otlphttp",
				Endpoint: testCase.endpoint,
			})
			if testCase.accepted {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "HTTPS")
		})
	}
}
