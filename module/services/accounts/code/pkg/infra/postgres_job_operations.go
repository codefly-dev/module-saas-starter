package infra

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ jobs.Operations = (*PostgresJobStore)(nil)

const defaultJobPageSize = 50

// GetJobOperations returns database-derived, low-cardinality queue state. It
// is deliberately not computed from one process's counters, so every replica
// presents the same durable view.
func (s *PostgresJobStore) GetJobOperations(
	ctx context.Context,
	request *jobsv1.GetJobOperationsRequest,
) (*jobsv1.GetJobOperationsResponse, error) {
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	w, end := wool.StartSpan(ctx, "jobs.GetJobOperations")
	defer end()
	ctx = w.Context()
	var observedAt time.Time
	if err := s.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&observedAt); err != nil {
		return nil, fmt.Errorf("observe job operations clock: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT queue,
		       COUNT(*) FILTER (WHERE state = 'pending'),
		       COUNT(*) FILTER (WHERE state = 'processing'),
		       COUNT(*) FILTER (WHERE state = 'retrying'),
		       COUNT(*) FILTER (WHERE state = 'succeeded'),
		       COUNT(*) FILTER (WHERE state = 'dead_letter'),
		       COUNT(*) FILTER (WHERE state = 'canceled'),
		       COUNT(*) FILTER (
		           WHERE state IN ('pending', 'retrying') AND available_at <= NOW()
		       ),
		       COUNT(*) FILTER (
		           WHERE state IN ('pending', 'retrying') AND available_at > NOW()
		       ),
		       COUNT(*) FILTER (
		           WHERE state = 'processing' AND lease_expires_at <= NOW()
		       ),
		       MIN(available_at) FILTER (
		           WHERE state IN ('pending', 'retrying') AND available_at <= NOW()
		       )
		FROM job_messages
		WHERE ($1 = '' OR queue = $1)
		GROUP BY queue
		ORDER BY queue`, request.GetQueue())
	if err != nil {
		return nil, fmt.Errorf("get job operations: %w", err)
	}
	defer rows.Close()

	response := &jobsv1.GetJobOperationsResponse{ObservedAt: timestamppb.New(observedAt)}
	for rows.Next() {
		snapshot := &jobsv1.JobQueueSnapshot{}
		var pending, processing, retrying, succeeded, deadLetter, canceled int64
		var ready, scheduled, expiredLeases int64
		var oldestReadyAt *time.Time
		if err := rows.Scan(
			&snapshot.Queue,
			&pending,
			&processing,
			&retrying,
			&succeeded,
			&deadLetter,
			&canceled,
			&ready,
			&scheduled,
			&expiredLeases,
			&oldestReadyAt,
		); err != nil {
			return nil, fmt.Errorf("scan job operations: %w", err)
		}
		snapshot.Pending = uint64(pending)
		snapshot.Processing = uint64(processing)
		snapshot.Retrying = uint64(retrying)
		snapshot.Succeeded = uint64(succeeded)
		snapshot.DeadLetter = uint64(deadLetter)
		snapshot.Canceled = uint64(canceled)
		snapshot.Ready = uint64(ready)
		snapshot.Scheduled = uint64(scheduled)
		snapshot.ExpiredLeases = uint64(expiredLeases)
		snapshot.OldestReadyAt = optionalTimestamp(oldestReadyAt)
		response.Queues = append(response.Queues, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get job operations: %w", err)
	}
	return response, nil
}

type jobPageCursor struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeJobPageToken(summary *jobsv1.JobSummary) (string, error) {
	if summary.GetCreatedAt() == nil || !summary.GetCreatedAt().IsValid() {
		return "", fmt.Errorf("%w: result has no valid creation time", jobs.ErrInvalidPageToken)
	}
	cursor := jobPageCursor{
		Version:   1,
		CreatedAt: summary.GetCreatedAt().AsTime().UTC(),
		ID:        summary.GetId(),
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode job page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeJobPageToken(token string) (*jobPageCursor, error) {
	if token == "" {
		return nil, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: base64", jobs.ErrInvalidPageToken)
	}
	var cursor jobPageCursor
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: cursor", jobs.ErrInvalidPageToken)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data", jobs.ErrInvalidPageToken)
	}
	if _, err := uuid.Parse(cursor.ID); err != nil {
		return nil, fmt.Errorf("%w: id", jobs.ErrInvalidPageToken)
	}
	return &cursor, nil
}

// ListJobs applies seek pagination to a metadata-only projection. The SQL
// intentionally contains neither payload nor attributes columns.
func (s *PostgresJobStore) ListJobs(
	ctx context.Context,
	request *jobsv1.ListJobsRequest,
) (*jobsv1.ListJobsResponse, error) {
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	cursor, err := decodeJobPageToken(request.GetPageToken())
	if err != nil {
		return nil, err
	}
	w, end := wool.StartSpan(ctx, "jobs.ListJobs")
	defer end()
	ctx = w.Context()

	pageSize := request.GetPageSize()
	if pageSize == 0 {
		pageSize = defaultJobPageSize
	}
	clauses := []string{"TRUE"}
	args := make([]any, 0, 7)
	add := func(format string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(format, len(args)))
	}
	if request.GetQueue() != "" {
		add("queue = $%d", request.GetQueue())
	}
	if len(request.GetStates()) > 0 {
		states := make([]string, 0, len(request.GetStates()))
		for _, state := range request.GetStates() {
			value, err := jobs.DatabaseState(state)
			if err != nil {
				return nil, err
			}
			states = append(states, value)
		}
		add("state = ANY($%d::text[])", states)
	}
	switch request.GetScope().(type) {
	case *jobsv1.ListJobsRequest_OrganizationId:
		add("organization_id = $%d::uuid", request.GetOrganizationId())
	case *jobsv1.ListJobsRequest_SubjectId:
		add("subject_id = $%d::uuid", request.GetSubjectId())
	}
	if cursor != nil {
		args = append(args, cursor.CreatedAt, cursor.ID)
		clauses = append(clauses, fmt.Sprintf(
			"(created_at, id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args),
		))
	}
	args = append(args, pageSize+1)

	rows, err := s.pool.Query(ctx, jobSummarySelect+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY created_at DESC, id DESC
		LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	response := &jobsv1.ListJobsResponse{}
	for rows.Next() {
		summary, err := scanJobSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}
		response.Jobs = append(response.Jobs, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	if len(response.Jobs) > int(pageSize) {
		response.Jobs = response.Jobs[:pageSize]
		response.NextPageToken, err = encodeJobPageToken(response.Jobs[len(response.Jobs)-1])
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (s *PostgresJobStore) GetJob(
	ctx context.Context,
	request *jobsv1.GetJobRequest,
) (*jobsv1.GetJobResponse, error) {
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	w, end := wool.StartSpan(ctx, "jobs.GetJob")
	defer end()
	ctx = w.Context()

	summary, err := scanJobSummaryRow(s.pool.QueryRow(
		ctx,
		jobSummarySelect+" WHERE id = $1::uuid",
		request.GetJobId(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, jobs.ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job: %w", err)
	}
	response := &jobsv1.GetJobResponse{Job: summary}

	attempts, err := s.pool.Query(ctx, `
		SELECT id::text, job_id::text, attempt_number, worker_id,
		       lease_token::text, COALESCE(outcome, ''),
		       COALESCE(error_code, ''), COALESCE(error_message, ''),
		       started_at, heartbeat_at, finished_at
		FROM job_attempts
		WHERE job_id = $1::uuid
		ORDER BY attempt_number`, request.GetJobId())
	if err != nil {
		return nil, fmt.Errorf("get job attempts: %w", err)
	}
	for attempts.Next() {
		attempt := &jobsv1.JobAttempt{}
		var number int
		var outcome, failureCode, failureMessage string
		var startedAt, heartbeatAt time.Time
		var finishedAt *time.Time
		if err := attempts.Scan(
			&attempt.Id, &attempt.JobId, &number, &attempt.WorkerId,
			&attempt.LeaseToken, &outcome, &failureCode, &failureMessage,
			&startedAt, &heartbeatAt, &finishedAt,
		); err != nil {
			attempts.Close()
			return nil, fmt.Errorf("scan job attempt: %w", err)
		}
		attempt.Number = uint32(number)
		attempt.Outcome, err = parseJobAttemptOutcome(outcome)
		if err != nil {
			attempts.Close()
			return nil, err
		}
		if failureCode != "" {
			attempt.Failure = &jobsv1.JobFailure{Code: failureCode, Message: failureMessage}
		}
		attempt.StartedAt = timestamppb.New(startedAt)
		attempt.HeartbeatAt = timestamppb.New(heartbeatAt)
		attempt.FinishedAt = optionalTimestamp(finishedAt)
		response.Attempts = append(response.Attempts, attempt)
	}
	if err := attempts.Err(); err != nil {
		attempts.Close()
		return nil, fmt.Errorf("get job attempts: %w", err)
	}
	attempts.Close()

	transitions, err := s.pool.Query(ctx, `
		SELECT sequence, job_id::text, COALESCE(from_state, ''), to_state,
		       state_version, attempt_count, actor,
		       COALESCE(error_code, ''), COALESCE(error_message, ''), occurred_at
		FROM job_state_transitions
		WHERE job_id = $1::uuid
		ORDER BY sequence`, request.GetJobId())
	if err != nil {
		return nil, fmt.Errorf("get job transitions: %w", err)
	}
	defer transitions.Close()
	for transitions.Next() {
		transition := &jobsv1.JobStateTransition{}
		var fromState, toState, failureCode, failureMessage string
		var stateVersion int64
		var attemptCount int
		var occurredAt time.Time
		if err := transitions.Scan(
			&transition.Sequence, &transition.JobId, &fromState, &toState,
			&stateVersion, &attemptCount, &transition.Actor,
			&failureCode, &failureMessage, &occurredAt,
		); err != nil {
			return nil, fmt.Errorf("scan job transition: %w", err)
		}
		if fromState != "" {
			transition.FromState, err = jobs.ParseDatabaseState(fromState)
			if err != nil {
				return nil, err
			}
		}
		transition.ToState, err = jobs.ParseDatabaseState(toState)
		if err != nil {
			return nil, err
		}
		transition.StateVersion = uint64(stateVersion)
		transition.AttemptCount = uint32(attemptCount)
		if failureCode != "" {
			transition.Failure = &jobsv1.JobFailure{Code: failureCode, Message: failureMessage}
		}
		transition.OccurredAt = timestamppb.New(occurredAt)
		response.Transitions = append(response.Transitions, transition)
	}
	if err := transitions.Err(); err != nil {
		return nil, fmt.Errorf("get job transitions: %w", err)
	}
	return response, nil
}

func (s *PostgresJobStore) ReplayJob(
	ctx context.Context,
	request *jobsv1.ReplayJobRequest,
) (*jobsv1.ReplayJobResponse, error) {
	fingerprint, err := jobs.ReplayFingerprint(request)
	if err != nil {
		return nil, err
	}
	w, end := wool.StartSpan(ctx, "jobs.ReplayJob")
	defer end()
	ctx = w.Context()

	var jobID string
	var storedFingerprint []byte
	var inserted bool
	var availableAt *time.Time
	if request.GetAvailableAt() != nil {
		value := request.GetAvailableAt().AsTime()
		availableAt = &value
	}
	err = s.pool.QueryRow(ctx, `
		SELECT job_id::text, stored_fingerprint, inserted
		FROM replay_job_message($1::uuid, $2, $3, $4)`,
		request.GetSourceJobId(), request.GetIdempotencyKey(), availableAt, fingerprint[:],
	).Scan(&jobID, &storedFingerprint, &inserted)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "P0002":
				return nil, jobs.ErrJobNotFound
			case "55000":
				return nil, jobs.ErrReplayNotAllowed
			}
		}
		return nil, fmt.Errorf("replay job: %w", err)
	}
	if !bytes.Equal(storedFingerprint, fingerprint[:]) {
		return nil, jobs.ErrIdempotencyConflict
	}
	disposition := jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE
	if inserted {
		disposition = jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED
	}
	return &jobsv1.ReplayJobResponse{JobId: jobID, Disposition: disposition}, nil
}

const jobSummarySelect = `
	SELECT id::text, direction, scope_kind,
	       COALESCE(organization_id::text, ''), COALESCE(subject_id::text, ''),
	       queue, topic, source, idempotency_key, COALESCE(ordering_key, ''),
	       schema_version, content_type, state, priority, attempt_count, max_attempts,
	       COALESCE(lease_owner, ''), COALESCE(lease_token::text, ''),
	       lease_expires_at, heartbeat_at,
	       COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
	       available_at, last_attempt_at, completed_at, dead_lettered_at,
	       created_at, updated_at, state_version, COALESCE(replay_of::text, '')
	FROM job_messages`

func scanJobSummary(rows pgx.Rows) (*jobsv1.JobSummary, error) {
	return scanJobSummaryValue(rows)
}

func scanJobSummaryRow(row pgx.Row) (*jobsv1.JobSummary, error) {
	return scanJobSummaryValue(row)
}

func scanJobSummaryValue(row jobEnvelopeScanner) (*jobsv1.JobSummary, error) {
	var (
		summary                                            jobsv1.JobSummary
		direction, scopeKind, organizationID, subjectID    string
		state, leaseOwner, leaseToken                      string
		failureCode, failureMessage                        string
		schemaVersion, priority, attemptCount, maxAttempts int
		stateVersion                                       int64
		leaseExpiresAt, heartbeatAt                        *time.Time
		lastAttemptAt, completedAt, deadLetteredAt         *time.Time
		availableAt, createdAt, updatedAt                  time.Time
	)
	if err := row.Scan(
		&summary.Id, &direction, &scopeKind, &organizationID, &subjectID,
		&summary.Queue, &summary.Topic, &summary.Source, &summary.IdempotencyKey,
		&summary.OrderingKey, &schemaVersion, &summary.ContentType, &state,
		&priority, &attemptCount, &maxAttempts, &leaseOwner, &leaseToken,
		&leaseExpiresAt, &heartbeatAt, &failureCode, &failureMessage,
		&availableAt, &lastAttemptAt, &completedAt, &deadLetteredAt,
		&createdAt, &updatedAt, &stateVersion, &summary.ReplayOf,
	); err != nil {
		return nil, err
	}
	var err error
	summary.Direction, err = jobs.ParseDatabaseDirection(direction)
	if err != nil {
		return nil, err
	}
	summary.Scope, err = databaseJobScope(scopeKind, organizationID, subjectID)
	if err != nil {
		return nil, err
	}
	summary.State, err = jobs.ParseDatabaseState(state)
	if err != nil {
		return nil, err
	}
	summary.SchemaVersion = uint32(schemaVersion)
	summary.Priority = int32(priority)
	summary.AttemptCount = uint32(attemptCount)
	summary.MaxAttempts = uint32(maxAttempts)
	summary.StateVersion = uint64(stateVersion)
	summary.AvailableAt = timestamppb.New(availableAt)
	summary.LastAttemptAt = optionalTimestamp(lastAttemptAt)
	summary.CompletedAt = optionalTimestamp(completedAt)
	summary.DeadLetteredAt = optionalTimestamp(deadLetteredAt)
	summary.CreatedAt = timestamppb.New(createdAt)
	summary.UpdatedAt = timestamppb.New(updatedAt)
	if leaseOwner != "" {
		summary.Lease = &jobsv1.JobLease{
			Owner: leaseOwner, Token: leaseToken,
			ExpiresAt: optionalTimestamp(leaseExpiresAt), HeartbeatAt: optionalTimestamp(heartbeatAt),
		}
	}
	if failureCode != "" {
		summary.LastFailure = &jobsv1.JobFailure{Code: failureCode, Message: failureMessage}
	}
	return &summary, nil
}

func parseJobAttemptOutcome(value string) (jobsv1.JobAttemptOutcome, error) {
	switch value {
	case "":
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_UNSPECIFIED, nil
	case "succeeded":
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_SUCCEEDED, nil
	case "retryable_failure":
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_RETRYABLE_FAILURE, nil
	case "permanent_failure":
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_PERMANENT_FAILURE, nil
	case "lease_expired":
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_LEASE_EXPIRED, nil
	case "canceled":
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_CANCELED, nil
	default:
		return jobsv1.JobAttemptOutcome_JOB_ATTEMPT_OUTCOME_UNSPECIFIED,
			fmt.Errorf("jobs: unknown database attempt outcome %q", value)
	}
}
