package infra

import (
	"accounts/pkg/business"
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

func scanExecutionCustody(row pgx.Row) (business.ExecutionCustodyRecord, error) {
	var v business.ExecutionCustodyRecord
	err := row.Scan(&v.Reference, &v.OrgID, &v.OwnerID, &v.AdmissionID, &v.Fingerprint, &v.Envelope, &v.ExpiresAt)
	return v, err
}

const executionCustodyColumns = `reference::text, org_id::text, owner_id::text, admission_id::text, fingerprint, envelope, expires_at`

func (s *PostgresStore) RegisterExecutionCustody(ctx context.Context, v business.ExecutionCustodyRecord) (business.ExecutionCustodyRecord, error) {
	var result business.ExecutionCustodyRecord
	// Private custody is inaccessible to tenant transactions. The broker verifies
	// owner admission before this call; SQL pins the complete registration key.
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		q := s.getQueryExecutor(ctx)
		_, err := q.Exec(ctx, `INSERT INTO execution_custody (reference,org_id,owner_id,admission_id,fingerprint,envelope,expires_at)
   VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (org_id,owner_id,admission_id) DO NOTHING`,
			v.Reference, v.OrgID, v.OwnerID, v.AdmissionID, v.Fingerprint, v.Envelope, v.ExpiresAt)
		if err != nil {
			return err
		}
		result, err = scanExecutionCustody(q.QueryRow(ctx, `SELECT `+executionCustodyColumns+` FROM execution_custody WHERE org_id=$1 AND owner_id=$2 AND admission_id=$3`, v.OrgID, v.OwnerID, v.AdmissionID))
		return err
	})
	return result, err
}

func (s *PostgresStore) GetExecutionCustody(ctx context.Context, reference string) (business.ExecutionCustodyRecord, error) {
	var result business.ExecutionCustodyRecord
	// A replacement worker has no owner session. Only the broker calls this
	// private reference lookup, and checks the encrypted binding before deriving.
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		result, err = scanExecutionCustody(s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT `+executionCustodyColumns+` FROM execution_custody WHERE reference=$1`, reference))
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	})
	return result, err
}

func (s *PostgresStore) PurgeExecutionCustody(ctx context.Context, before time.Time) error {
	// Expiry cleanup spans tenants, erases ciphertext, and retains tombstones.
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		_, err := s.getQueryExecutor(ctx).Exec(ctx, `UPDATE execution_custody SET envelope='' WHERE envelope<>'' AND expires_at <= LEAST($1, CURRENT_TIMESTAMP)`, before)
		return err
	})
}

var _ business.ExecutionCustodyStore = (*PostgresStore)(nil)

// FindExecutionCustody recovers the original registration identity after a lost
// acknowledgement. Only an authenticated owner admission may call this path.
func (s *PostgresStore) FindExecutionCustody(ctx context.Context, org, owner, admission string) (business.ExecutionCustodyRecord, error) {
	var result business.ExecutionCustodyRecord
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		result, err = scanExecutionCustody(s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT `+executionCustodyColumns+` FROM execution_custody WHERE org_id=$1 AND owner_id=$2 AND admission_id=$3`, org, owner, admission))
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	})
	return result, err
}
