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
	if exporter == "" {
		exporter = "debug"
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
	defer response.Body.Close()
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
