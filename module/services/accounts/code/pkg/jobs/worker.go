package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler processes one claimed envelope. Implementations must honor context
// cancellation and make downstream side effects idempotent. The envelope must
// never be logged wholesale because it contains payload bytes and attributes.
type Handler func(context.Context, *jobsv1.JobEnvelope) error

// ProcessingError is the only handler error whose bounded, operator-safe code
// and message may enter durable job history. Arbitrary errors and panics are
// replaced by generic diagnostics to avoid persisting credentials or payloads.
type ProcessingError struct {
	Failure   *jobsv1.JobFailure
	Retryable bool
}

func (e *ProcessingError) Error() string {
	if e == nil || e.Failure == nil {
		return "job processing failed"
	}
	return e.Failure.Code
}

// NewProcessingError validates an explicitly safe durable failure.
func NewProcessingError(code, message string, retryable bool) error {
	failure := &jobsv1.JobFailure{Code: code, Message: message}
	if err := ValidateCommand(failure); err != nil {
		return fmt.Errorf("%w: invalid processing failure: %v", ErrInvalidCommand, err)
	}
	return &ProcessingError{Failure: failure, Retryable: retryable}
}

type WorkerConfig struct {
	Store             Store
	Queue             string
	Handler           Handler
	WorkerID          string
	PollInterval      time.Duration
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	BatchSize         uint32
	Now               func() time.Time
	RetryDelay        func(attempt uint32) time.Duration
}

type workerMetrics struct {
	iterations    atomic.Uint64
	claimErrors   atomic.Uint64
	claimed       atomic.Uint64
	succeeded     atomic.Uint64
	retried       atomic.Uint64
	deadLettered  atomic.Uint64
	handlerPanics atomic.Uint64
	leaseLost     atomic.Uint64
	active        atomic.Int64
}

// Worker is the reusable, product-neutral polling runtime. One worker owns one
// queue, keeping queue as its only metric label.
type Worker struct {
	config  WorkerConfig
	metrics workerMetrics

	mu           sync.Mutex
	started      bool
	cancelClaims context.CancelFunc
	cancelAll    context.CancelFunc
	done         chan struct{}
}

