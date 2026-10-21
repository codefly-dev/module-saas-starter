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
// been written. A delivered revision reads with an empty WrittenBy.
func (s *PostgresStore) LatestModuleEnvelopeRecord(ctx context.Context) (*business.ModuleEnvelopeRecord, error) {
	return s.latestModuleEnvelopeRecord(ctx, "")
}

// LatestDeliveredModuleEnvelopeRecord returns the highest DELIVERED revision,
// or nil when none has been delivered.
func (s *PostgresStore) LatestDeliveredModuleEnvelopeRecord(ctx context.Context) (*business.ModuleEnvelopeRecord, error) {
	return s.latestModuleEnvelopeRecord(ctx, "WHERE written_by IS NULL")
}

func (s *PostgresStore) latestModuleEnvelopeRecord(ctx context.Context, where string) (*business.ModuleEnvelopeRecord, error) {
	var (
		record   business.ModuleEnvelopeRecord
		revision int64
		envelope *string
	)
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT revision, document::text, COALESCE(written_by::text, ''), written_at,
		       COALESCE(signer, ''), COALESCE(ownership_domain, ''), delivered_payload, delivered_envelope::text
		  FROM public.module_envelopes `+where+`
		 ORDER BY revision DESC
		 LIMIT 1`).Scan(&revision, &record.Document, &record.WrittenBy, &record.WrittenAt,
		&record.Signer, &record.OwnershipDomain, &record.Payload, &envelope)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.Revision = uint64(revision) //nolint:gosec // the column is CHECKed positive
	if envelope != nil {
		record.Envelope = []byte(*envelope)
	}
	return &record, nil
}

// AppendModuleEnvelopeRecord writes a revision above expectedRevision while the
// current one is expectedRevision: an administrator writes the next one, a
// delivery the one its composition declared. A row is a user's or a delivery's,
// never both (module_envelopes_writer_check).
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
	if record.Revision <= expectedRevision {
		return fmt.Errorf("%w: revision %d is not above %d", business.ErrModuleEnvelopeConflict, record.Revision, expectedRevision)
	}
	var envelope *string
	if record.Envelope != nil {
		encoded := string(record.Envelope)
		envelope = &encoded
	}
	if _, err := s.getQueryExecutor(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('module-envelope-installation',0))`); err != nil {
		return err
	}
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO public.module_envelopes
		       (revision, document, written_by, signer, ownership_domain, delivered_payload, delivered_envelope)
		SELECT $1, $2::jsonb, NULLIF($3, '')::uuid, NULLIF($5, ''), NULLIF($6, ''), $7, $8::jsonb
		 WHERE COALESCE((SELECT max(revision) FROM public.module_envelopes), 0) = $4
		RETURNING written_at`,
		int64(record.Revision), string(record.Document), record.WrittenBy, int64(expectedRevision), //nolint:gosec // revisions are small positive counters
		record.Signer, record.OwnershipDomain, record.Payload, envelope,
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

func (s *PostgresStore) ModuleInstallations(ctx context.Context) ([]business.ModuleInstallationConsent, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `SELECT id::text, org_id::text, registry_principal_id::text
        FROM public.installations WHERE status='active' AND registry_principal_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.ModuleInstallationConsent
	for rows.Next() {
		var row business.ModuleInstallationConsent
		if err := rows.Scan(&row.ID, &row.OrgID, &row.PrincipalID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ModuleEnvelopeHistory reads all immutable ceilings through the snapshot. A
// remove/re-add between reconciles remains visible to the lifetime projection.
func (s *PostgresStore) ModuleEnvelopeHistory(ctx context.Context, through uint64) ([]*business.ModuleEnvelopeRecord, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `SELECT revision, document::text FROM public.module_envelopes WHERE revision <= $1 ORDER BY revision`, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*business.ModuleEnvelopeRecord
	for rows.Next() {
		record := &business.ModuleEnvelopeRecord{}
		if err := rows.Scan(&record.Revision, &record.Document); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
