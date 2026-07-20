package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PostgresJobStore implements the generic worker contract with database-clock
// leases, row locking, and a fresh UUID fencing token for every attempt.
type PostgresJobStore struct {
	pool *pgxpool.Pool
}

func NewPostgresJobStore(pool *pgxpool.Pool) *PostgresJobStore {
	return &PostgresJobStore{pool: pool}
}

var _ jobs.Store = (*PostgresJobStore)(nil)

func (s *PostgresJobStore) Claim(
	ctx context.Context,
	request *jobsv1.ClaimJobsRequest,
) (*jobsv1.ClaimJobsResponse, error) {
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}

	response := &jobsv1.ClaimJobsResponse{}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := recoverExpiredJobLeases(ctx, tx, request.GetQueue(), request.GetLimit()); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, claimJobsSQL,
			request.GetQueue(),
			request.GetLimit(),
			request.GetWorkerId(),
			request.GetLeaseDuration().AsDuration().Seconds(),
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			envelope, err := scanJobEnvelope(rows)
			if err != nil {
				return err
			}
			response.Jobs = append(response.Jobs, envelope)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("claim jobs: %w", err)
	}
	return response, nil
}

func (s *PostgresJobStore) Heartbeat(
	ctx context.Context,
	request *jobsv1.HeartbeatJobRequest,
) (*jobsv1.HeartbeatJobResponse, error) {
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}

	var lease *jobsv1.JobLease
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		ref := request.GetLease()
		var owner, token string
		var expiresAt, heartbeatAt time.Time
		err := tx.QueryRow(ctx, `
			UPDATE job_messages
			SET lease_expires_at = NOW() + ($4::double precision * INTERVAL '1 second'),
			    heartbeat_at = NOW()
			WHERE id = $1::uuid
			  AND state = 'processing'
			  AND lease_owner = $2
			  AND lease_token = $3::uuid
			  AND lease_expires_at > NOW()
			RETURNING lease_owner, lease_token::text, lease_expires_at, heartbeat_at`,
			ref.GetJobId(), ref.GetWorkerId(), ref.GetLeaseToken(),
			request.GetExtension().AsDuration().Seconds(),
		).Scan(&owner, &token, &expiresAt, &heartbeatAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.ErrLeaseLost
		}
		if err != nil {
			return err
		}

		result, err := tx.Exec(ctx, `
			UPDATE job_attempts
			SET heartbeat_at = $4
			WHERE job_id = $1::uuid
			  AND worker_id = $2
			  AND lease_token = $3::uuid
			  AND finished_at IS NULL`,
			ref.GetJobId(), ref.GetWorkerId(), ref.GetLeaseToken(), heartbeatAt,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("jobs: live lease has no matching open attempt")
		}

		lease = &jobsv1.JobLease{
			Owner:       owner,
			Token:       token,
			ExpiresAt:   timestamppb.New(expiresAt),
			HeartbeatAt: timestamppb.New(heartbeatAt),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("heartbeat job: %w", err)
	}
	return &jobsv1.HeartbeatJobResponse{Lease: lease}, nil
}

func (s *PostgresJobStore) Complete(
	ctx context.Context,
	request *jobsv1.CompleteJobRequest,
) error {
	if err := jobs.ValidateCommand(request); err != nil {
		return err
	}
	return s.finalize(ctx, request.GetLease(), jobFinalization{
		state:   jobsv1.JobState_JOB_STATE_SUCCEEDED,
		outcome: "succeeded",
	})
}

func (s *PostgresJobStore) Retry(
	ctx context.Context,
	request *jobsv1.RetryJobRequest,
) (*jobsv1.RetryJobResponse, error) {
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	state, err := s.finalizeRetry(ctx, request)
	if err != nil {
		return nil, err
	}
	return &jobsv1.RetryJobResponse{State: state}, nil
}

func (s *PostgresJobStore) DeadLetter(
	ctx context.Context,
	request *jobsv1.DeadLetterJobRequest,
) error {
	if err := jobs.ValidateCommand(request); err != nil {
		return err
	}
	return s.finalize(ctx, request.GetLease(), jobFinalization{
		state:   jobsv1.JobState_JOB_STATE_DEAD_LETTER,
		outcome: "permanent_failure",
		failure: request.GetFailure(),
	})
}

