package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/core/wool"
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
	heartbeatErr error
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
	if s.heartbeatErr != nil {
		return nil, s.heartbeatErr
	}
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

// recordingLogs captures what the worker writes to the log sink.
// TestWorkerPersistsOnlyTypedSafeFailures inspects only the store, so it stayed
// green while an unredacted handler cause reached the sink; these assertions
// close that blind spot.
type recordingLogs struct {
	mu   sync.Mutex
	logs []*wool.Log
}

func (r *recordingLogs) Process(log *wool.Log) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
}

func (r *recordingLogs) withMessage(message string) []*wool.Log {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []*wool.Log
	for _, log := range r.logs {
		if log.Message == message {
			found = append(found, log)
		}
	}
	return found
}

// rendered flattens every captured line the way a log shipper would store it,
// so a leak assertion cannot be fooled by which field carried the text.
func (r *recordingLogs) rendered() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out strings.Builder
	for _, log := range r.logs {
		fmt.Fprintf(&out, "%s %s", log.Header, log.Message)
		for _, field := range log.Fields {
			if field == nil {
				continue
			}
			fmt.Fprintf(&out, " %s=%v", field.Key, field.Value)
		}
		out.WriteString("\n")
	}
	return out.String()
}

func captureLogs(t *testing.T) *recordingLogs {
	t.Helper()
	sink := &recordingLogs{}
	wool.SetFallbackLogger(sink)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	return sink
}

func logField(t *testing.T, log *wool.Log, key string) any {
	t.Helper()
	for _, field := range log.Fields {
		if field != nil && field.Key == key {
			return field.Value
		}
	}
	t.Fatalf("log line has no %q field", key)
	return nil
}

func newTestWorker(t *testing.T, store Store, handler Handler) *Worker {
	t.Helper()
	return newTestWorkerLoggingCause(t, store, handler, false)
}

func newTestWorkerLoggingCause(t *testing.T, store Store, handler Handler, logCause bool) *Worker {
	t.Helper()
	worker, err := NewWorker(WorkerConfig{
		Store: store, Queue: "email", Handler: handler, WorkerID: "worker-1",
		PollInterval: time.Hour, LeaseDuration: time.Second,
		HeartbeatInterval: 5 * time.Millisecond, BatchSize: 1,
		Now:                   func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		RetryDelay:            func(uint32) time.Duration { return 30 * time.Second },
		UnsafeLogHandlerCause: logCause,
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

// The cause of a handler failure is arbitrary handler data: an untyped error
// routinely quotes what the handler was given, and a panic value is whatever the
// handler panicked with. Neither may reach the log sink unless an operator has
// explicitly accepted that, so the default-off path must report the bounded
// classification and nothing else.
func TestWorkerFailureLogOmitsHandlerCauseByDefault(t *testing.T) {
	const unsafeCause = "token=must-never-be-persisted"
	tests := []struct {
		name          string
		handler       Handler
		wantCode      string
		wantRetryable bool
		wantPanicked  bool
	}{
		{
			name: "untyped error",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				return errors.New(unsafeCause)
			},
			wantCode: "jobs.handler_failed", wantRetryable: true,
		},
		{
			name: "panic",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				panic(unsafeCause)
			},
			wantCode: "jobs.handler_panic", wantRetryable: true, wantPanicked: true,
		},
		{
			name: "typed permanent",
			handler: func(context.Context, *jobsv1.JobEnvelope) error {
				return NewProcessingError("email.invalid_recipient", "recipient rejected", false)
			},
			wantCode: "email.invalid_recipient",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := captureLogs(t)
			store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
			worker := newTestWorker(t, store, test.handler)
			if _, err := worker.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}

			logs := sink.withMessage("job handler failed")
			if len(logs) != 1 {
				t.Fatalf("failure log count = %d, want 1:\n%s", len(logs), sink.rendered())
			}
			want := map[string]any{
				"queue":        "email",
				"topic":        "email.send",
				"job_id":       claimedJob().GetId(),
				"attempt":      uint32(1),
				"failure_code": test.wantCode,
				"retryable":    test.wantRetryable,
				"panicked":     test.wantPanicked,
			}
			for key, wantValue := range want {
				if got := logField(t, logs[0], key); got != wantValue {
					t.Fatalf("%s = %v, want %v", key, got, wantValue)
				}
			}
			if strings.Contains(sink.rendered(), unsafeCause) {
				t.Fatalf("handler cause leaked to the log sink:\n%s", sink.rendered())
			}
		})
	}
}

