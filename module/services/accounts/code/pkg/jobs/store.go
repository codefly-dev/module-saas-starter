package jobs

import (
	"context"
	"errors"
	"fmt"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
)

var (
	// ErrInvalidCommand identifies a generated worker command that failed its
	// protobuf validation rules. Callers may safely treat it as non-retryable.
	ErrInvalidCommand = errors.New("jobs: invalid command")
	// ErrIdempotencyConflict means a producer reused an idempotency key for a
	// semantically different generated enqueue command.
	ErrIdempotencyConflict = errors.New("jobs: idempotency key reused with a different request")
	// ErrOrderingKeyTooLong means the collision-free canonical encoding does
	// not fit the durable 255-byte ordering-key bound.
	ErrOrderingKeyTooLong = errors.New("jobs: canonical ordering key exceeds 255 bytes")
	// ErrTransactionRequired prevents request handlers from accidentally
	// publishing an outbox message outside their business mutation transaction.
	ErrTransactionRequired = errors.New("jobs: producer transaction required")
	// ErrLeaseLost is returned when a worker no longer owns the current,
	// unexpired fencing token. A stale worker must stop without side effects.
	ErrLeaseLost = errors.New("jobs: lease lost")
	// ErrJobNotFound identifies an operations lookup whose durable job does not
	// exist. Payload data is never included in the returned error.
	ErrJobNotFound = errors.New("jobs: job not found")
	// ErrReplayNotAllowed protects terminal history: only dead-lettered work may
	// be copied into a new pending job.
	ErrReplayNotAllowed = errors.New("jobs: only dead-lettered jobs may be replayed")
	// ErrInvalidPageToken identifies an untrusted operations cursor that cannot
	// be decoded or validated.
	ErrInvalidPageToken = errors.New("jobs: invalid page token")
)

var commandValidator = func() protovalidate.Validator {
	validator, err := protovalidate.New()
	if err != nil {
		panic(fmt.Errorf("jobs: create protobuf validator: %w", err))
	}
	return validator
}()

// ValidateCommand applies the rules generated from saas.jobs.v1. Keeping this
// at producer and worker boundaries makes protobuf the single command contract
// for every product that embeds the starter.
func ValidateCommand(command proto.Message) error {
	if command == nil || !command.ProtoReflect().IsValid() {
		return fmt.Errorf("%w: command is required", ErrInvalidCommand)
	}
	if err := commandValidator.Validate(command); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	return nil
}

// Store is the product-neutral durable worker boundary. It intentionally has
// no transport, workload, or application dependency; every adopter drives it
// with the same generated commands.
type Store interface {
	Claim(context.Context, *jobsv1.ClaimJobsRequest) (*jobsv1.ClaimJobsResponse, error)
	Heartbeat(context.Context, *jobsv1.HeartbeatJobRequest) (*jobsv1.HeartbeatJobResponse, error)
	Complete(context.Context, *jobsv1.CompleteJobRequest) error
	Retry(context.Context, *jobsv1.RetryJobRequest) (*jobsv1.RetryJobResponse, error)
	DeadLetter(context.Context, *jobsv1.DeadLetterJobRequest) error
}

// Producer appends immutable work and resolves exact idempotent retries.
type Producer interface {
	EnqueueJob(context.Context, *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error)
}

// Operations is the payload-free cross-tenant administration boundary. Its
// implementation must use the isolated app_job_worker database role; request
// traffic must never acquire global access to job payloads.
type Operations interface {
	GetJobOperations(context.Context, *jobsv1.GetJobOperationsRequest) (*jobsv1.GetJobOperationsResponse, error)
	ListJobs(context.Context, *jobsv1.ListJobsRequest) (*jobsv1.ListJobsResponse, error)
	GetJob(context.Context, *jobsv1.GetJobRequest) (*jobsv1.GetJobResponse, error)
	ReplayJob(context.Context, *jobsv1.ReplayJobRequest) (*jobsv1.ReplayJobResponse, error)
}