func NewWorker(config WorkerConfig) (*Worker, error) {
	if config.Store == nil {
		return nil, errors.New("jobs: worker store is required")
	}
	if config.Handler == nil {
		return nil, errors.New("jobs: worker handler is required")
	}
	if config.Queue == "" {
		return nil, errors.New("jobs: worker queue is required")
	}
	if config.WorkerID == "" {
		host, _ := os.Hostname()
		config.WorkerID = fmt.Sprintf("%s-%s", host, uuid.NewString())
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = time.Minute
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = config.LeaseDuration / 3
	}
	if config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.LeaseDuration {
		return nil, errors.New("jobs: heartbeat interval must be positive and shorter than the lease")
	}
	if config.BatchSize == 0 {
		config.BatchSize = 16
	}
	if config.BatchSize > 100 {
		return nil, errors.New("jobs: batch size must not exceed 100")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RetryDelay == nil {
		config.RetryDelay = defaultRetryDelay
	}
	claim := &jobsv1.ClaimJobsRequest{
		Queue: config.Queue, WorkerId: config.WorkerID, Limit: config.BatchSize,
		LeaseDuration: durationpb.New(config.LeaseDuration),
	}
	if err := ValidateCommand(claim); err != nil {
		return nil, err
	}
	return &Worker{config: config}, nil
}

// Start begins polling once. Repeated calls are harmless.
func (w *Worker) Start(parent context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	processCtx, cancelAll := context.WithCancel(parent)
	claimCtx, cancelClaims := context.WithCancel(processCtx)
	w.cancelAll = cancelAll
	w.cancelClaims = cancelClaims
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go func() {
		defer close(done)
		run := func() {
			if _, err := w.runOnce(claimCtx, processCtx); err != nil &&
				!errors.Is(err, context.Canceled) {
				wool.Get(processCtx).In("jobs.worker").Warn(
					"iteration failed",
					wool.Field("queue", w.config.Queue),
					wool.ErrField(err),
				)
			}
		}
		run()
		ticker := time.NewTicker(w.config.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-claimCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

// Shutdown stops new claims and lets already-claimed handlers finish. When the
// caller's deadline expires, processing contexts are canceled and remaining
// jobs are recovered later by the database lease-expiry path.
func (w *Worker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	cancelClaims, cancelAll, done := w.cancelClaims, w.cancelAll, w.done
	w.mu.Unlock()

	cancelClaims()
	select {
	case <-done:
		cancelAll()
		return nil
	case <-ctx.Done():
		cancelAll()
		select {
		case <-done:
		default:
		}
		return ctx.Err()
	}
}

// RunOnce claims and drains one bounded batch. It is useful for deterministic
// tests and one-shot execution environments.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	return w.runOnce(ctx, ctx)
}

func (w *Worker) runOnce(claimCtx, processCtx context.Context) (int, error) {
	w.metrics.iterations.Add(1)
	trace, end := wool.StartSpan(claimCtx, "jobs.worker.poll")
	defer end()
	claim, err := w.config.Store.Claim(trace.Context(), &jobsv1.ClaimJobsRequest{
		Queue: w.config.Queue, WorkerId: w.config.WorkerID, Limit: w.config.BatchSize,
		LeaseDuration: durationpb.New(w.config.LeaseDuration),
	})
	if err != nil {
		w.metrics.claimErrors.Add(1)
		return 0, err
	}
	w.metrics.claimed.Add(uint64(len(claim.GetJobs())))

	var wait sync.WaitGroup
	errorsByJob := make(chan error, len(claim.GetJobs()))
	for _, envelope := range claim.GetJobs() {
		envelope := envelope
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := w.process(processCtx, envelope); err != nil {
				errorsByJob <- err
			}
		}()
	}
	wait.Wait()
	close(errorsByJob)
	var combined error
	for err := range errorsByJob {
		combined = errors.Join(combined, err)
	}
	return len(claim.GetJobs()), combined
}

func (w *Worker) process(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
	if envelope == nil || envelope.GetLease() == nil {
		return errors.New("jobs: claimed envelope has no lease")
	}
	w.metrics.active.Add(1)
	defer w.metrics.active.Add(-1)
	trace, end := wool.StartSpan(ctx, "jobs.worker.process")
	defer end()
	trace.Debug("processing job",
		wool.Field("queue", envelope.GetQueue()),
		wool.Field("topic", envelope.GetTopic()),
		wool.Field("job_id", envelope.GetId()),
	)

	lease := &jobsv1.JobLeaseReference{
		JobId: envelope.GetId(), WorkerId: envelope.GetLease().GetOwner(),
		LeaseToken: envelope.GetLease().GetToken(),
	}
	handlerCtx, cancelHandler := context.WithCancel(trace.Context())
	heartbeatCtx, cancelHeartbeat := context.WithCancel(trace.Context())
	heartbeatDone := make(chan error, 1)
	go w.heartbeat(heartbeatCtx, cancelHandler, lease, heartbeatDone)

	handlerErr, panicked := w.invokeHandler(handlerCtx, envelope)
	cancelHeartbeat()
	heartbeatErr := <-heartbeatDone
	cancelHandler()
	if panicked {
		w.metrics.handlerPanics.Add(1)
	}
	if heartbeatErr != nil {
		if errors.Is(heartbeatErr, ErrLeaseLost) {
			w.metrics.leaseLost.Add(1)
		}
		return heartbeatErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if handlerErr == nil {
		if err := w.config.Store.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: lease}); err != nil {
			return w.finalizationError(err)
		}
		w.metrics.succeeded.Add(1)
		return nil
	}

	failure := &jobsv1.JobFailure{Code: "jobs.handler_failed", Message: "job handler failed"}
	retryable := true
	if panicked {
		failure = &jobsv1.JobFailure{Code: "jobs.handler_panic", Message: "job handler panicked"}
	}
	var processingErr *ProcessingError
	if errors.As(handlerErr, &processingErr) && processingErr.Failure != nil &&
		ValidateCommand(processingErr.Failure) == nil {
		failure = processingErr.Failure
		retryable = processingErr.Retryable
	}
	if !retryable {
		if err := w.config.Store.DeadLetter(ctx, &jobsv1.DeadLetterJobRequest{
			Lease: lease, Failure: failure,
		}); err != nil {
			return w.finalizationError(err)
		}
		w.metrics.deadLettered.Add(1)
		return nil
	}

	response, err := w.config.Store.Retry(ctx, &jobsv1.RetryJobRequest{
		Lease:   lease,
		Failure: failure,
		RetryAt: timestamppb.New(w.config.Now().UTC().Add(
			w.config.RetryDelay(envelope.GetAttemptCount()),
		)),
	})
	if err != nil {
		return w.finalizationError(err)
	}
	if response.GetState() == jobsv1.JobState_JOB_STATE_DEAD_LETTER {
		w.metrics.deadLettered.Add(1)
	} else {
		w.metrics.retried.Add(1)
	}
	return nil
}

func (w *Worker) invokeHandler(ctx context.Context, envelope *jobsv1.JobEnvelope) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			err = errors.New("jobs: handler panic")
			panicked = true
		}
	}()
	return w.config.Handler(ctx, envelope), false
}

func (w *Worker) heartbeat(
	ctx context.Context,
	cancelHandler context.CancelFunc,
	lease *jobsv1.JobLeaseReference,
	done chan<- error,
) {
	ticker := time.NewTicker(w.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			_, err := w.config.Store.Heartbeat(ctx, &jobsv1.HeartbeatJobRequest{
				Lease: lease, Extension: durationpb.New(w.config.LeaseDuration),
			})
			if err != nil {
				cancelHandler()
				done <- err
				return
			}
		}
	}
}

func (w *Worker) finalizationError(err error) error {
	if errors.Is(err, ErrLeaseLost) {
		w.metrics.leaseLost.Add(1)
	}
	return err
}

func (w *Worker) Metrics() *jobsv1.JobWorkerMetrics {
	return &jobsv1.JobWorkerMetrics{
		Queue:         w.config.Queue,
		Iterations:    w.metrics.iterations.Load(),
		ClaimErrors:   w.metrics.claimErrors.Load(),
		Claimed:       w.metrics.claimed.Load(),
		Succeeded:     w.metrics.succeeded.Load(),
		Retried:       w.metrics.retried.Load(),
		DeadLettered:  w.metrics.deadLettered.Load(),
		HandlerPanics: w.metrics.handlerPanics.Load(),
		LeaseLost:     w.metrics.leaseLost.Load(),
		Active:        w.metrics.active.Load(),
	}
}

func defaultRetryDelay(attempt uint32) time.Duration {
	if attempt == 0 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 8 {
		shift = 8
	}
	return 5 * time.Second * time.Duration(1<<shift)
}
