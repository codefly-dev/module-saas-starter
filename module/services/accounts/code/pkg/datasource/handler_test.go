package datasource_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"accounts/pkg/datasource"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	testSourceID = "acme-docs"
	testSecret   = "whsec_github_fake_secret"
)

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
	// Enforce the same command contract the real producer does, so a job that
	// violates saas.jobs.v1 validation fails the test instead of silently
	// "queuing".
	if err := jobs.ValidateCommand(request.GetJob()); err != nil {
		return nil, err
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

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newTestServer(t *testing.T, producer *fakeJobProducer) *httptest.Server {
	t.Helper()
	resolver, err := datasource.NewStaticSecretResolver(map[string]string{testSourceID: testSecret})
	require.NoError(t, err)
	handler := datasource.NewHandler(datasource.GitHubWebhookPath, datasource.HandlerDeps{
		Producer: producer,
		Secrets:  resolver,
	})
	// The httptest server serves from "/", so mount the handler under the same
	// prefix the receiver strips to recover the source id.
	mux := http.NewServeMux()
	mux.Handle(datasource.GitHubWebhookPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

type pushOptions struct {
	sourceID  string
	delivery  string
	event     string
	signature string // when set, overrides the computed signature
	secret    string // signing secret; defaults to testSecret
}

func postPush(t *testing.T, server *httptest.Server, body []byte, opts pushOptions) *http.Response {
	t.Helper()
	if opts.sourceID == "" {
		opts.sourceID = testSourceID
	}
	if opts.event == "" {
		opts.event = "push"
	}
	if opts.secret == "" {
		opts.secret = testSecret
	}
	url := server.URL + datasource.GitHubWebhookPath + opts.sourceID
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if opts.delivery != "" {
		req.Header.Set("X-GitHub-Delivery", opts.delivery)
	}
	req.Header.Set("X-GitHub-Event", opts.event)
	if opts.signature != "" {
		req.Header.Set("X-Hub-Signature-256", opts.signature)
	} else {
		req.Header.Set("X-Hub-Signature-256", signBody(opts.secret, body))
	}
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return response
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

func pushBody(ref string) []byte {
	return []byte(`{"ref":"` + ref + `","commits":[{"added":["a.md"],"modified":["b.md"],"removed":[]}]}`)
}

func TestHandlerVerifiesAndDurablyQueuesRawDelivery(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)
	body := pushBody("refs/heads/main")

	response := postPush(t, server, body, pushOptions{delivery: "d-queued"})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "queued", decodeStatus(t, response))

	request, ok := producer.request("d-queued")
	require.True(t, ok)
	job := request.GetJob()
	require.Equal(t, jobsv1.JobDirection_JOB_DIRECTION_INBOX, job.GetDirection())
	require.True(t, job.GetScope().GetGlobal())
	require.Equal(t, datasource.GitHubWebhookQueue, job.GetQueue())
	require.Equal(t, datasource.GitHubWebhookTopic, job.GetTopic())
	require.Equal(t, datasource.GitHubWebhookSource, job.GetSource())
	require.EqualValues(t, datasource.GitHubWebhookSchemaVersion, job.GetSchemaVersion())
	require.EqualValues(t, datasource.GitHubWebhookMaxAttempts, job.GetMaxAttempts())
	require.Equal(t, "application/json", job.GetContentType())
	require.Equal(t, "d-queued", job.GetIdempotencyKey())
	// The exact verified bytes are retained, not a re-serialized projection.
	require.Equal(t, body, job.GetPayload())
	require.Equal(t, "push", job.GetAttributes()["github.event"])
	require.Equal(t, "d-queued", job.GetAttributes()["github.delivery"])
	require.Equal(t, testSourceID, job.GetAttributes()["datasource.source_id"])
}

func TestHandlerAcknowledgesExactDuplicateAndRejectsConflictingReuse(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)
	body := pushBody("refs/heads/main")

	first := postPush(t, server, body, pushOptions{delivery: "d-dup"})
	_ = first.Body.Close()

	duplicate := postPush(t, server, body, pushOptions{delivery: "d-dup"})
	defer func() { _ = duplicate.Body.Close() }()
	require.Equal(t, http.StatusOK, duplicate.StatusCode)
	require.Equal(t, "duplicate", decodeStatus(t, duplicate))

	// Same delivery id, different verified body → a semantic conflict the
	// producer must reject rather than overwrite.
	conflict := postPush(t, server, pushBody("refs/heads/other"), pushOptions{delivery: "d-dup"})
	defer func() { _ = conflict.Body.Close() }()
	require.Equal(t, http.StatusConflict, conflict.StatusCode)
	require.EqualValues(t, 1, producer.inserts.Load())
}

func TestHandlerConcurrentDuplicateDeliveryInsertsOnce(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)
	body := pushBody("refs/heads/main")

	const deliveries = 32
	var wait sync.WaitGroup
	statuses := make(chan int, deliveries)
	for range deliveries {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := postPush(t, server, body, pushOptions{delivery: "d-concurrent"})
			statuses <- response.StatusCode
			_ = response.Body.Close()
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, http.StatusOK, status)
	}
	require.EqualValues(t, 1, producer.inserts.Load())
}

