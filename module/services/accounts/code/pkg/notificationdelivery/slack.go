package notificationdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Slack uses a bot token supplied by the host's encrypted installation store.
// It does not share the platform-operations incoming webhook. The endpoint is
// fixed; redirects are refused so a bearer token cannot escape to another host.
type Slack struct {
	token  string
	client *http.Client
}

func NewSlack(botToken string) *Slack {
	return &Slack{token: botToken, client: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var slackConversation = regexp.MustCompile(`^[CDG][A-Z0-9]{1,79}$`)

func (s *Slack) Send(ctx context.Context, message Message) (Result, error) {
	if s.token == "" {
		return Result{}, ErrNotConfigured
	}
	if message.EffectID == "" || !slackConversation.MatchString(message.Destination) || message.Title == "" || utf8.RuneCountInString(message.Title) > 150 || utf8.RuneCountInString(message.Body) > 3000 {
		return Result{}, ErrInvalidMessage
	}
	// Plain-text blocks and disabled parsing/unfurls prevent notification text
	// from triggering mentions or external fetches. Escape the fallback as well.
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	blocks := []any{map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": message.Title, "emoji": false}}}
	if message.Body != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "plain_text", "text": message.Body, "emoji": false}})
	}
	body, err := json.Marshal(map[string]any{
		"channel": message.Destination, "text": escape.Replace(message.Title + "\n" + message.Body),
		"blocks": blocks, "parse": "none", "mrkdwn": false, "unfurl_links": false, "unfurl_media": false,
	})
	if err != nil {
		return Result{}, ErrInvalidMessage
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return Result{}, ErrInvalidMessage
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := s.client.Do(req)
	if err != nil {
		return Result{}, &Failure{Code: "transport_error", Ambiguous: true}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusTooManyRequests {
		seconds, err := strconv.Atoi(response.Header.Get("Retry-After"))
		if err != nil || seconds < 1 || seconds > 86400 {
			seconds = 60
		}
		return Result{}, &Failure{Code: "rate_limited", RetryAfter: time.Duration(seconds) * time.Second}
	}
	if response.StatusCode != http.StatusOK {
		return Result{}, &Failure{Code: "http_error", Ambiguous: response.StatusCode >= 500}
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    string `json:"ts"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result); err != nil {
		return Result{}, &Failure{Code: "invalid_response", Ambiguous: true}
	}
	if !result.OK {
		// Use a closed public vocabulary. Slack error bodies are untrusted and may
		// include installation details that don't belong in application logs.
		code := "provider_rejected"
		ambiguous := true
		switch result.Error {
		case "invalid_auth", "token_revoked", "account_inactive":
			code = "installation_revoked"
			ambiguous = false
		case "channel_not_found", "not_in_channel", "is_archived":
			code = "destination_unavailable"
			ambiguous = false
		case "ratelimited":
			return Result{}, &Failure{Code: "rate_limited", RetryAfter: time.Minute}
		}
		return Result{}, &Failure{Code: code, Ambiguous: ambiguous}
	}
	if result.TS == "" {
		return Result{}, &Failure{Code: "missing_message_id", Ambiguous: true}
	}
	return Result{ProviderMessageID: result.TS}, nil
}
