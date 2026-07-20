package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type preparedJobEnqueue struct {
	direction          string
	scopeKind          string
	organizationID     *uuid.UUID
	subjectID          *uuid.UUID
	queue              string
	topic              string
	source             string
	idempotencyKey     string
	orderingKey        *string
	schemaVersion      int
	payload            []byte
	contentType        string
	attributes         []byte
	priority           int16
	maxAttempts        int
	availableAt        *time.Time
	requestFingerprint [32]byte
}

var (
	_ jobs.Producer = (*PostgresStore)(nil)
	_ jobs.Producer = (*PostgresJobStore)(nil)
)

// EnqueueJob appends request-scoped outbox work inside the caller's existing
// tenant or subject transaction. Refusing a bare context is what makes the
// business mutation and its durable side effect one commit boundary.
func (s *PostgresStore) EnqueueJob(
	ctx context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	prepared, err := prepareJobEnqueue(request)
	if err != nil {
		return nil, err
	}
	tx, ok := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
	if !ok {
		return nil, jobs.ErrTransactionRequired
	}
	return enqueuePreparedJob(ctx, tx, prepared)
}

// EnqueueJob lets the privileged generic worker accept inbox or global work.
// Its dedicated role has no product-table grants, so this transaction remains
// confined to the common job platform.
func (s *PostgresJobStore) EnqueueJob(
	ctx context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	prepared, err := prepareJobEnqueue(request)
	if err != nil {
		return nil, err
	}
	var response *jobsv1.EnqueueJobResponse
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		response, err = enqueuePreparedJob(ctx, tx, prepared)
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func prepareJobEnqueue(request *jobsv1.EnqueueJobRequest) (preparedJobEnqueue, error) {
	fingerprint, err := jobs.EnqueueFingerprint(request)
	if err != nil {
		return preparedJobEnqueue{}, err
	}
	job := request.GetJob()
	direction, err := jobs.DatabaseDirection(job.GetDirection())
	if err != nil {
		return preparedJobEnqueue{}, err
	}
	scopeKind, organizationID, subjectID, err := enqueueScopeValues(job.GetScope())
	if err != nil {
		return preparedJobEnqueue{}, err
	}
	canonicalOrderingKey, err := jobs.CanonicalOrderingKey(job.GetOrdering())
	if err != nil {
		return preparedJobEnqueue{}, err
	}
	var orderingKey *string
	if canonicalOrderingKey != "" {
		orderingKey = &canonicalOrderingKey
	}
	attributes := []byte("{}")
	if len(job.GetAttributes()) > 0 {
		attributes, err = json.Marshal(job.GetAttributes())
		if err != nil {
			return preparedJobEnqueue{}, fmt.Errorf("jobs: encode attributes: %w", err)
		}
	}
	var availableAt *time.Time
	if job.GetAvailableAt() != nil {
		value := job.GetAvailableAt().AsTime()
		availableAt = &value
	}
	return preparedJobEnqueue{
		direction:          direction,
		scopeKind:          scopeKind,
		organizationID:     organizationID,
		subjectID:          subjectID,
		queue:              job.GetQueue(),
		topic:              job.GetTopic(),
		source:             job.GetSource(),
		idempotencyKey:     job.GetIdempotencyKey(),
		orderingKey:        orderingKey,
		schemaVersion:      int(job.GetSchemaVersion()),
		payload:            job.GetPayload(),
		contentType:        job.GetContentType(),
		attributes:         attributes,
		priority:           int16(job.GetPriority()),
		maxAttempts:        int(job.GetMaxAttempts()),
		availableAt:        availableAt,
		requestFingerprint: fingerprint,
	}, nil
}

func enqueueScopeValues(
	scope *jobsv1.JobScope,
) (string, *uuid.UUID, *uuid.UUID, error) {
	switch value := scope.GetValue().(type) {
	case *jobsv1.JobScope_OrganizationId:
		id, err := uuid.Parse(value.OrganizationId)
		if err != nil {
			return "", nil, nil, fmt.Errorf("jobs: parse organization scope: %w", err)
		}
		return "tenant", &id, nil, nil
	case *jobsv1.JobScope_SubjectId:
		id, err := uuid.Parse(value.SubjectId)
		if err != nil {
			return "", nil, nil, fmt.Errorf("jobs: parse subject scope: %w", err)
		}
		return "subject", nil, &id, nil
	case *jobsv1.JobScope_Global:
		return "global", nil, nil, nil
	default:
		return "", nil, nil, fmt.Errorf("%w: job scope is required", jobs.ErrInvalidCommand)
	}
}

func enqueuePreparedJob(
	ctx context.Context,
	executor QueryExecutor,
	job preparedJobEnqueue,
) (*jobsv1.EnqueueJobResponse, error) {
	var (
		jobID             string
		storedFingerprint []byte
		inserted          bool
	)
	err := executor.QueryRow(ctx, `
		SELECT job_id::text, stored_fingerprint, inserted
		FROM public.enqueue_job_message(
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16, $17
		)`,
		job.direction,
		job.scopeKind,
		job.organizationID,
		job.subjectID,
		job.queue,
		job.topic,
		job.source,
		job.idempotencyKey,
		job.orderingKey,
		job.schemaVersion,
		job.payload,
		job.contentType,
		job.attributes,
		job.priority,
		job.maxAttempts,
		job.availableAt,
		job.requestFingerprint[:],
	).Scan(&jobID, &storedFingerprint, &inserted)
	if err != nil {
		return nil, fmt.Errorf("enqueue job: %w", err)
	}
	if !bytes.Equal(storedFingerprint, job.requestFingerprint[:]) {
		return nil, jobs.ErrIdempotencyConflict
	}
	disposition := jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE
	if inserted {
		disposition = jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED
	}
	return &jobsv1.EnqueueJobResponse{JobId: jobID, Disposition: disposition}, nil
}