type jobFinalization struct {
	state     jobsv1.JobState
	outcome   string
	failure   *jobsv1.JobFailure
	available *time.Time
}

func (s *PostgresJobStore) finalizeRetry(
	ctx context.Context,
	request *jobsv1.RetryJobRequest,
) (jobsv1.JobState, error) {
	state := jobsv1.JobState_JOB_STATE_UNSPECIFIED
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		attemptCount, maxAttempts, err := lockLiveJobLease(ctx, tx, request.GetLease())
		if err != nil {
			return err
		}
		state = jobsv1.JobState_JOB_STATE_RETRYING
		if attemptCount >= maxAttempts {
			state = jobsv1.JobState_JOB_STATE_DEAD_LETTER
		}
		retryAt := request.GetRetryAt().AsTime()
		return finalizeLockedJob(ctx, tx, request.GetLease(), jobFinalization{
			state:     state,
			outcome:   "retryable_failure",
			failure:   request.GetFailure(),
			available: &retryAt,
		})
	})
	if err != nil {
		return jobsv1.JobState_JOB_STATE_UNSPECIFIED, fmt.Errorf("retry job: %w", err)
	}
	return state, nil
}

func (s *PostgresJobStore) finalize(
	ctx context.Context,
	lease *jobsv1.JobLeaseReference,
	finalization jobFinalization,
) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := lockLiveJobLease(ctx, tx, lease); err != nil {
			return err
		}
		return finalizeLockedJob(ctx, tx, lease, finalization)
	})
	if err != nil {
		return fmt.Errorf("finalize job: %w", err)
	}
	return nil
}

func lockLiveJobLease(
	ctx context.Context,
	tx pgx.Tx,
	lease *jobsv1.JobLeaseReference,
) (int, int, error) {
	var attemptCount, maxAttempts int
	err := tx.QueryRow(ctx, `
		SELECT attempt_count, max_attempts
		FROM job_messages
		WHERE id = $1::uuid
		  AND state = 'processing'
		  AND lease_owner = $2
		  AND lease_token = $3::uuid
		  AND lease_expires_at > NOW()
		FOR UPDATE`,
		lease.GetJobId(), lease.GetWorkerId(), lease.GetLeaseToken(),
	).Scan(&attemptCount, &maxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, jobs.ErrLeaseLost
	}
	return attemptCount, maxAttempts, err
}

func finalizeLockedJob(
	ctx context.Context,
	tx pgx.Tx,
	lease *jobsv1.JobLeaseReference,
	finalization jobFinalization,
) error {
	var failureCode, failureMessage *string
	if finalization.failure != nil {
		failureCode = &finalization.failure.Code
		if finalization.failure.Message != "" {
			failureMessage = &finalization.failure.Message
		}
	}

	result, err := tx.Exec(ctx, `
		UPDATE job_attempts
		SET outcome = $4,
		    error_code = $5,
		    error_message = $6,
		    heartbeat_at = GREATEST(heartbeat_at, NOW()),
		    finished_at = NOW()
		WHERE job_id = $1::uuid
		  AND worker_id = $2
		  AND lease_token = $3::uuid
		  AND finished_at IS NULL`,
		lease.GetJobId(), lease.GetWorkerId(), lease.GetLeaseToken(),
		finalization.outcome, failureCode, failureMessage,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("jobs: live lease has no matching open attempt")
	}

	state, err := jobs.DatabaseState(finalization.state)
	if err != nil {
		return err
	}
	result, err = tx.Exec(ctx, `
		UPDATE job_messages
		SET state = $4,
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    heartbeat_at = NULL,
		    last_error_code = $5,
		    last_error_message = $6,
		    available_at = CASE WHEN $4 = 'retrying' THEN $7 ELSE available_at END,
		    completed_at = CASE WHEN $4 = 'succeeded' THEN NOW() ELSE NULL END,
		    dead_lettered_at = CASE WHEN $4 = 'dead_letter' THEN NOW() ELSE NULL END
		WHERE id = $1::uuid
		  AND state = 'processing'
		  AND lease_owner = $2
		  AND lease_token = $3::uuid`,
		lease.GetJobId(), lease.GetWorkerId(), lease.GetLeaseToken(),
		state, failureCode, failureMessage, finalization.available,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return jobs.ErrLeaseLost
	}
	return nil
}

