package business

import (
	"context"
	"errors"
	"sync"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"google.golang.org/protobuf/proto"
)

const teeOrgID = "00000000-0000-0000-0000-000000000001"

// teeStore is a fake Store honoring transaction semantics: work recorded inside
// WithOrgTx is discarded when the function returns an error, so a failed enqueue
// rolls the audit row back the way a real transaction would.
type teeStore struct {
	Store
	mu                sync.Mutex
	audits            []AuditEntry
	jobs              []*jobsv1.EnqueueJobRequest
	subs              []*WebhookSubscription
	failExportEnqueue bool
}

func (s *teeStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	s.mu.Lock()
	auditMark, jobMark := len(s.audits), len(s.jobs)
	s.mu.Unlock()
	if err := fn(ctx); err != nil {
		s.mu.Lock()
		s.audits = s.audits[:auditMark]
		s.jobs = s.jobs[:jobMark]
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *teeStore) InsertAuditEvent(_ context.Context, entry AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, entry)
	return nil
}

func (s *teeStore) GetActiveWebhookSubscriptions(_ context.Context, _ string) ([]*WebhookSubscription, error) {
	return s.subs, nil
}

func (s *teeStore) CreateWebhookDelivery(_ context.Context, _ *WebhookDelivery) error { return nil }

