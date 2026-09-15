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
// the collector's upstream-exporter settings (configurations/<env>/observability.env).
const observabilityConfiguration = "observability"

// workspaceEnv reads a key from a named Codefly workspace configuration,
// including its secret namespace, and falls back to a plain process variable
// for deployments that do not use Codefly's configuration provider. It is the
// same accessor accounts (work.go) and auth-gateway (configuration.go) already
// use for their declared workspace-configuration-dependencies.
//
// The accessor is load-bearing, not cosmetic. telemetry declares
// `observability` as a workspace-configuration-dependency, and a declared group
// reaches a deployed pod ONLY under prefixed names —
// CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__<KEY>, via envFrom a
// ConfigMap — while codefly.Init builds an in-process map without ever mutating
// the process environment. A bare os.Getenv("OBSERVABILITY_EXPORTER") therefore
// resolved to "" wherever the configuration arrives that way; collector.New
// defaulted that empty value to "debug", and the collector logged and dropped
// every span it received while its own ConfigMap said otlphttp.
func workspaceEnv(ctx context.Context, configuration, key string) string {
	if value, err := codefly.For(ctx).WorkspaceValue(configuration, key); err == nil && value != "" {
		return value
	}
	return os.Getenv(key)
}

// resolveExporterConfig reads the three upstream-exporter settings from the
// observability workspace configuration group, preferring the injected
// workspace value and falling back to a bare process variable so a local or
// non-Codefly run still works.
func resolveExporterConfig(ctx context.Context) collector.Config {
	return collector.Config{
		Exporter: workspaceEnv(ctx, observabilityConfiguration, "OBSERVABILITY_EXPORTER"),
		Endpoint: workspaceEnv(ctx, observabilityConfiguration, "OTEL_EXPORTER_OTLP_ENDPOINT"),
		Headers:  workspaceEnv(ctx, observabilityConfiguration, "OTEL_EXPORTER_OTLP_HEADERS"),
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
