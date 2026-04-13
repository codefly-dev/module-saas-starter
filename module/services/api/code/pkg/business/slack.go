package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SlackNotifier sends messages to a Slack channel via an incoming webhook URL.
type SlackNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewSlackNotifier creates a Slack notifier for the given webhook URL.
func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		webhookURL: webhookURL,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type slackMessage struct {
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text"`
}

// Send posts a message to the configured Slack webhook.
// The channel parameter is optional — if empty, the webhook's default channel is used.
func (s *SlackNotifier) Send(ctx context.Context, channel, text string) error {
	if s.webhookURL == "" {
		return nil
	}

	msg := slackMessage{
		Channel: channel,
		Text:    text,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("slack: marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: unexpected status %d", resp.StatusCode)
	}

	return nil
}
