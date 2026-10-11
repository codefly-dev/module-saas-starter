package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// The module envelope's persistence.
//
// module_envelopes is a control-plane-owned platform relation (no tenant
// column, no RLS), granted SELECT and INSERT only. Each row is the whole
// ceiling at one revision; the current ceiling is the highest revision, and a
// correction is a new revision rather than an edit. Every method assumes the
// caller opened a WithControlPlane transaction.

// LatestModuleEnvelopeRecord returns the highest revision, or nil when none has
// been written.
func (s *PostgresStore) LatestModuleEnvelopeRecord(ctx context.Context) (*business.ModuleEnvelopeRecord, error) {
	var (
		record   business.ModuleEnvelopeRecord
		revision int64
	)
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT revision, document::text, written_by::text, written_at
		  FROM public.module_envelopes
		 ORDER BY revision DESC
		 LIMIT 1`).Scan(&revision, &record.Document, &record.WrittenBy, &record.WrittenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.Revision = uint64(revision) //nolint:gosec // the column is CHECKed positive
	return &record, nil
}

// AppendModuleEnvelopeRecord writes the next revision while the current one is
// expectedRevision.
//
// Two guards, one per way a write can be stale. The WHERE clause refuses a
// writer whose expected revision is not the current one — it read a ceiling
// that has since been replaced. The primary key refuses the race the WHERE
// clause cannot see: two writers that both read revision N both insert N+1, and
// exactly one row can hold it. Either way the caller is told to re-read, and
// nothing is layered on a ceiling it never saw.
func (s *PostgresStore) AppendModuleEnvelopeRecord(
	ctx context.Context, record *business.ModuleEnvelopeRecord, expectedRevision uint64,
) error {
	if record.Revision != expectedRevision+1 {
		return fmt.Errorf("%w: revision %d does not follow %d", business.ErrModuleEnvelopeConflict, record.Revision, expectedRevision)
	}
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO public.module_envelopes (revision, document, written_by)
		SELECT $1, $2::jsonb, $3::uuid
		 WHERE COALESCE((SELECT max(revision) FROM public.module_envelopes), 0) = $4
		RETURNING written_at`,
		int64(record.Revision), string(record.Document), record.WrittenBy, int64(expectedRevision), //nolint:gosec // revisions are small positive counters
	).Scan(&record.WrittenAt)
	if errors.Is(err, pgx.ErrNoRows) || isUniqueViolation(err) {
		return fmt.Errorf("%w: revision %d is no longer the current one", business.ErrModuleEnvelopeConflict, expectedRevision)
	}
	return err
}

// LatestModuleEnvelopeRevision is the highest revision written, 0 when none —
// the fence every capability check reads, so it reads one integer.
func (s *PostgresStore) LatestModuleEnvelopeRevision(ctx context.Context) (uint64, error) {
	var revision int64
	if err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT COALESCE(max(revision), 0) FROM public.module_envelopes`).Scan(&revision); err != nil {
		return 0, err
	}
	return uint64(revision), nil //nolint:gosec // the column is CHECKed positive
}

var _ business.ModuleEnvelopeStore = (*PostgresStore)(nil)
