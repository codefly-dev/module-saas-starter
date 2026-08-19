package email

import (
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

const resendProviderName = "resend"

// ResendWebhookConfig configures the Resend delivery-callback handler. Recorder
// is the provider-agnostic sink: this handler decodes and translates Resend's
// Svix events into canonical DeliveryEvents before recording them.
type ResendWebhookConfig struct {
	SigningSecret string
	Recorder      DeliveryEventRecorder
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
		if _, err := cfg.Recorder.RecordDeliveryEvent(r.Context(), event); err != nil {
			http.Error(w, "webhook persistence unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), nil
}

// decodeResendEvent verifies the shape of a Resend event and translates it into
// a canonical, provider-neutral DeliveryEvent. The Svix message id is the
// replay-stable dedup key; the native event name is retained for audit while
// Status carries the canonical projection.
func decodeResendEvent(svixID string, body []byte) (DeliveryEvent, error) {
	var payload struct {
		Type      string    `json:"type"`
		CreatedAt time.Time `json:"created_at"`
		Data      struct {
			EmailID string            `json:"email_id"`
			Tags    map[string]string `json:"tags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return DeliveryEvent{}, err
	}
	eventType := strings.TrimSpace(payload.Type)
	if !supportedResendEmailEvent(eventType) {
		return DeliveryEvent{}, errors.New("unsupported Resend event type")
	}
	event := DeliveryEvent{
		Provider:          resendProviderName,
		EventID:           strings.TrimSpace(svixID),
		ProviderMessageID: strings.TrimSpace(payload.Data.EmailID),
		EventType:         eventType,
		Status:            resendDeliveryStatus(eventType),
		InvitationID:      strings.TrimSpace(payload.Data.Tags["invitation_id"]),
		OccurredAt:        payload.CreatedAt.UTC(),
	}
	if event.EventID == "" || len(event.EventID) > 255 {
		return DeliveryEvent{}, errors.New("invalid Svix event id")
	}
	if event.ProviderMessageID == "" || len(event.ProviderMessageID) > 255 {
		return DeliveryEvent{}, errors.New("invalid Resend email id")
	}
	if event.OccurredAt.IsZero() {
		return DeliveryEvent{}, errors.New("missing Resend event time")
	}
	return event, nil
}

// resendDeliveryStatus maps Resend's native event names onto the canonical
// delivery status. Events that do not advance delivery state (delays, opens,
// clicks) map to the empty status: they are recorded but never projected.
func resendDeliveryStatus(eventType string) DeliveryStatus {
	switch eventType {
	case "email.sent":
		return DeliveryStatusSent
	case "email.delivered":
		return DeliveryStatusDelivered
	case "email.failed", "email.bounced", "email.suppressed":
		return DeliveryStatusBounced
	case "email.complained":
		return DeliveryStatusComplained
	default:
		return ""
	}
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