func recoverExpiredJobLeases(ctx context.Context, tx pgx.Tx, queue string, limit uint32) error {
	_, err := tx.Exec(ctx, `
		WITH expired AS (
			SELECT id, lease_token, attempt_count, max_attempts
			FROM job_messages
			WHERE queue = $1
			  AND state = 'processing'
			  AND lease_expires_at <= NOW()
			ORDER BY lease_expires_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		), closed_attempts AS (
			UPDATE job_attempts attempt
			SET outcome = 'lease_expired',
			    error_code = 'jobs.lease_expired',
			    error_message = 'processing lease expired before finalization',
			    heartbeat_at = GREATEST(attempt.heartbeat_at, NOW()),
			    finished_at = NOW()
			FROM expired
			WHERE attempt.job_id = expired.id
			  AND attempt.lease_token = expired.lease_token
			  AND attempt.finished_at IS NULL
			RETURNING attempt.job_id
		)
		UPDATE job_messages message
		SET state = CASE
		        WHEN message.attempt_count >= message.max_attempts THEN 'dead_letter'
		        ELSE 'retrying'
		    END,
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    heartbeat_at = NULL,
		    last_error_code = 'jobs.lease_expired',
		    last_error_message = 'processing lease expired before finalization',
		    available_at = CASE
		        WHEN message.attempt_count < message.max_attempts THEN NOW()
		        ELSE message.available_at
		    END,
		    dead_lettered_at = CASE
		        WHEN message.attempt_count >= message.max_attempts THEN NOW()
		        ELSE NULL
		    END
		FROM expired
		WHERE message.id = expired.id
		  AND EXISTS (
		      SELECT 1 FROM closed_attempts WHERE job_id = message.id
		  )`,
		queue, limit,
	)
	return err
}

const claimJobsSQL = `
	WITH candidates AS (
		SELECT message.id
		FROM job_messages message
		WHERE message.queue = $1
		  AND message.state IN ('pending', 'retrying')
		  AND message.available_at <= NOW()
		  AND message.attempt_count < message.max_attempts
		  AND (
		      message.ordering_key IS NULL
		      OR NOT EXISTS (
		          SELECT 1
		          FROM job_messages prior
		          WHERE prior.queue = message.queue
		            AND prior.ordering_key = message.ordering_key
		            AND prior.state IN ('pending', 'processing', 'retrying')
		            AND (prior.created_at, prior.id) < (message.created_at, message.id)
		      )
		  )
		ORDER BY message.priority DESC, message.available_at, message.created_at, message.id
		FOR UPDATE OF message SKIP LOCKED
		LIMIT $2
	), claimed AS (
		UPDATE job_messages message
		SET state = 'processing',
		    attempt_count = message.attempt_count + 1,
		    lease_owner = $3,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = NOW() + ($4::double precision * INTERVAL '1 second'),
		    heartbeat_at = NOW(),
		    last_attempt_at = NOW()
		FROM candidates
		WHERE message.id = candidates.id
		RETURNING message.*
	), opened_attempts AS (
		INSERT INTO job_attempts (
			id, job_id, attempt_number, worker_id, lease_token,
			started_at, heartbeat_at
		)
		SELECT gen_random_uuid(), id, attempt_count, lease_owner, lease_token,
		       heartbeat_at, heartbeat_at
		FROM claimed
		RETURNING job_id
	)
	SELECT
		claimed.id::text, claimed.direction, claimed.scope_kind,
		COALESCE(claimed.organization_id::text, ''),
		COALESCE(claimed.subject_id::text, ''),
		claimed.queue, claimed.topic, claimed.source, claimed.idempotency_key,
		COALESCE(claimed.ordering_key, ''), claimed.schema_version,
		claimed.payload, claimed.content_type, claimed.attributes::text,
		claimed.state, claimed.priority, claimed.attempt_count, claimed.max_attempts,
		COALESCE(claimed.lease_owner, ''), COALESCE(claimed.lease_token::text, ''),
		claimed.lease_expires_at, claimed.heartbeat_at,
		COALESCE(claimed.last_error_code, ''),
		COALESCE(claimed.last_error_message, ''),
		claimed.available_at, claimed.last_attempt_at, claimed.completed_at,
		claimed.dead_lettered_at, claimed.created_at, claimed.updated_at,
		claimed.state_version, COALESCE(claimed.replay_of::text, '')
	FROM claimed
	JOIN opened_attempts ON opened_attempts.job_id = claimed.id
	ORDER BY claimed.priority DESC, claimed.available_at, claimed.created_at, claimed.id`

