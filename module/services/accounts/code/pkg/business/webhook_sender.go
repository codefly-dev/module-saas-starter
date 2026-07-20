package business

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const webhookSignatureVersion = "v1"

// WebhookSender is the transport adapter used only behind the generated
// outbound-webhook job handler. Product persistence is projected separately.
type WebhookSender struct {
	cipher SecretCipher
	client *http.Client
	now    func() time.Time
}

type webhookAttemptResult struct {
	HTTPStatus   int
	ResponseBody string
}

// NewWebhookSender creates a fail-closed sender. The policy validates DNS at
// socket-connect time, pins the checked address, and refuses redirects.
func NewWebhookSender(cipher SecretCipher, policy *WebhookEndpointPolicy) *WebhookSender {
	return &WebhookSender{
		cipher: cipher,
		client: policy.ensureDefaults().HTTPClient(),
		now:    time.Now,
	}
}

// NewWebhookSenderWithClient is an explicit test seam. Production wiring must
// use NewWebhookSender so endpoint policy cannot be bypassed accidentally.
func NewWebhookSenderWithClient(cipher SecretCipher, client *http.Client) *WebhookSender {
	return &WebhookSender{cipher: cipher, client: client, now: time.Now}
}

func (s *WebhookSender) attempt(
	ctx context.Context,
	sub *WebhookSubscription,
	delivery *WebhookDelivery,
	payloadBytes []byte,
) (webhookAttemptResult, error) {
	current, previous, err := s.signingSecrets(ctx, sub)
	if err != nil {
		return webhookAttemptResult{}, err
	}
	if delivery.EventID == "" {
		delivery.EventID = delivery.ID
	}
	timestamp := s.now().UTC().Unix()
	signature := webhookSignatureHeader(timestamp, delivery.EventID, payloadBytes, current, previous)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payloadBytes))
	if err != nil {
		return webhookAttemptResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Codefly-Webhook/1.0")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Delivery-ID", delivery.ID)
	req.Header.Set("X-Webhook-Event-ID", delivery.EventID)
	req.Header.Set("X-Webhook-Event", delivery.EventType)

	resp, err := s.client.Do(req)
	if err != nil {
		return webhookAttemptResult{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return webhookAttemptResult{HTTPStatus: resp.StatusCode, ResponseBody: string(body)}, nil
}

func (s *WebhookSender) signingSecrets(ctx context.Context, sub *WebhookSubscription) (string, string, error) {
	if s.cipher == nil {
		return "", "", fmt.Errorf("webhook secret cipher is not configured")
	}
	purpose := WebhookSecretPurpose(sub.ID)
	current, err := s.cipher.DecryptSecret(ctx, purpose, sub.SecretEncrypted)
	if err != nil {
		return "", "", fmt.Errorf("decrypt current webhook secret: %w", err)
	}
	var previous string
	if sub.PreviousSecretEncrypted != "" && sub.PreviousSecretExpiresAt != nil && s.now().Before(*sub.PreviousSecretExpiresAt) {
		previous, err = s.cipher.DecryptSecret(ctx, purpose, sub.PreviousSecretEncrypted)
		if err != nil {
			return "", "", fmt.Errorf("decrypt previous webhook secret: %w", err)
		}
	}
	return current, previous, nil
}

// webhookSignatureHeader follows a Stripe-style, versioned format. Consumers
// verify HMAC-SHA256(secret, "<unix>.<event-id>.<exact-body>") and should reject
// stale timestamps plus already-processed event IDs. During key overlap the
// header contains two v1 values; accepting either enables zero-downtime rotation.
func webhookSignatureHeader(timestamp int64, eventID string, body []byte, secrets ...string) string {
	signed := make([]byte, 0, len(body)+len(eventID)+32)
	signed = strconv.AppendInt(signed, timestamp, 10)
	signed = append(signed, '.')
	signed = append(signed, eventID...)
	signed = append(signed, '.')
	signed = append(signed, body...)

	parts := []string{"t=" + strconv.FormatInt(timestamp, 10)}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(signed)
		parts = append(parts, webhookSignatureVersion+"="+hex.EncodeToString(mac.Sum(nil)))
	}
	return strings.Join(parts, ",")
}
