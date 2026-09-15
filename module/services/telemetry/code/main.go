package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"telemetry/collector"

	codefly "github.com/codefly-dev/sdk-go"
	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// observabilityConfiguration is the workspace configuration group that carries
// the collector's upstream-exporter settings. Values arrive from either half of
// the group — configurations/<env>/observability.env or its .secret.env
// sibling, which is where OTEL_EXPORTER_OTLP_HEADERS lives — because
// WorkspaceValue consults the public namespace first and then the secret one.
const observabilityConfiguration = "observability"

// exporterKeys are the three settings that make up one exporter configuration.
// They are resolved together, never individually; see resolveExporterConfig.
var exporterKeys = struct{ exporter, endpoint, headers string }{
	exporter: "OBSERVABILITY_EXPORTER",
	endpoint: "OTEL_EXPORTER_OTLP_ENDPOINT",
	headers:  "OTEL_EXPORTER_OTLP_HEADERS",
}

// workspaceExporterConfig reads all three settings from the observability
// workspace configuration group, reporting whether the group delivered any of
// them.
//
// Reading the group is load-bearing, not cosmetic. telemetry declares
// `observability` as a workspace-configuration-dependency, and a declared group
// reaches a deployed pod ONLY under prefixed names —
// CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__<KEY>, via envFrom a
// ConfigMap — while codefly.Init builds an in-process map without ever mutating
// the process environment. A bare os.Getenv("OBSERVABILITY_EXPORTER") therefore
// resolved to "" wherever the configuration arrives that way; collector.New
// defaulted that empty value to "debug", and the collector logged and dropped
// every span it received while its own ConfigMap said otlphttp.
func workspaceExporterConfig(ctx context.Context) (collector.Config, bool) {
	value := func(key string) string {
		resolved, err := codefly.For(ctx).WorkspaceValue(observabilityConfiguration, key)
		if err != nil {
			return ""
		}
		return resolved
	}
	config := collector.Config{
		Exporter: value(exporterKeys.exporter),
		Endpoint: value(exporterKeys.endpoint),
		Headers:  value(exporterKeys.headers),
	}
	return config, config.Exporter != "" || config.Endpoint != "" || config.Headers != ""
}

// resolveExporterConfig picks ONE authority for the whole exporter
// configuration: the workspace group if it delivered anything at all, otherwise
// the bare process environment, so a local or non-Codefly run still works.
//
// The choice is deliberately made for the tuple, not per key. collector.New
// cross-validates these three values (debug rejects an endpoint; otlphttp
// requires one), so mixing sources produces configurations no operator wrote.
// Resolving key by key meant a developer who exported OBSERVABILITY_EXPORTER
// and OTEL_EXPORTER_OTLP_ENDPOINT to drive scripts/setup/otel.sh could no
// longer start the collector: the committed local group supplied
// OBSERVABILITY_EXPORTER=debug while its empty endpoint fell through to the
// shell's, and debug+endpoint is refused. The same split let an ambient
// OTEL_EXPORTER_OTLP_HEADERS — the credential, and a name the OpenTelemetry
// spec makes ubiquitous — attach itself to a workspace-configured endpoint it
// was never issued for, whenever the observability.secret.env half was not
// provisioned. One authority for all three makes both states unrepresentable.
func resolveExporterConfig(ctx context.Context) collector.Config {
	if config, delivered := workspaceExporterConfig(ctx); delivered {
		return config
	}
	return collector.Config{
		Exporter: os.Getenv(exporterKeys.exporter),
		Endpoint: os.Getenv(exporterKeys.endpoint),
		Headers:  os.Getenv(exporterKeys.headers),
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	provider, err := codefly.Init(ctx)
	if err != nil {
		log.Fatal(err)
	}
	ctx = provider.Inject(ctx)
	defer codefly.CatchPanic(ctx)

	port := codefly.For(ctx).WithDefaultNetwork().API("grpc").NetworkInstance().Port
	if port == 0 {
		log.Fatal("telemetry: Codefly did not inject the collector gRPC port")
	}
	sink, err := collector.New(resolveExporterConfig(ctx))
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(port))))
	if err != nil {
		log.Fatal(err)
	}
	server := grpc.NewServer()
	collectortracev1.RegisterTraceServiceServer(server, sink.TraceService())
	collectormetricsv1.RegisterMetricsServiceServer(server, sink.MetricsService())
	collectorlogsv1.RegisterLogsServiceServer(server, sink.LogsService())
	go func() {
		if err := server.Serve(listener); err != nil {
			log.Printf("telemetry: serve: %v", err)
			stop()
		}
	}()
	fmt.Printf("OpenTelemetry collector listening on Codefly gRPC endpoint %d\n", port)
	<-ctx.Done()
	server.GracefulStop()
}
