package email

// Resend implementation of Sender.
//
// API docs: https://resend.com/docs/api-reference/emails/send-email
//
// Minimal wrapper around a single endpoint:
//
//	POST https://api.resend.com/emails
//	Authorization: Bearer re_xxx
//	{ "from", "to", "subject", "html", "text", "tags" }
//
// Keeping it as stdlib HTTP means no vendor SDK to track version on,
// and tests can use httptest against the same code path real sends use.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ResendConfig is the constructor input.
type ResendConfig struct {
	// APIKey is the Resend secret (re_...).
	APIKey string
	// BaseURL defaults to https://api.resend.com. Tests override.
	BaseURL string
	// HTTPClient optional, defaults to a 10s-timeout client.
	HTTPClient *http.Client
}

// ResendSender implements Sender using Resend's HTTP API.
type ResendSender struct {
	cfg  ResendConfig
	base string
	http *http.Client
}

// NewResendSender constructs a ResendSender.
func NewResendSender(cfg ResendConfig) (*ResendSender, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("email: Resend API key is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.resend.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &ResendSender{
		cfg:  cfg,
		base: strings.TrimRight(cfg.BaseURL, "/"),
		http: cfg.HTTPClient,
	}, nil
}

// Send implements Sender.Send against Resend's /emails endpoint.
func (s *ResendSender) Send(ctx context.Context, m *Message) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}

	payload := map[string]any{
		"from":    m.From,
		"to":      m.To,
		"subject": m.Subject,
	}
	if m.HTMLBody != "" {
		payload["html"] = m.HTMLBody
	}
	if m.TextBody != "" {
		payload["text"] = m.TextBody
	}
	if m.ReplyTo != "" {
		payload["reply_to"] = m.ReplyTo
	}
	if len(m.Tags) > 0 {
		tags := make([]map[string]string, 0, len(m.Tags))
		for k, v := range m.Tags {
			tags = append(tags, map[string]string{"name": k, "value": v})
		}
		payload["tags"] = tags
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("email: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/emails", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("email: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("email: send: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("email: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("email: resend http %d: %s", resp.StatusCode, truncate(respBody, 500))
	}

	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("email: decode response: %w", err)
	}
	return decoded.ID, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
