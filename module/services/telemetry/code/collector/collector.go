package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	Exporter   string
	Endpoint   string
	Headers    string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

type Collector struct {
	exporter string
	endpoint string
	headers  http.Header
	client   *http.Client
	logger   *slog.Logger
}

type TraceService struct {
	collectortracev1.UnimplementedTraceServiceServer
	collector *Collector
}

type MetricsService struct {
	collectormetricsv1.UnimplementedMetricsServiceServer
	collector *Collector
}

type LogsService struct {
	collectorlogsv1.UnimplementedLogsServiceServer
	collector *Collector
}

func (c *Collector) TraceService() *TraceService { return &TraceService{collector: c} }

func (c *Collector) MetricsService() *MetricsService {
	return &MetricsService{collector: c}
}

func (c *Collector) LogsService() *LogsService { return &LogsService{collector: c} }

func New(config Config) (*Collector, error) {
	exporter := strings.ToLower(strings.TrimSpace(config.Exporter))
	// Fail closed on an empty exporter instead of defaulting to "debug".
	// An empty value never meant "the operator chose debug" — every committed
	// observability.env sets this key explicitly, and scripts/setup/otel.sh
	// always writes it — it meant the configuration never reached the process.
	// Defaulting it to debug turned that into SILENT trace loss: the collector
	// logged "received OTLP signal" and dropped every span while its own
	// ConfigMap said otlphttp. Refusing to start surfaces the same fault in one
	// line of pod logs.
	if exporter == "" {
		return nil, errors.New(
			"telemetry: OBSERVABILITY_EXPORTER is required and must be debug or otlphttp; " +
				"the observability workspace configuration did not reach this process " +
				"(on the local-dogfood profile, run scripts/setup/otel.sh --debug)")
	}
	switch exporter {
	case "debug":
		if strings.TrimSpace(config.Endpoint) != "" || strings.TrimSpace(config.Headers) != "" {
			return nil, errors.New("telemetry: debug exporter cannot have an external endpoint or headers")
		}
	case "otlphttp":
		endpoint, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.Endpoint), "/"))
		if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
			return nil, errors.New("telemetry: OTLP/HTTP endpoint must be absolute")
		}
		// Plaintext is allowed only to loopback, which never leaves the pod and so
		// is governed by no network policy. Everything else must be HTTPS, because
		// that is the only egress this module actually grants telemetry:
		// services/telemetry/service.codefly.yaml declares spec.deployment.public-egress-ports
		// [443], which renders allow-telemetry-public-egress — TCP 443 to public IP
		// space with 10/8, 172.16/12, 192.168/16 and fc00::/7 excepted — on top of a
		// namespace-wide default-deny that has no allow-intra-namespace rule.
		//
		// A cluster-internal plaintext address (a *.svc name on 4318) is therefore
		// denied at the network layer no matter what this check says; accepting it
		// here would only move the failure from a startup error to a 10s export
		// timeout per batch. Reaching an in-cluster collector is a topology change —
		// a declared dependency edge with its regenerated NetworkPolicy — not a
		// transport exemption in application code.
		local := endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1"
		if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || !local) {
			return nil, errors.New("telemetry: OTLP/HTTP endpoint must use HTTPS")
		}
		config.Endpoint = endpoint.String()
	default:
		return nil, errors.New("telemetry: OBSERVABILITY_EXPORTER must be debug or otlphttp")
	}
	headers, err := parseHeaders(config.Headers)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Collector{
		exporter: exporter,
		endpoint: strings.TrimRight(config.Endpoint, "/"),
		headers:  headers,
		client:   client,
		logger:   logger,
	}, nil
}

func (s *TraceService) Export(
	ctx context.Context,
	request *collectortracev1.ExportTraceServiceRequest,
) (*collectortracev1.ExportTraceServiceResponse, error) {
	if err := s.collector.deliver(ctx, "traces", request, len(request.GetResourceSpans())); err != nil {
		return nil, err
	}
	return &collectortracev1.ExportTraceServiceResponse{}, nil
}

func (s *MetricsService) Export(
	ctx context.Context,
	request *collectormetricsv1.ExportMetricsServiceRequest,
) (*collectormetricsv1.ExportMetricsServiceResponse, error) {
	if err := s.collector.deliver(ctx, "metrics", request, len(request.GetResourceMetrics())); err != nil {
		return nil, err
	}
	return &collectormetricsv1.ExportMetricsServiceResponse{}, nil
}

func (s *LogsService) Export(
	ctx context.Context,
	request *collectorlogsv1.ExportLogsServiceRequest,
) (*collectorlogsv1.ExportLogsServiceResponse, error) {
	if err := s.collector.deliver(ctx, "logs", request, len(request.GetResourceLogs())); err != nil {
		return nil, err
	}
	return &collectorlogsv1.ExportLogsServiceResponse{}, nil
}

func (c *Collector) deliver(ctx context.Context, signal string, message proto.Message, resources int) error {
	if c.exporter == "debug" {
		c.logger.Info("received OTLP signal", "signal", signal, "resources", resources)
		return nil
	}
	body, err := proto.Marshal(message)
	if err != nil {
		return fmt.Errorf("telemetry: encode %s: %w", signal, err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint+"/v1/"+signal,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("telemetry: create %s export: %w", signal, err)
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("telemetry: export %s: %w", signal, err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("telemetry: export %s returned HTTP %d", signal, response.StatusCode)
	}
	return nil
}

func parseHeaders(raw string) (http.Header, error) {
	headers := make(http.Header)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return headers, nil
	}
	values, err := url.ParseQuery(strings.ReplaceAll(raw, ",", "&"))
	if err != nil {
		return nil, errors.New("telemetry: OTLP headers must use URL-encoded key=value pairs")
	}
	for name, entries := range values {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("telemetry: OTLP header name is required")
		}
		for _, value := range entries {
			if strings.ContainsAny(value, "\r\n") {
				return nil, errors.New("telemetry: OTLP header value contains a newline")
			}
			headers.Add(name, value)
		}
	}
	return headers, nil
}