// With the opt-in set, the cause must actually be there — including a panic's
// value and stack, which the recover path used to discard, leaving the report
// with nothing to say about the one failure an operator most needs to diagnose.
// Durable history stays generic either way.
func TestWorkerFailureLogCarriesCauseWhenExplicitlyEnabled(t *testing.T) {
	t.Run("untyped error", func(t *testing.T) {
		sink := captureLogs(t)
		store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
		worker := newTestWorkerLoggingCause(t, store, func(context.Context, *jobsv1.JobEnvelope) error {
			return errors.New("upstream rejected the request")
		}, true)
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}

		logs := sink.withMessage("job handler failed")
		if len(logs) != 1 {
			t.Fatalf("failure log count = %d, want 1", len(logs))
		}
		if got := logField(t, logs[0], "error"); got != "upstream rejected the request" {
			t.Fatalf("error = %v, want the handler cause", got)
		}
		if code := store.retried[0].GetFailure().GetCode(); code != "jobs.handler_failed" {
			t.Fatalf("durable failure code = %q, want the generic code", code)
		}
		if message := store.retried[0].GetFailure().GetMessage(); message != "job handler failed" {
			t.Fatalf("durable failure message = %q, want the generic message", message)
		}
	})

	t.Run("panic keeps its value and stack", func(t *testing.T) {
		sink := captureLogs(t)
		store := &fakeWorkerStore{claims: []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}}}
		worker := newTestWorkerLoggingCause(t, store, func(context.Context, *jobsv1.JobEnvelope) error {
			panic("handler exploded")
		}, true)
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}

		logs := sink.withMessage("job handler failed")
		if len(logs) != 1 {
			t.Fatalf("failure log count = %d, want 1", len(logs))
		}
		if got := logField(t, logs[0], "panic_value"); got != "handler exploded" {
			t.Fatalf("panic_value = %v, want the recovered value", got)
		}
		stack, ok := logField(t, logs[0], "panic_stack").(string)
		if !ok || stack == "" {
			t.Fatal("panic_stack is empty, so the panic site is still unrecoverable")
		}
		if !strings.Contains(stack, "panic") {
			t.Fatalf("panic_stack does not show the panic site:\n%s", stack)
		}
		if code := store.retried[0].GetFailure().GetCode(); code != "jobs.handler_panic" {
			t.Fatalf("durable failure code = %q, want the generic panic code", code)
		}
		if strings.Contains(store.retried[0].GetFailure().GetMessage(), "handler exploded") {
			t.Fatal("panic value reached durable history")
		}
	})
}

// A lease the worker lost cancels the handler itself, so the handler error is
// that cancellation and not a failure. Reporting it as one blamed the handler
// for a worker-side event and logged it twice, once here and once as the Run
// loop's iteration failure.
func TestWorkerDoesNotBlameHandlerWhenLeaseIsLost(t *testing.T) {
	sink := captureLogs(t)
	store := &fakeWorkerStore{
		claims:       []*jobsv1.ClaimJobsResponse{{Jobs: []*jobsv1.JobEnvelope{claimedJob()}}},
		heartbeatErr: ErrLeaseLost,
	}
	worker := newTestWorker(t, store, func(ctx context.Context, _ *jobsv1.JobEnvelope) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := worker.RunOnce(context.Background()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RunOnce() error = %v, want lease lost", err)
	}

	if logs := sink.withMessage("job handler failed"); len(logs) != 0 {
		t.Fatalf("lease loss reported as a handler failure:\n%s", sink.rendered())
	}
	if len(store.retried) != 0 || len(store.deadLettered) != 0 {
		t.Fatalf("lease loss wrote failure history: retried=%d dead=%d",
			len(store.retried), len(store.deadLettered))
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
	sink := captureLogs(t)
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

	// Shutdown recovers these jobs through the lease-expiry path; the handler did
	// not fail, so reporting one warning per in-flight job on every rollout is
	// noise that buries the real failures this log exists to surface.
	if logs := sink.withMessage("job handler failed"); len(logs) != 0 {
		t.Fatalf("shutdown reported as a handler failure:\n%s", sink.rendered())
	}
}
