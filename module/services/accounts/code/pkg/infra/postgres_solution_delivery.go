package infra

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// The delivery inbox's persistence.
//
// solution_delivery_documents is a control-plane-owned platform relation (no
// tenant column, no RLS), and it is granted SELECT and INSERT only — no UPDATE
// and no DELETE. An inbox that can be edited is not a record of what was
// received, so a correction is a new generation rather than a rewrite. Every
// method assumes the caller opened a WithControlPlane transaction.
//
// It holds one kind of document, the module manifest (migration 41): the row's
// document_id is the manifest's binding.

// solutionDeliveryManifestKind is the one kind the inbox holds.
const solutionDeliveryManifestKind = "manifest"

const solutionDeliveryColumns = `document_id, generation, content_hash,
	payload, bundle, signer_identity, ownership_domain, received_at`

// RecordDeliveredDocument inserts one received carrier, and reports whether the
// row was new.
//
// The unique index on (kind, document_id, generation) means a second delivery
// at the same generation collides, and the collision has two meanings that
// must NOT be conflated:
//
//   - the same bytes again — a retry, or a re-sync of an unchanged tree. Writes
//     nothing and reports not-inserted.
//   - DIFFERENT bytes at a generation already delivered — a rewrite, which
//     publish never produces. Refused as ErrSolutionDeliveryConflict.
//
// `ON CONFLICT DO NOTHING` alone could not tell those apart, so the stored row
// is read back and compared.
func (s *PostgresStore) RecordDeliveredDocument(
	ctx context.Context, record *business.SolutionDeliveryRecord,
) (bool, error) {
	executor := s.getQueryExecutor(ctx)

	var storedHash string
	var storedPayload []byte
	err := executor.QueryRow(ctx, `
		INSERT INTO public.solution_delivery_documents (kind, `+solutionDeliveryColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (kind, document_id, generation) DO NOTHING
		RETURNING content_hash, payload`,
		solutionDeliveryManifestKind, record.Binding, int64(record.Generation), record.ContentHash, //nolint:gosec // a manifest generation is a small positive counter
		record.Payload, record.Envelope, record.SignerIdentity, record.OwnershipDomain, record.ReceivedAt,
	).Scan(&storedHash, &storedPayload)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err := executor.QueryRow(ctx, `
		SELECT content_hash, payload FROM public.solution_delivery_documents
		 WHERE kind = $1 AND document_id = $2 AND generation = $3`,
		solutionDeliveryManifestKind, record.Binding, int64(record.Generation), //nolint:gosec // a manifest generation is a small positive counter
	).Scan(&storedHash, &storedPayload); err != nil {
		return false, err
	}
	if storedHash == record.ContentHash && bytes.Equal(storedPayload, record.Payload) {
		return false, nil
	}
	return false, fmt.Errorf("%w: binding %s generation %d was delivered as %s and is now offered as %s",
		business.ErrSolutionDeliveryConflict, record.Binding, record.Generation, storedHash, record.ContentHash)
}

// ListNewestDeliveredDocuments returns the newest delivered generation per
// binding: the desired set.
func (s *PostgresStore) ListNewestDeliveredDocuments(ctx context.Context) ([]*business.SolutionDeliveryRecord, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT DISTINCT ON (document_id) `+solutionDeliveryColumns+`
		  FROM public.solution_delivery_documents
		 WHERE kind = $1
		 ORDER BY document_id, generation DESC`, solutionDeliveryManifestKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []*business.SolutionDeliveryRecord{}
	for rows.Next() {
		record, err := scanSolutionDelivery(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ReadDeliveredDocument reads the exact inbox generation selected by applied
// state, or nil. Absence is not permission to use a newer or older delivery.
func (s *PostgresStore) ReadDeliveredDocument(
	ctx context.Context, binding string, generation uint64,
) (*business.SolutionDeliveryRecord, error) {
	if generation == 0 || generation > uint64(1<<63-1) {
		return nil, fmt.Errorf("delivery generation is outside the stored range")
	}
	record, err := scanSolutionDelivery(s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT `+solutionDeliveryColumns+` FROM public.solution_delivery_documents
		 WHERE kind = $1 AND document_id = $2 AND generation = $3`,
		solutionDeliveryManifestKind, binding, int64(generation))) //nolint:gosec // range CHECKed above
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func scanSolutionDelivery(row pgx.Row) (*business.SolutionDeliveryRecord, error) {
	var (
		record     business.SolutionDeliveryRecord
		generation int64
	)
	if err := row.Scan(&record.Binding, &generation, &record.ContentHash,
		&record.Payload, &record.Envelope, &record.SignerIdentity, &record.OwnershipDomain,
		&record.ReceivedAt); err != nil {
		return nil, err
	}
	record.Generation = uint64(generation) //nolint:gosec // CHECKed positive
	return &record, nil
}

var _ business.SolutionDeliveryStore = (*PostgresStore)(nil)

// CountDeliveredDocuments is how many manifests the inbox holds.
//
// The inbox is append-only — a generation is inserted once and never rewritten
// or removed — so the count only grows, and it grows exactly when a manifest a
// registry pass has not read yet arrives. That makes it the marker a registry
// snapshot is compared with: see business.Service.deliveredSince.
func (s *PostgresStore) CountDeliveredDocuments(ctx context.Context) (uint64, error) {
	var count int64
	if err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM public.solution_delivery_documents`).Scan(&count); err != nil {
		return 0, err
	}
	return uint64(count), nil //nolint:gosec // a count is non-negative
}

// CountUnappliedManifests is how many bindings hold a delivered manifest newer
// than the generation they applied, that the host has not refused. Each is a
// declaration the reconciler has yet to apply: until it has, the registry
// cannot hold the principal it declares.
func (s *PostgresStore) CountUnappliedManifests(ctx context.Context) (uint64, error) {
	var count int64
	if err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT count(*)
		  FROM (SELECT document_id, max(generation) AS generation
		          FROM public.solution_delivery_documents
		         WHERE kind = $1
		         GROUP BY document_id) newest
		  LEFT JOIN public.solution_host_bindings binding ON binding.binding_id = newest.document_id
		 WHERE COALESCE(binding.applied_generation, 0) < newest.generation
		   AND binding.pending_reason IS NULL`,
		solutionDeliveryManifestKind).Scan(&count); err != nil {
		return 0, err
	}
	return uint64(count), nil //nolint:gosec // a count is non-negative
}

var _ business.SolutionDeliveryCounter = (*PostgresStore)(nil)
