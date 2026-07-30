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

	"github.com/codefly-dev/sdk-go"
	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

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
	sink, err := collector.New(collector.Config{
		Exporter: os.Getenv("OBSERVABILITY_EXPORTER"),
		Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		Headers:  os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
	})
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
