package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	svix "github.com/svix/svix-webhooks/go"
)

type memoryResendRecorder struct {
	mu     sync.Mutex
	events map[string]ResendEvent
	err    error
}

func (r *memoryResendRecorder) RecordResendEvent(
	_ context.Context,
	event ResendEvent,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, r.err
	}
	if r.events == nil {
		r.events = make(map[string]ResendEvent)
	}
	if _, exists := r.events[event.SvixID]; exists {
		return false, nil
	}
	r.events[event.SvixID] = event
	return true, nil
}

func TestResendWebhookVerifiesRawBodyAndDeduplicates(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("resend-test-secret"))
	recorder := &memoryResendRecorder{}
	handler, err := NewResendWebhookHandler(ResendWebhookConfig{
		SigningSecret: secret,
		Recorder:      recorder,
	})
	require.NoError(t, err)

	body := []byte(fmt.Sprintf(`{
		"type":"email.delivered",
		"created_at":%q,
		"data":{
			"email_id":"email_123",
			"to":["private@example.com"],
			"subject":"private subject",
			"tags":{"invitation_id":"01955e5e-9dc7-7c89-b211-754d572db2ab"}
		}
	}`, time.Now().UTC().Format(time.RFC3339Nano)))
	for range 2 {
		request := signedResendRequest(t, secret, "msg_delivery_123", time.Now(), body)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
	}

	require.Len(t, recorder.events, 1)
	event := recorder.events["msg_delivery_123"]
	require.Equal(t, "email.delivered", event.Type)
	require.Equal(t, "email_123", event.ProviderEmailID)
	require.Equal(t, "01955e5e-9dc7-7c89-b211-754d572db2ab", event.InvitationID)
}

func TestResendWebhookRejectsTamperingBeforePersistence(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("resend-test-secret"))
	recorder := &memoryResendRecorder{}
	handler, err := NewResendWebhookHandler(ResendWebhookConfig{
		SigningSecret: secret,
		Recorder:      recorder,
	})
	require.NoError(t, err)

	signedBody := []byte(fmt.Sprintf(
		`{"type":"email.sent","created_at":%q,"data":{"email_id":"email_123"}}`,
		time.Now().UTC().Format(time.RFC3339Nano),
	))
	request := signedResendRequest(t, secret, "msg_tampered", time.Now(), signedBody)
	request.Body = io.NopCloser(bytes.NewReader(bytes.Replace(
		signedBody, []byte("email.sent"), []byte("email.failed"), 1,
	)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Empty(t, recorder.events, "unverified input must never reach persistence")
}

func TestResendWebhookRejectsStaleReplayAndBoundsBody(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("resend-test-secret"))
	recorder := &memoryResendRecorder{}
	handler, err := NewResendWebhookHandler(ResendWebhookConfig{
		SigningSecret: secret,
		Recorder:      recorder,
		MaxBodyBytes:  64,
	})
	require.NoError(t, err)

	body := []byte(fmt.Sprintf(
		`{"type":"email.sent","created_at":%q,"data":{"email_id":"e"}}`,
		time.Now().UTC().Format(time.RFC3339Nano),
	))
	stale := signedResendRequest(t, secret, "msg_old", time.Now().Add(-10*time.Minute), body)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, stale)
	require.Equal(t, http.StatusRequestEntityTooLarge, staleResponse.Code)

	smallHandler, err := NewResendWebhookHandler(ResendWebhookConfig{
		SigningSecret: secret,
		Recorder:      recorder,
	})
	require.NoError(t, err)
	stale = signedResendRequest(t, secret, "msg_old", time.Now().Add(-10*time.Minute), body)
	staleResponse = httptest.NewRecorder()
	smallHandler.ServeHTTP(staleResponse, stale)
	require.Equal(t, http.StatusBadRequest, staleResponse.Code)
	require.Empty(t, recorder.events)
}

func TestResendWebhookReturnsRetryableFailureWhenPersistenceFails(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("resend-test-secret"))
	recorder := &memoryResendRecorder{err: context.DeadlineExceeded}
	handler, err := NewResendWebhookHandler(ResendWebhookConfig{
		SigningSecret: secret,
		Recorder:      recorder,
	})
	require.NoError(t, err)
	body := []byte(fmt.Sprintf(
		`{"type":"email.sent","created_at":%q,"data":{"email_id":"email_123"}}`,
		time.Now().UTC().Format(time.RFC3339Nano),
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedResendRequest(t, secret, "msg_retry", time.Now(), body))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func signedResendRequest(
	t *testing.T,
	secret string,
	id string,
	timestamp time.Time,
	body []byte,
) *http.Request {
	t.Helper()
	verifier, err := svix.NewWebhook(secret)
	require.NoError(t, err)
	signature, err := verifier.Sign(id, timestamp, body)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/email/webhook/resend", bytes.NewReader(body))
	request.Header.Set("svix-id", id)
	request.Header.Set("svix-timestamp", fmt.Sprint(timestamp.Unix()))
	request.Header.Set("svix-signature", signature)
	return request
}

func TestResendSenderDeliveryWebhook(t *testing.T) {
	sender, err := NewResendSender(ResendConfig{APIKey: "re_test", WebhookSecret: "whsec_test"})
	require.NoError(t, err)

	path, handler, err := sender.DeliveryWebhook(&memoryResendRecorder{})
	require.NoError(t, err)
	require.Equal(t, ResendWebhookPath, path)
	require.NotNil(t, handler)

	// Without a signing secret the provider cannot build a verifying handler,
	// so wiring fails closed rather than serving an unverified route.
	senderNoSecret, err := NewResendSender(ResendConfig{APIKey: "re_test"})
	require.NoError(t, err)
	_, _, err = senderNoSecret.DeliveryWebhook(&memoryResendRecorder{})
	require.Error(t, err)
}
