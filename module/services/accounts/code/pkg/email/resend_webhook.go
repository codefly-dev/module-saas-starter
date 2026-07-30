package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	svix "github.com/svix/svix-webhooks/go"
)

const defaultResendWebhookBodyLimit int64 = 256 << 10

// ResendEvent is the privacy-minimized delivery projection retained from a
// verified Resend webhook. Recipient addresses, subjects, click URLs, bounce
// messages, and the raw payload are deliberately excluded.
type ResendEvent struct {
	SvixID          string
	Type            string
	ProviderEmailID string
	InvitationID    string
	CreatedAt       time.Time
}

// ResendEventRecorder durably deduplicates events by SvixID and projects
// delivery state. The bool is true only for the first accepted delivery.
type ResendEventRecorder interface {
	RecordResendEvent(ctx context.Context, event ResendEvent) (bool, error)
}

type ResendWebhookConfig struct {
	SigningSecret string
	Recorder      ResendEventRecorder
	MaxBodyBytes  int64
}

// NewResendWebhookHandler verifies Resend's Svix signature against the exact
// raw body before decoding or persisting anything.
func NewResendWebhookHandler(cfg ResendWebhookConfig) (http.Handler, error) {
	if strings.TrimSpace(cfg.SigningSecret) == "" {
		return nil, errors.New("email: Resend webhook signing secret is required")
	}
	if cfg.Recorder == nil {
		return nil, errors.New("email: Resend webhook recorder is required")
	}
	verifier, err := svix.NewWebhook(strings.TrimSpace(cfg.SigningSecret))
	if err != nil {
		return nil, fmt.Errorf("email: invalid Resend webhook signing secret: %w", err)
	}
	limit := cfg.MaxBodyBytes
	if limit == 0 {
		limit = defaultResendWebhookBodyLimit
	}
	if limit < 1 {
		return nil, errors.New("email: Resend webhook body limit must be positive")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			http.Error(w, "invalid webhook body", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > limit {
			http.Error(w, "webhook body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := verifier.Verify(body, r.Header); err != nil {
			http.Error(w, "invalid webhook signature", http.StatusBadRequest)
			return
		}
		event, err := decodeResendEvent(r.Header.Get("svix-id"), body)
		if err != nil {
			http.Error(w, "invalid webhook event", http.StatusBadRequest)
			return
		}
		if _, err := cfg.Recorder.RecordResendEvent(r.Context(), event); err != nil {
			http.Error(w, "webhook persistence unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), nil
}

func decodeResendEvent(svixID string, body []byte) (ResendEvent, error) {
	var payload struct {
		Type      string    `json:"type"`
		CreatedAt time.Time `json:"created_at"`
		Data      struct {
			EmailID string            `json:"email_id"`
			Tags    map[string]string `json:"tags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ResendEvent{}, err
	}
	event := ResendEvent{
		SvixID:          strings.TrimSpace(svixID),
		Type:            strings.TrimSpace(payload.Type),
		ProviderEmailID: strings.TrimSpace(payload.Data.EmailID),
		InvitationID:    strings.TrimSpace(payload.Data.Tags["invitation_id"]),
		CreatedAt:       payload.CreatedAt.UTC(),
	}
	if event.SvixID == "" || len(event.SvixID) > 255 {
		return ResendEvent{}, errors.New("invalid Svix event id")
	}
	if !supportedResendEmailEvent(event.Type) {
		return ResendEvent{}, errors.New("unsupported Resend event type")
	}
	if event.ProviderEmailID == "" || len(event.ProviderEmailID) > 255 {
		return ResendEvent{}, errors.New("invalid Resend email id")
	}
	if event.CreatedAt.IsZero() {
		return ResendEvent{}, errors.New("missing Resend event time")
	}
	return event, nil
}

func supportedResendEmailEvent(eventType string) bool {
	switch eventType {
	case "email.sent",
		"email.delivered",
		"email.delivery_delayed",
		"email.failed",
		"email.bounced",
		"email.complained",
		"email.opened",
		"email.clicked",
		"email.suppressed":
		return true
	default:
		return false
	}
}