func TestHandlerRejectsTamperedSignatureWithoutTouchingJobs(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)

	response := postPush(t, server, pushBody("refs/heads/main"), pushOptions{
		delivery:  "d-tampered",
		signature: "sha256=deadbeef",
	})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerRejectsSignatureFromWrongSecret(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)

	response := postPush(t, server, pushBody("refs/heads/main"), pushOptions{
		delivery: "d-wrong-secret",
		secret:   "whsec_not_the_registered_secret",
	})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerRejectsMissingSignatureHeader(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)

	url := server.URL + datasource.GitHubWebhookPath + testSourceID
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(pushBody("refs/heads/main")))
	require.NoError(t, err)
	req.Header.Set("X-GitHub-Delivery", "d-nosig")
	req.Header.Set("X-GitHub-Event", "push")
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerRejectsUnknownSourceWithoutReadingSecret(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)

	response := postPush(t, server, pushBody("refs/heads/main"), pushOptions{
		sourceID: "unregistered-source",
		delivery: "d-unknown",
	})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerRejectsMissingDeliveryHeaders(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)

	// Verified body but no X-GitHub-Delivery → no idempotency key to dedup on.
	response := postPush(t, server, pushBody("refs/heads/main"), pushOptions{delivery: ""})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)

	oversized := bytes.Repeat([]byte("x"), (960*1024)+1)
	response := postPush(t, server, oversized, pushOptions{delivery: "d-oversized"})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
	require.EqualValues(t, 0, producer.inserts.Load())
}

func TestHandlerPersistenceFailureRequestsRedelivery(t *testing.T) {
	producer := newFakeJobProducer()
	producer.enqueueErr = errors.New("database unavailable")
	server := newTestServer(t, producer)

	response := postPush(t, server, pushBody("refs/heads/main"), pushOptions{delivery: "d-retry"})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
}

func TestHandlerMissingDurableDispositionRequestsRedelivery(t *testing.T) {
	producer := newFakeJobProducer()
	producer.enqueueResponse = &jobsv1.EnqueueJobResponse{}
	server := newTestServer(t, producer)

	response := postPush(t, server, pushBody("refs/heads/main"), pushOptions{delivery: "d-nodisp"})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
}

func TestHandlerRejectsNonPost(t *testing.T) {
	server := newTestServer(t, newFakeJobProducer())
	response, err := http.Get(server.URL + datasource.GitHubWebhookPath + testSourceID)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
}

func TestHandlerRelaysNonPushEventsVerbatim(t *testing.T) {
	// The receiver is event-agnostic: it verifies and persists any signed GitHub
	// event, tagging the event name for the ingest consumer to filter on. GitHub
	// sends a signed "ping" on webhook creation and expects a 2xx.
	producer := newFakeJobProducer()
	server := newTestServer(t, producer)
	body := []byte(`{"zen":"Keep it simple.","hook_id":42}`)

	response := postPush(t, server, body, pushOptions{delivery: "d-ping", event: "ping"})
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusOK, response.StatusCode)
	request, ok := producer.request("d-ping")
	require.True(t, ok)
	require.Equal(t, "ping", request.GetJob().GetAttributes()["github.event"])
}

func TestStaticSecretResolverRejectsEmptyEntries(t *testing.T) {
	_, err := datasource.NewStaticSecretResolver(map[string]string{"": testSecret})
	require.Error(t, err)
	_, err = datasource.NewStaticSecretResolver(map[string]string{testSourceID: "  "})
	require.Error(t, err)

	resolver, err := datasource.NewStaticSecretResolver(map[string]string{testSourceID: testSecret})
	require.NoError(t, err)
	secret, err := resolver.SigningSecret(context.Background(), testSourceID)
	require.NoError(t, err)
	require.Equal(t, testSecret, secret)
	_, err = resolver.SigningSecret(context.Background(), "missing")
	require.ErrorIs(t, err, datasource.ErrSourceNotFound)
}