type jobEnvelopeScanner interface {
	Scan(...any) error
}

func scanJobEnvelope(row jobEnvelopeScanner) (*jobsv1.JobEnvelope, error) {
	var (
		id, direction, scopeKind, organizationID, subjectID string
		queue, topic, source, idempotencyKey, orderingKey   string
		payload, attributesJSON                             []byte
		contentType, state                                  string
		leaseOwner, leaseToken, failureCode, failureMessage string
		replayOf                                            string
		schemaVersion, priority, attemptCount, maxAttempts  int
		stateVersion                                        int64
		leaseExpiresAt, heartbeatAt                         *time.Time
		lastAttemptAt, completedAt, deadLetteredAt          *time.Time
		createdAt, updatedAt                                time.Time
		availableAt                                         time.Time
	)
	if err := row.Scan(
		&id, &direction, &scopeKind, &organizationID, &subjectID,
		&queue, &topic, &source, &idempotencyKey, &orderingKey,
		&schemaVersion, &payload, &contentType, &attributesJSON,
		&state, &priority, &attemptCount, &maxAttempts,
		&leaseOwner, &leaseToken, &leaseExpiresAt, &heartbeatAt,
		&failureCode, &failureMessage, &availableAt, &lastAttemptAt,
		&completedAt, &deadLetteredAt, &createdAt, &updatedAt,
		&stateVersion, &replayOf,
	); err != nil {
		return nil, err
	}

	jobDirection, err := jobs.ParseDatabaseDirection(direction)
	if err != nil {
		return nil, err
	}
	jobState, err := jobs.ParseDatabaseState(state)
	if err != nil {
		return nil, err
	}
	attributes := make(map[string]string)
	if err := json.Unmarshal(attributesJSON, &attributes); err != nil {
		return nil, fmt.Errorf("decode job attributes: %w", err)
	}

	scope, err := databaseJobScope(scopeKind, organizationID, subjectID)
	if err != nil {
		return nil, err
	}
	envelope := &jobsv1.JobEnvelope{
		Id:             id,
		Direction:      jobDirection,
		Scope:          scope,
		Queue:          queue,
		Topic:          topic,
		Source:         source,
		IdempotencyKey: idempotencyKey,
		OrderingKey:    orderingKey,
		SchemaVersion:  uint32(schemaVersion),
		Payload:        payload,
		ContentType:    contentType,
		Attributes:     attributes,
		State:          jobState,
		Priority:       int32(priority),
		AttemptCount:   uint32(attemptCount),
		MaxAttempts:    uint32(maxAttempts),
		AvailableAt:    timestamppb.New(availableAt),
		CreatedAt:      timestamppb.New(createdAt),
		UpdatedAt:      timestamppb.New(updatedAt),
		StateVersion:   uint64(stateVersion),
		ReplayOf:       replayOf,
	}
	if leaseOwner != "" {
		envelope.Lease = &jobsv1.JobLease{
			Owner:       leaseOwner,
			Token:       leaseToken,
			ExpiresAt:   optionalTimestamp(leaseExpiresAt),
			HeartbeatAt: optionalTimestamp(heartbeatAt),
		}
	}
	if failureCode != "" {
		envelope.LastFailure = &jobsv1.JobFailure{Code: failureCode, Message: failureMessage}
	}
	envelope.LastAttemptAt = optionalTimestamp(lastAttemptAt)
	envelope.CompletedAt = optionalTimestamp(completedAt)
	envelope.DeadLetteredAt = optionalTimestamp(deadLetteredAt)
	return envelope, nil
}

func databaseJobScope(kind, organizationID, subjectID string) (*jobsv1.JobScope, error) {
	switch kind {
	case "tenant":
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
			OrganizationId: organizationID,
		}}, nil
	case "subject":
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{
			SubjectId: subjectID,
		}}, nil
	case "global":
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}}, nil
	default:
		return nil, fmt.Errorf("jobs: unknown database scope %q", kind)
	}
}

func optionalTimestamp(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
