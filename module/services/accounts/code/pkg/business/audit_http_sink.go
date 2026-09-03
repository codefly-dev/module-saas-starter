package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HTTPAuditSink delivers one audit event per request as JSON to an
// operator-configured warehouse ingestion endpoint. Retry, ordering, and
// at-least-once are the async feed's responsibility (see audit_jobs.go); this
// sink performs a single attempt and reports success or a retryable failure.
// Events arrive already PII-redacted from the feed.
type HTTPAuditSink struct {
	endpoint string
	token    string
	client   *http.Client
}

type HTTPAuditSinkConfig struct {
	Endpoint string
	Token    string
	Client   *http.Client
}

func NewHTTPAuditSink(config HTTPAuditSinkConfig) (*HTTPAuditSink, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("audit: external sink endpoint must be an http(s) URL")
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HTTPAuditSink{endpoint: config.Endpoint, token: config.Token, client: client}, nil
}

func (s *HTTPAuditSink) Emit(ctx context.Context, entry AuditEntry) error {
	body, err := json.Marshal(newAuditExportPayload(entry))
	if err != nil {
		return fmt.Errorf("audit: encode external event: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("audit: external sink returned HTTP %d", response.StatusCode)
	}
	return nil
}
