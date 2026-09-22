package infra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// Compile-time check that PostgresStore implements GDPRStore.
var _ business.GDPRStore = (*PostgresStore)(nil)

// gdprRequestColumns is the single projection every read of a privacy request
// uses, so execution state cannot drift between the subject's view and the
// worker's.
const gdprRequestColumns = `
	id, user_id, type, status, job_id, download_url, expires_at, error,
	failure_code, attempt_count, lease_owner, lease_token, step_receipts,
	created_at, updated_at, completed_at`

func (s *PostgresStore) CreateGDPRRequest(ctx context.Context, req *business.GDPRRequest) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		INSERT INTO gdpr_requests (id, user_id, type, status, job_id)
		VALUES ($1, $2, $3, $4, $5)`,
		req.ID, req.UserID, string(req.Type), string(req.Status), nullableUUID(req.JobID))
	return err
}

func (s *PostgresStore) GetGDPRRequest(ctx context.Context, id string) (*business.GDPRRequest, error) {
	q := s.getQueryExecutor(ctx)

	req, err := scanGDPRRequest(q.QueryRow(ctx, `
		SELECT`+gdprRequestColumns+`
		FROM gdpr_requests WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("GDPR request not found"), business.ErrTypeNotFound)
		}
		return nil, err
	}
	return req, nil
}

func (s *PostgresStore) GetUserGDPRRequests(ctx context.Context, userID string) ([]*business.GDPRRequest, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT`+gdprRequestColumns+`
		FROM gdpr_requests WHERE user_id = $1
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*business.GDPRRequest
	for rows.Next() {
		req, err := scanGDPRRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// ClaimGDPRRequest stamps this attempt's fencing token on the request. Every
// state a worker can start from is claimable — including a failed request an
// operator replayed — but a completed one is returned untouched so redelivery
// never re-runs finished work.
//
// Two conditions fence the claim itself, so a worker that lost its lease cannot
// take the request back and run the adapter a second time. Within one job,
// attempts only ever increase, so a stale attempt carries a lower number than
// the attempt that replaced it. Across jobs — an operator replaying a
// dead-lettered request produces a new one — the attempt number restarts, so
// the claim instead requires that no attempt currently holds the row. Every
// transition that ends an attempt clears the lease, so a request a replay may
// legitimately take over always has a null token, while one still being worked
// does not.
//
// The row's own lease state is what fences this, deliberately: the worker's
// copy of its job lease expiry is captured once at claim time and never
// refreshed, while the heartbeat keeps extending the real lease, so comparing
// against that copy would refuse legitimate attempts whose lease is very much
// alive.
func (s *PostgresStore) ClaimGDPRRequest(
	ctx context.Context,
	id string,
	lease business.GDPRLease,
) (*business.GDPRRequest, error) {
	q := s.getQueryExecutor(ctx)

	req, err := scanGDPRRequest(q.QueryRow(ctx, `
		UPDATE gdpr_requests
		SET status = 'processing',
		    job_id = $5,
		    lease_owner = $2,
		    lease_token = $3,
		    attempt_count = $4,
		    failure_code = NULL,
		    error = '',
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status <> 'completed'
		  AND (
		      (job_id IS NOT DISTINCT FROM $5 AND attempt_count <= $4)
		      OR (job_id IS DISTINCT FROM $5 AND lease_token IS NULL)
		  )
		RETURNING`+gdprRequestColumns,
		id, lease.Owner, lease.Token, int(lease.Attempt),
		nullableUUID(lease.JobID)))
	if err == nil {
		return req, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	req, err = scanGDPRRequest(q.QueryRow(ctx, `
		SELECT`+gdprRequestColumns+`
		FROM gdpr_requests WHERE id = $1`, id))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, business.ErrPrivacyRequestNotFound
	case err != nil:
		return nil, err
	case req.Status == business.GDPRCompleted:
		return req, nil
	default:
		return nil, business.ErrPrivacyLeaseLost
	}
}

func (s *PostgresStore) RecordGDPRStepReceipt(
	ctx context.Context,
	id string,
	lease business.GDPRLease,
	step, receipt string,
) error {
	q := s.getQueryExecutor(ctx)

	// A receipt is written once: the first attempt that records a step owns the
	// evidence for it, so a later attempt that repeated the effect under the
	// same idempotency key cannot overwrite what was reported.
	tag, err := q.Exec(ctx, `
		UPDATE gdpr_requests
		SET step_receipts = step_receipts || jsonb_build_object($4::text, $5::text),
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND lease_token = $2 AND lease_owner = $3
		  AND NOT step_receipts ? $4::text`,
		id, lease.Token, lease.Owner, step, receipt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.gdprLeaseOutcome(ctx, id, lease, step)
	}
	return nil
}

// gdprLeaseOutcome separates the two reasons a fenced write matched no row: the
// step was already recorded (harmless, the effect is evidenced) or this worker
// no longer holds the lease (the attempt must stop).
func (s *PostgresStore) gdprLeaseOutcome(
	ctx context.Context,
	id string,
	lease business.GDPRLease,
	step string,
) error {
	q := s.getQueryExecutor(ctx)
	var owned, recorded bool
	err := q.QueryRow(ctx, `
		SELECT lease_token = $2 AND lease_owner = $3, step_receipts ? $4::text
		FROM gdpr_requests WHERE id = $1`, id, lease.Token, lease.Owner, step,
	).Scan(&owned, &recorded)
	if errors.Is(err, pgx.ErrNoRows) {
		return business.ErrPrivacyRequestNotFound
	}
	if err != nil {
		return err
	}
	if owned && recorded {
		return nil
	}
	return business.ErrPrivacyLeaseLost
}

// RecordGDPRExportArtifact stores the reference to a produced export artifact
// under lease. It is written as soon as the object exists, independently of
// whether the request later completes, because the reference is the only thing
// that lets the expiry sweep delete stored personal data.
func (s *PostgresStore) RecordGDPRExportArtifact(
	ctx context.Context,
	id string,
	lease business.GDPRLease,
	artifact business.PrivacyExportArtifact,
) error {
	q := s.getQueryExecutor(ctx)

	tag, err := q.Exec(ctx, `
		UPDATE gdpr_requests
		SET download_url = $4,
		    expires_at = $5,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND lease_token = $2 AND lease_owner = $3`,
		id, lease.Token, lease.Owner, artifact.DownloadURL, artifact.ExpiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return business.ErrPrivacyLeaseLost
	}
	return nil
}

// FailGDPRRequestByJob parks the request a job owns when that job can never
// execute — an envelope this runtime cannot decode, for instance. It is fenced
// on job_id, which uq_gdpr_requests_job makes unique, because the caller has
// neither a usable payload nor a lease. The lease is cleared so a later
// operator replay can take the request over.
func (s *PostgresStore) FailGDPRRequestByJob(
	ctx context.Context,
	jobID, failureCode, message string,
) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		UPDATE gdpr_requests
		SET status = 'failed',
		    failure_code = $2,
		    error = $3,
		    lease_owner = NULL,
		    lease_token = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE job_id = $1 AND status <> 'completed'`,
		nullableUUID(jobID), failureCode, message)
	return err
}

// LockPrivacyArtifactSweep serializes the expiry sweep across replicas for the
// remainder of the transaction.
func (s *PostgresStore) LockPrivacyArtifactSweep(ctx context.Context) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"privacy:artifact_sweep")
	return err
}

func (s *PostgresStore) FinishGDPRRequest(
	ctx context.Context,
	id string,
	lease business.GDPRLease,
	outcome business.GDPROutcome,
) error {
	q := s.getQueryExecutor(ctx)

	var completedAt *time.Time
	if outcome.Status == business.GDPRCompleted {
		now := time.Now()
		completedAt = &now
	}
	// An outcome that carries no artifact leaves any already-recorded one in
	// place. Clearing it would strand the stored object: the expiry sweep finds
	// artifacts by their reference, so dropping the reference on a failed
	// attempt means nothing ever deletes the personal data it points at.
	tag, err := q.Exec(ctx, `
		UPDATE gdpr_requests
		SET status = $4,
		    download_url = COALESCE(NULLIF($5::text, ''), download_url),
		    expires_at = COALESCE($6, expires_at),
		    failure_code = NULLIF($7::text, ''),
		    error = $8,
		    completed_at = $9,
		    lease_owner = NULL,
		    lease_token = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND lease_token = $2 AND lease_owner = $3`,
		id, lease.Token, lease.Owner,
		string(outcome.Status), outcome.DownloadURL, outcome.ExpiresAt,
		outcome.FailureCode, outcome.Error, completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return business.ErrPrivacyLeaseLost
	}
	return nil
}

func (s *PostgresStore) ListExpiredGDPRExports(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]*business.GDPRRequest, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT`+gdprRequestColumns+`
		FROM gdpr_requests
		WHERE type = 'export'
		  AND download_url IS NOT NULL
		  AND expires_at IS NOT NULL
		  AND expires_at <= $1
		ORDER BY expires_at
		LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*business.GDPRRequest
	for rows.Next() {
		req, err := scanGDPRRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// ClearGDPRExportArtifacts drops the references to artifacts the sweep has just
// deleted from storage. One statement covers the batch so a single row cannot
// fail mid-loop and abort the surrounding sweep transaction.
func (s *PostgresStore) ClearGDPRExportArtifacts(ctx context.Context, ids []string) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		UPDATE gdpr_requests
		SET download_url = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ANY($1::uuid[])`, ids)
	return err
}