func (s *teeStore) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	if s.failExportEnqueue && request.GetJob().GetQueue() == AuditExportQueue {
		return nil, errors.New("enqueue exploded")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
	return &jobsv1.EnqueueJobResponse{
		JobId:       NewIDString(),
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func (s *teeStore) jobsForQueue(queue string) []*jobsv1.EnqueueJobRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*jobsv1.EnqueueJobRequest
	for _, job := range s.jobs {
		if job.GetJob().GetQueue() == queue {
			out = append(out, job)
		}
	}
	return out
}

func orgAuditEntry() AuditEntry {
	return AuditEntry{
		ID:        NewIDString(),
		OrgID:     teeOrgID,
		ActorID:   NewIDString(),
		ActorType: "user",
		EventType: EventType("test.event.performed"),
	}
}

func TestSelectorPostgresDoesNotTee(t *testing.T) {
	store := &teeStore{subs: []*WebhookSubscription{{ID: "00000000-0000-0000-0000-000000000002"}}}
	emitter, err := NewDurableAuditEmitter(store, store)
	if err != nil {
		t.Fatalf("NewDurableAuditEmitter: %v", err)
	}
	emitter.Emit(t.Context(), orgAuditEntry())

	if got := len(store.audits); got != 1 {
		t.Fatalf("audits committed = %d, want 1", got)
	}
	if got := len(store.jobsForQueue(AuditExportQueue)); got != 0 {
		t.Fatalf("audit export jobs = %d, want 0 (tee disabled)", got)
	}
	if got := len(store.jobsForQueue(OutboundWebhookQueue)); got != 1 {
		t.Fatalf("webhook jobs = %d, want 1", got)
	}
}

func TestSelectorBothTeesAndCommitsAtomically(t *testing.T) {
	store := &teeStore{subs: []*WebhookSubscription{{ID: "00000000-0000-0000-0000-000000000002"}}}
	emitter, err := NewDurableAuditEmitter(store, store, WithExternalTee())
	if err != nil {
		t.Fatalf("NewDurableAuditEmitter: %v", err)
	}
	entry := orgAuditEntry()
	emitter.Emit(t.Context(), entry)

	if got := len(store.audits); got != 1 {
		t.Fatalf("audits committed = %d, want 1", got)
	}
	if got := len(store.jobsForQueue(OutboundWebhookQueue)); got != 1 {
		t.Fatalf("webhook jobs = %d, want 1", got)
	}
	exports := store.jobsForQueue(AuditExportQueue)
	if len(exports) != 1 {
		t.Fatalf("audit export jobs = %d, want 1", len(exports))
	}
	decoded, err := decodeAuditExportEnvelope(envelopeFromRequest(t, exports[0]))
	if err != nil {
		t.Fatalf("decode export envelope: %v", err)
	}
	if decoded.ID != entry.ID || decoded.OrgID != entry.OrgID {
		t.Fatalf("export entry = %+v, want ID %s org %s", decoded, entry.ID, entry.OrgID)
	}
}

func TestExportEnqueueFailureRollsBackAuditRow(t *testing.T) {
	store := &teeStore{failExportEnqueue: true}
	emitter, err := NewDurableAuditEmitter(store, store, WithExternalTee())
	if err != nil {
		t.Fatalf("NewDurableAuditEmitter: %v", err)
	}
	emitter.Emit(t.Context(), orgAuditEntry())

	// The export job is enqueued inside the audit transaction, so a failed
	// enqueue must abort the whole write — no compliance record without its tee.
	if got := len(store.audits); got != 0 {
		t.Fatalf("audits committed = %d, want 0 (tx rolled back)", got)
	}
}

// flakySink fails its first failures calls, then records each delivered ID once.
type flakySink struct {
	mu        sync.Mutex
	failures  int
	attempts  int
	delivered map[string]int
}

func (s *flakySink) Emit(_ context.Context, entry AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.attempts <= s.failures {
		return errors.New("warehouse unavailable")
	}
	if s.delivered == nil {
		s.delivered = map[string]int{}
	}
	s.delivered[entry.ID]++
	return nil
}

func TestExportHandlerAtLeastOnceDrain(t *testing.T) {
	store := &teeStore{}
	if err := enqueueAuditExport(t.Context(), store, orgAuditEntry()); err != nil {
		t.Fatalf("enqueueAuditExport: %v", err)
	}
	envelope := envelopeFromRequest(t, store.jobs[0])

	sink := &flakySink{failures: 2}
	handler, err := NewAuditExportJobHandler(sink)
	if err != nil {
		t.Fatalf("NewAuditExportJobHandler: %v", err)
	}

	// Model the worker retry loop: the handler returns an error while the sink is
	// down, then succeeds once it recovers — at-least-once delivery.
	var lastErr error
	for i := 0; i < 5; i++ {
		if lastErr = handler(t.Context(), envelope); lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		t.Fatalf("handler never drained: %v", lastErr)
	}
	if sink.attempts != 3 {
		t.Fatalf("sink attempts = %d, want 3 (2 failures + 1 success)", sink.attempts)
	}

	// A redelivery after success must dedupe on ID.
	if err := handler(t.Context(), envelope); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	id := store.jobs[0].GetJob().GetIdempotencyKey()
	if sink.delivered[id] != 2 {
		t.Fatalf("delivered count = %d, want 2 observed deliveries of a single id", sink.delivered[id])
	}
}

func TestExportRedactsPayload(t *testing.T) {
	store := &teeStore{}
	entry := orgAuditEntry()
	entry.Payload = map[string]any{"secret": "value"}
	if err := enqueueAuditExport(t.Context(), store, entry); err != nil {
		t.Fatalf("enqueueAuditExport: %v", err)
	}
	decoded, err := decodeAuditExportEnvelope(envelopeFromRequest(t, store.jobs[0]))
	if err != nil {
		t.Fatalf("decode export envelope: %v", err)
	}
	// An unregistered event type is redacted whole (fail closed), so nothing
	// leaves the audit store even when the schema is unknown.
	if len(decoded.Payload) != 0 {
		t.Fatalf("teed payload = %v, want redacted empty", decoded.Payload)
	}
}

func TestExportHandlerRejectsMalformedJob(t *testing.T) {
	handler, err := NewAuditExportJobHandler(&flakySink{})
	if err != nil {
		t.Fatalf("NewAuditExportJobHandler: %v", err)
	}
	envelope := &jobsv1.JobEnvelope{
		Id:             NewIDString(),
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: teeOrgID}},
		Queue:          AuditExportQueue,
		Topic:          "audit.event.wrong",
		Source:         AuditExportSource,
		IdempotencyKey: NewIDString(),
		SchemaVersion:  AuditExportSchemaVersion,
		Payload:        []byte("{}"),
		ContentType:    AuditExportContentType,
		State:          jobsv1.JobState_JOB_STATE_PENDING,
		MaxAttempts:    AuditExportMaxAttempts,
	}
	err = handler(t.Context(), envelope)
	var processing *jobs.ProcessingError
	if !errors.As(err, &processing) {
		t.Fatalf("error = %v, want ProcessingError", err)
	}
	if processing.Retryable {
		t.Fatal("malformed job must be non-retryable")
	}
}

// envelopeFromRequest projects a producer enqueue request into the leased
// envelope a worker would hand the handler.
func envelopeFromRequest(t *testing.T, request *jobsv1.EnqueueJobRequest) *jobsv1.JobEnvelope {
	t.Helper()
	job := request.GetJob()
	return &jobsv1.JobEnvelope{
		Id:             NewIDString(),
		Direction:      job.GetDirection(),
		Scope:          job.GetScope(),
		Queue:          job.GetQueue(),
		Topic:          job.GetTopic(),
		Source:         job.GetSource(),
		IdempotencyKey: job.GetIdempotencyKey(),
		SchemaVersion:  job.GetSchemaVersion(),
		Payload:        job.GetPayload(),
		ContentType:    job.GetContentType(),
		State:          jobsv1.JobState_JOB_STATE_PENDING,
		MaxAttempts:    job.GetMaxAttempts(),
	}
}
