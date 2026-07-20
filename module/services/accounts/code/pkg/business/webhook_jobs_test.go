package business

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type webhookJobCipher struct{}

func (webhookJobCipher) EncryptSecret(context.Context, string, string) (string, error) {
	return "", errors.New("not used")
}

func (webhookJobCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return envelope, nil
}

type webhookJobProjection struct {
	delivery     *WebhookDelivery
	subscription *WebhookSubscription
	attempts     []OutboundWebhookAttempt
	loadErr      error
	recordErr    error
}

func (p *webhookJobProjection) LoadOutboundWebhookDelivery(
	context.Context,
	string,
) (*WebhookDelivery, *WebhookSubscription, error) {
	return p.delivery, p.subscription, p.loadErr
}

func (p *webhookJobProjection) RecordOutboundWebhookAttempt(
	_ context.Context,
	attempt OutboundWebhookAttempt,
) error {
	if p.recordErr != nil {
		return p.recordErr
	}
	p.attempts = append(p.attempts, attempt)
	if attempt.DeliveredAt == nil {
		p.delivery.Status = "failed"
	} else {
		p.delivery.Status = "delivered"
	}
	return nil
}

func outboundWebhookFixture(t *testing.T, endpoint string) (*WebhookDelivery, *WebhookSubscription, *jobsv1.JobEnvelope) {
	t.Helper()
	orgID := uuid.NewString()
	subscriptionID := uuid.NewString()
	delivery := &WebhookDelivery{
		ID: uuid.NewString(), SubscriptionID: subscriptionID,
		EventID: uuid.NewString(), EventType: "user.registered",
		Payload: `{"exact": "bytes stay exact"}`, Status: "pending",
	}
	delivery.OutboxEventID = delivery.EventID
	subscription := &WebhookSubscription{
		ID: subscriptionID, OrgID: orgID, URL: endpoint,
		SecretEncrypted: "whsec_test", Active: true,
	}
	request, err := newOutboundWebhookJob(orgID, delivery, []byte(delivery.Payload))
	require.NoError(t, err)
	job := request.GetJob()
	ordering, err := jobs.CanonicalOrderingKey(job.GetOrdering())
	require.NoError(t, err)
	envelope := &jobsv1.JobEnvelope{
		Id: uuid.NewString(), Direction: job.GetDirection(), Scope: job.GetScope(),
		Queue: job.GetQueue(), Topic: job.GetTopic(), Source: job.GetSource(),
		IdempotencyKey: job.GetIdempotencyKey(), OrderingKey: ordering,
		SchemaVersion: job.GetSchemaVersion(), Payload: job.GetPayload(),
		ContentType: job.GetContentType(), State: jobsv1.JobState_JOB_STATE_PROCESSING,
		AttemptCount: 1, MaxAttempts: job.GetMaxAttempts(),
	}
	return delivery, subscription, envelope
}

func TestOutboundWebhookJobSendsExactBodyAndProjectsSuccess(t *testing.T) {
	var body []byte
	var deliveryHeader, eventHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		deliveryHeader = r.Header.Get("X-Webhook-Delivery-ID")
		eventHeader = r.Header.Get("X-Webhook-Event-ID")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	delivery, subscription, envelope := outboundWebhookFixture(t, server.URL)
	projection := &webhookJobProjection{delivery: delivery, subscription: subscription}
	sender := NewWebhookSenderWithClient(webhookJobCipher{}, server.Client())
	handler, err := NewOutboundWebhookJobHandler(projection, sender)
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelope))

	require.Equal(t, delivery.Payload, string(body))
	require.Equal(t, delivery.ID, deliveryHeader)
	require.Equal(t, delivery.EventID, eventHeader)
	require.Len(t, projection.attempts, 1)
	require.Equal(t, uint32(1), projection.attempts[0].Attempt)
	require.Equal(t, http.StatusNoContent, projection.attempts[0].HTTPStatus)
	require.NotNil(t, projection.attempts[0].DeliveredAt)
}

func TestOutboundWebhookJobAcceptsExactPayloadReplayIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	delivery, subscription, envelope := outboundWebhookFixture(t, server.URL)
	envelope.IdempotencyKey = "operator-replay-1"
	envelope.ReplayOf = uuid.NewString()
	projection := &webhookJobProjection{delivery: delivery, subscription: subscription}
	handler, err := NewOutboundWebhookJobHandler(
		projection, NewWebhookSenderWithClient(webhookJobCipher{}, server.Client()),
	)
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelope))
	require.Len(t, projection.attempts, 1)
}

func TestOutboundWebhookJobLeavesEndpointFailureRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("try later"))
	}))
	t.Cleanup(server.Close)

	delivery, subscription, envelope := outboundWebhookFixture(t, server.URL)
	projection := &webhookJobProjection{delivery: delivery, subscription: subscription}
	handler, err := NewOutboundWebhookJobHandler(
		projection, NewWebhookSenderWithClient(webhookJobCipher{}, server.Client()),
	)
	require.NoError(t, err)
	err = handler(t.Context(), envelope)
	require.Error(t, err)
	var processingErr *jobs.ProcessingError
	require.False(t, errors.As(err, &processingErr), "endpoint details must be redacted by the generic worker")
	require.Len(t, projection.attempts, 1)
	require.Equal(t, http.StatusServiceUnavailable, projection.attempts[0].HTTPStatus)
	require.Equal(t, "try later", projection.attempts[0].ResponseBody)
	require.Nil(t, projection.attempts[0].DeliveredAt)
}

func TestOutboundWebhookJobRejectsRoutingPermanently(t *testing.T) {
	delivery, subscription, envelope := outboundWebhookFixture(t, "https://example.com/hook")
	projection := &webhookJobProjection{delivery: delivery, subscription: subscription}
	handler, err := NewOutboundWebhookJobHandler(
		projection, NewWebhookSenderWithClient(webhookJobCipher{}, http.DefaultClient),
	)
	require.NoError(t, err)
	envelope.Topic = "webhook.delivery.wrong"
	err = handler(t.Context(), envelope)
	var processingErr *jobs.ProcessingError
	require.ErrorAs(t, err, &processingErr)
	require.False(t, processingErr.Retryable)
	require.Equal(t, "webhooks.invalid_job", processingErr.Failure.GetCode())
	require.Empty(t, projection.attempts)
}

func TestOutboundWebhookJobSkipsAlreadyProjectedSuccess(t *testing.T) {
	delivery, subscription, envelope := outboundWebhookFixture(t, "https://example.com/hook")
	delivery.Status = "delivered"
	projection := &webhookJobProjection{delivery: delivery, subscription: subscription}
	handler, err := NewOutboundWebhookJobHandler(
		projection, NewWebhookSenderWithClient(webhookJobCipher{}, http.DefaultClient),
	)
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelope))
	require.Empty(t, projection.attempts)
}

func TestOutboundWebhookRetryDelayIsBounded(t *testing.T) {
	require.Equal(t, 5*time.Second, OutboundWebhookRetryDelay(1))
	require.Equal(t, 30*time.Second, OutboundWebhookRetryDelay(2))
	require.Equal(t, 2*time.Hour, OutboundWebhookRetryDelay(100))
}