func scanGDPRRequest(row rowScanner) (*business.GDPRRequest, error) {
	var req business.GDPRRequest
	var reqType, status string
	var jobID, downloadURL, errMsg, failureCode, leaseOwner, leaseToken *string
	var expiresAt, completedAt *time.Time
	var receipts []byte

	if err := row.Scan(
		&req.ID, &req.UserID, &reqType, &status, &jobID, &downloadURL, &expiresAt,
		&errMsg, &failureCode, &req.Attempt, &leaseOwner, &leaseToken, &receipts,
		&req.CreatedAt, &req.UpdatedAt, &completedAt,
	); err != nil {
		return nil, err
	}

	req.Type = business.GDPRRequestType(reqType)
	req.Status = business.GDPRRequestStatus(status)
	req.JobID = derefString(jobID)
	req.DownloadURL = derefString(downloadURL)
	req.Error = derefString(errMsg)
	req.FailureCode = derefString(failureCode)
	req.LeaseOwner = derefString(leaseOwner)
	req.LeaseToken = derefString(leaseToken)
	req.ExpiresAt = expiresAt
	req.CompletedAt = completedAt
	if len(receipts) > 0 {
		if err := json.Unmarshal(receipts, &req.StepReceipts); err != nil {
			return nil, err
		}
	}
	return &req, nil
}
