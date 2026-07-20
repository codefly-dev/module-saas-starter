package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeWorkerStore struct {
	mu sync.Mutex

	claims       []*jobsv1.ClaimJobsResponse
	heartbeats   []*jobsv1.HeartbeatJobRequest
	completed    []*jobsv1.CompleteJobRequest
	retried      []*jobsv1.RetryJobRequest
	deadLettered []*jobsv1.DeadLetterJobRequest
	retryState   jobsv1.JobState
}

func (s *fakeWorkerStore) Claim(context.Context, *jobsv1.ClaimJobsRequest) (*jobsv1.ClaimJobsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.claims) == 0 {
		return &jobsv1.ClaimJobsResponse{}, nil
	}
	response := s.claims[0]
	s.claims = s.claims[1:]
	return response, nil
}

func (s *fakeWorkerStore) Heartbeat(_ context.Context, request *jobsv1.HeartbeatJobRequest) (*jobsv1.HeartbeatJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats = append(s.heartbeats, request)
	return &jobsv1.HeartbeatJobResponse{Lease: &jobsv1.JobLease{
		Owner: request.GetLease().GetWorkerId(), Token: request.GetLease().GetLeaseToken(),
		ExpiresAt: timestamppb.Now(), HeartbeatAt: timestamppb.Now(),
	}}, nil
}

func (s *fakeWorkerStore) Complete(_ context.Context, request *jobsv1.CompleteJobRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, request)
	return nil
}

func (s *fakeWorkerStore) Retry(_ context.Context, request *jobsv1.RetryJobRequest) (*jobsv1.RetryJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, request)
	state := s.retryState
	if state == jobsv1.JobState_JOB_STATE_UNSPECIFIED {
		state = jobsv1.JobState_JOB_STATE_RETRYING
	}
	return &jobsv1.RetryJobResponse{State: state}, nil
}

func (s *fakeWorkerStore) DeadLetter(_ context.Context, request *jobsv1.DeadLetterJobRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadLettered = append(s.deadLettered, request)
	return nil
}

func claimedJob() *jobsv1.JobEnvelope {
	return &jobsv1.JobEnvelope{
		Id:        "1b1e0ddd-72ec-46a4-b813-30fa5319cf52",
		Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     "email", Topic: "email.send", Source: "test",
		IdempotencyKey: "event-1", SchemaVersion: 1,
		ContentType: "application/json", State: jobsv1.JobState_JOB_STATE_PROCESSING,
		AttemptCount: 1, MaxAttempts: 3,
		Lease: &jobsv1.JobLease{
			Owner: "worker-1", Token: "a1491a52-345f-469f-aa1b-c894c2c88f05", //gitleaks:allow -- deterministic non-secret fixture
			ExpiresAt: timestamppb.Now(), HeartbeatAt: timestamppb.Now(),
		},
		AvailableAt: timestamppb.Now(), CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
	}
}

func newTestWorker(t *testing.T, store Store, handler Handler) *Worker {
	t.Helper()
	worker, err := NewWorker(WorkerConfig{
		Store: store, Queue: "email", Handler: handler, WorkerID: "worker-1",
		PollInterval: time.Hour, LeaseDuration: time.Second,
		HeartbeatInterval: 5 * time.Millisecond, BatchSize: 1,
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		RetryDelay: func(uint32) time.Duration { return 30 * time.Second },
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	return worker
}

func TestWorkerCompletesSuccessfulJob(t *testing.T) {
	store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
	worker := newTestWorker(t, store, func(context.Context, *jobsv1.JobEnvelope) error { return nil })

	count, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if count != 1 || len(store.completed) != 1 {
		t.Fatalf("count/completed = %d/%d, want 1/1", count, len(store.completed))
	}
	metrics := worker.Metrics()
	if metrics.GetIterations() != 1 || metrics.GetClaimed() != 1 || metrics.GetSucceeded() != 1 || metrics.GetActive() != 0 {
		t.Fatalf("unexpected metrics: %v", metrics)
	}
}

func TestWorkerPersistsOnlyTypedSafeFailures(t *testing.T) {
	tests := []struct {
		name      string
		handler   Handler
		wantRetry bool
		wantCode  string
	}{
		{
			name: "retryable typed",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				return NewProcessingError("email.rate_limited", "provider asked for backoff", true)
			},
			wantRetry: true,
			wantCode:  "email.rate_limited",
		},
		{
			name: "permanent typed",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				return NewProcessingError("email.invalid_recipient", "recipient rejected", false)
			},
			wantCode: "email.invalid_recipient",
		},
		{
			name: "untyped is redacted",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				return errors.New("token=must-never-be-persisted")
			},
			wantRetry: true,
			wantCode:  "jobs.handler_failed",
		},
		{
			name: "panic is redacted",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				panic("secret payload")
			},
			wantRetry: true,
			wantCode:  "jobs.handler_panic",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
			worker := newTestWorker(t, store, test.handler)
			if _, err := worker.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}
			var failure *jobsv1.JobFailure
			if test.wantRetry {
				if len(store.retried) != 1 {
					t.Fatalf("retry count = %d, want 1", len(store.retried))
				}
				failure = store.retried[0].GetFailure()
			} else {
				if len(store.deadLettered) != 1 {
					t.Fatalf("dead-letter count = %d, want 1", len(store.deadLettered))
				}
				failure = store.deadLettered[0].GetFailure()
			}
			if failure.GetCode() != test.wantCode {
				t.Fatalf("failure code = %q, want %q", failure.GetCode(), test.wantCode)
			}
			if test.name == "untyped is redacted" && failure.GetMessage() != "job handler failed" {
				t.Fatalf("untyped failure leaked: %q", failure.GetMessage())
			}
		})
	}
}

func TestWorkerHeartbeatsLongRunningHandler(t *testing.T) {
	store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
	worker := newTestWorker(t, store, func(context.Context, *jobsv1.JobEnvelope) error {
		time.Sleep(18 * time.Millisecond)
		return nil
	})
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	store.mu.Lock()
	heartbeats := len(store.heartbeats)
	store.mu.Unlock()
	if heartbeats < 2 {
		t.Fatalf("heartbeat count = %d, want at least 2", heartbeats)
	}
}

func TestWorkerShutdownCancelsAfterGraceDeadline(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
	worker := newTestWorker(t, store, func(ctx context.Context, _ *jobsv1.JobEnvelope) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	worker.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := worker.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("handler context was not canceled after shutdown deadline")
	}
}
