package billing_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/billing"
	billingv1 "accounts/pkg/gen/saas/billing/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const webhookSecret = "whsec_test_fake_secret"

type fakeJobProducer struct {
	mu              sync.Mutex
	requests        map[string]*jobsv1.EnqueueJobRequest
	fingerprints    map[string][sha256.Size]byte
	enqueueErr      error
	enqueueResponse *jobsv1.EnqueueJobResponse
	inserts         atomic.Int32
}

func newFakeJobProducer() *fakeJobProducer {
	return &fakeJobProducer{
		requests:     make(map[string]*jobsv1.EnqueueJobRequest),
		fingerprints: make(map[string][sha256.Size]byte),
	}
}

func (producer *fakeJobProducer) EnqueueJob(_ context.Context, request *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	if producer.enqueueErr != nil {
		return nil, producer.enqueueErr
	}
	if producer.enqueueResponse != nil {
		return producer.enqueueResponse, nil
	}
	fingerprint, err := jobs.EnqueueFingerprint(request)
	if err != nil {
		return nil, err
	}
	key := request.GetJob().GetIdempotencyKey()
	producer.mu.Lock()
	defer producer.mu.Unlock()
	if stored, exists := producer.fingerprints[key]; exists {
		if stored != fingerprint {
			return nil, jobs.ErrIdempotencyConflict
		}
		return &jobsv1.EnqueueJobResponse{
			JobId:       "755b7765-9500-43a6-b355-e71581a92a77",
			Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE,
		}, nil
	}
	producer.fingerprints[key] = fingerprint
	producer.requests[key] = proto.Clone(request).(*jobsv1.EnqueueJobRequest)
	producer.inserts.Add(1)
	return &jobsv1.EnqueueJobResponse{
		JobId:       "755b7765-9500-43a6-b355-e71581a92a77",
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func (producer *fakeJobProducer) request(id string) (*jobsv1.EnqueueJobRequest, bool) {
	producer.mu.Lock()
	defer producer.mu.Unlock()
	request, ok := producer.requests[id]
	return request, ok
}

func signBody(body []byte) string {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func eventBody(id, eventType string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"type":%q,"created":1700000000,"api_version":"2026-06-30.basil","livemode":true,"data":{"object":{}}}`, id, eventType))
}

func postEvent(t *testing.T, server *httptest.Server, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signBody(body))
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return response
}

func newTestHandler(t *testing.T, producer *fakeJobProducer) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(billing.NewHandler(billing.HandlerDeps{
		Producer:      producer,
		WebhookSecret: webhookSecret,
	}))
	t.Cleanup(server.Close)
	return server
}

func decodeStatus(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	var value map[string]string
	require.NoError(t, json.Unmarshal(body, &value))
	if status := value["status"]; status != "" {
		return status
	}
	return value["error"]
}

func TestHandlerVerifiesAndDurablyQueuesGeneratedExactPayload(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestHandler(t, producer)
	body := eventBody("evt_queued", "customer.subscription.created")

	response := postEvent(t, server, body)
	defer response.Body.Close()

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "queued", decodeStatus(t, response))
	request, ok := producer.request("evt_queued")
	require.True(t, ok)
	job := request.GetJob()
	require.Equal(t, jobsv1.JobDirection_JOB_DIRECTION_INBOX, job.GetDirection())
	require.True(t, job.GetScope().GetGlobal())
	require.Equal(t, billing.StripeWebhookQueue, job.GetQueue())
	require.Equal(t, billing.StripeWebhookTopic, job.GetTopic())
	require.Equal(t, billing.StripeWebhookSource, job.GetSource())
	require.EqualValues(t, billing.StripeWebhookSchemaVersion, job.GetSchemaVersion())
	require.EqualValues(t, billing.StripeWebhookMaxAttempts, job.GetMaxAttempts())
	require.Equal(t, "application/protobuf", job.GetContentType())

	payload := &billingv1.StripeWebhookJob{}
	require.NoError(t, proto.Unmarshal(job.GetPayload(), payload))
	require.Equal(t, "evt_queued", payload.GetEventId())
	require.Equal(t, "customer.subscription.created", payload.GetEventType())
	require.Equal(t, body, payload.GetRawBody())
	require.Equal(t, "2026-06-30.basil", payload.GetApiVersion())
	require.True(t, payload.GetLivemode())
	require.Equal(t, time.Unix(1700000000, 0).UTC(), payload.GetStripeCreatedAt().AsTime())
}

func TestHandlerAcknowledgesExactDuplicateAndRejectsConflictingReuse(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestHandler(t, producer)
	original := eventBody("evt_duplicate", "customer.subscription.created")

	first := postEvent(t, server, original)
	first.Body.Close()
	duplicate := postEvent(t, server, original)
	defer duplicate.Body.Close()
	require.Equal(t, http.StatusOK, duplicate.StatusCode)
	require.Equal(t, "duplicate", decodeStatus(t, duplicate))

	conflict := postEvent(t, server, eventBody("evt_duplicate", "customer.subscription.deleted"))
	defer conflict.Body.Close()
	require.Equal(t, http.StatusConflict, conflict.StatusCode)
	require.Equal(t, "event conflict", decodeStatus(t, conflict))
	require.EqualValues(t, 1, producer.inserts.Load())
}

func TestHandlerConcurrentDuplicateDeliveryInsertsOnce(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestHandler(t, producer)
	body := eventBody("evt_concurrent", "invoice.paid")

	const deliveries = 32
	var wait sync.WaitGroup
	statuses := make(chan int, deliveries)
	for range deliveries {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := postEvent(t, server, body)
			statuses <- response.StatusCode
			response.Body.Close()
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, http.StatusOK, status)
	}
	require.EqualValues(t, 1, producer.inserts.Load())
}

func TestHandlerInvalidSignatureDoesNotTouchJobs(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestHandler(t, producer)
	body := eventBody("evt_tampered", "invoice.paid")
	request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerPersistenceFailureReturns500ForStripeRetry(t *testing.T) {
	producer := newFakeJobProducer()
	producer.enqueueErr = errors.New("database unavailable")
	server := newTestHandler(t, producer)

	response := postEvent(t, server, eventBody("evt_retry", "invoice.paid"))
	defer response.Body.Close()

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerMissingDurableDispositionReturns500ForStripeRetry(t *testing.T) {
	producer := newFakeJobProducer()
	producer.enqueueResponse = &jobsv1.EnqueueJobResponse{}
	server := newTestHandler(t, producer)

	response := postEvent(t, server, eventBody("evt_unspecified", "invoice.paid"))
	defer response.Body.Close()

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
}

func TestHandlerGETRejected(t *testing.T) {
	server := newTestHandler(t, newFakeJobProducer())
	response, err := http.Get(server.URL)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
}

type recordingProcessor struct {
	event *billingv1.StripeWebhookJob
	err   error
}

func (processor *recordingProcessor) ProcessWebhook(_ context.Context, event *billingv1.StripeWebhookJob) error {
	processor.event = proto.Clone(event).(*billingv1.StripeWebhookJob)
	return processor.err
}

func stripeEnvelope(t *testing.T) *jobsv1.JobEnvelope {
	t.Helper()
	payload := &billingv1.StripeWebhookJob{
		EventId: "evt_job", EventType: "invoice.paid",
		RawBody: eventBody("evt_job", "invoice.paid"),
	}
	encoded, err := proto.Marshal(payload)
	require.NoError(t, err)
	return &jobsv1.JobEnvelope{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:          billing.StripeWebhookQueue,
		Topic:          billing.StripeWebhookTopic,
		Source:         billing.StripeWebhookSource,
		IdempotencyKey: payload.GetEventId(),
		SchemaVersion:  billing.StripeWebhookSchemaVersion,
		Payload:        encoded,
		ContentType:    "application/protobuf",
		MaxAttempts:    billing.StripeWebhookMaxAttempts,
	}
}

func TestStripeWebhookJobHandlerAcceptsOnlyCanonicalGeneratedPayload(t *testing.T) {
	processor := &recordingProcessor{}
	handler, err := billing.NewStripeWebhookJobHandler(processor)
	require.NoError(t, err)

	require.NoError(t, handler(context.Background(), stripeEnvelope(t)))
	require.Equal(t, "evt_job", processor.event.GetEventId())
	require.Equal(t, eventBody("evt_job", "invoice.paid"), processor.event.GetRawBody())

	replay := stripeEnvelope(t)
	replay.IdempotencyKey = "operator-replay-1"
	replay.ReplayOf = uuid.NewString()
	require.NoError(t, handler(context.Background(), replay))
	require.Equal(t, "evt_job", processor.event.GetEventId())

	invalid := stripeEnvelope(t)
	invalid.Topic = "another.topic"
	err = handler(context.Background(), invalid)
	var processingError *jobs.ProcessingError
	require.ErrorAs(t, err, &processingError)
	require.False(t, processingError.Retryable)
	require.Equal(t, "billing.invalid_job", processingError.Failure.GetCode())
	require.NotContains(t, processingError.Failure.GetMessage(), "another.topic")
}

func TestStripeWebhookJobHandlerLeavesProjectionFailuresRetryableAndRedactable(t *testing.T) {
	projectorErr := errors.New("provider token must not enter durable history")
	handler, err := billing.NewStripeWebhookJobHandler(&recordingProcessor{err: projectorErr})
	require.NoError(t, err)

	require.ErrorIs(t, handler(context.Background(), stripeEnvelope(t)), projectorErr)
}
