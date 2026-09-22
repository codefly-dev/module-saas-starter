package infra

import (
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// A producer that declares a content type but no payload passes saas.jobs.v1
// validation — payload is bounded only from above — while job_messages.payload
// is NOT NULL and pgx encodes a nil slice as SQL NULL. prepareJobEnqueue must
// therefore hand the enqueue function an empty bytea, never NULL: on the
// PostgresStore path the enqueue runs inside the caller's transaction, so that
// constraint violation would roll back the business mutation with it.
func TestPrepareJobEnqueue_NilPayloadIsEmptyBytesNotNull(t *testing.T) {
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:          "datasource.deliveries",
		Topic:          "datasource.github.reconcile",
		Source:         "github.reconcile",
		IdempotencyKey: "key-1",
		SchemaVersion:  1,
		ContentType:    "application/json",
		MaxAttempts:    5,
	}}

	prepared, err := prepareJobEnqueue(request)
	if err != nil {
		t.Fatalf("a payload-less job is a valid command; prepare must not fail: %v", err)
	}
	if prepared.payload == nil {
		t.Fatal("nil payload reaches enqueue_job_message as SQL NULL and violates job_messages.payload NOT NULL")
	}
	if len(prepared.payload) != 0 {
		t.Fatalf("payload = %q, want the empty bytea", prepared.payload)
	}
}

// A payload the producer did supply must reach the store byte-for-byte: the
// normalisation above must not touch it.
func TestPrepareJobEnqueue_SuppliedPayloadIsUnchanged(t *testing.T) {
	body := []byte(`{"a":1}`)
	prepared, err := prepareJobEnqueue(&jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:          "datasource.deliveries",
		Topic:          "datasource.github.reconcile",
		Source:         "github.reconcile",
		IdempotencyKey: "key-2",
		SchemaVersion:  1,
		Payload:        body,
		ContentType:    "application/json",
		MaxAttempts:    5,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if string(prepared.payload) != string(body) {
		t.Fatalf("payload = %q, want %q", prepared.payload, body)
	}
}
