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

const solutionDeliveryColumns = `kind, document_id, generation, content_hash,
	payload, bundle, signer_identity, ownership_domain, received_at`

// RecordDeliveredDocument inserts one received carrier, and reports whether the
// row was new.
//
// Idempotency is the whole of this method. The unique index on
// (kind, document_id, generation) means a second delivery at the same generation
// collides, and the collision has two meanings that must NOT be conflated:
//
//   - the same bytes again — a retry, or a re-sync of an unchanged tree. Writes
//     nothing, reports not-inserted, and the caller answers 200.
//   - DIFFERENT bytes at a generation already delivered — a rewrite of a
//     delivered generation, which publish never produces. Refused as
//     ErrSolutionDeliveryRewritten, and the caller answers 409 so an operator
//     sees it as the hand-edited delivery tree it is.
//
// `ON CONFLICT DO NOTHING` alone could not tell those apart: it reports no row
// written for both, and the second case would then read as a successful replay
// while the host kept serving different bytes than delivery believes it sent.
// So the conflict is resolved by reading the stored content hash back.
func (s *PostgresStore) RecordDeliveredDocument(
	ctx context.Context, record *business.SolutionDeliveryRecord,
) (bool, error) {
	executor := s.getQueryExecutor(ctx)

	var storedHash string
	var storedPayload []byte
	err := executor.QueryRow(ctx, `
		INSERT INTO public.solution_delivery_documents (`+solutionDeliveryColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (kind, document_id, generation) DO NOTHING
		RETURNING content_hash, payload`,
		string(record.Kind), record.DocumentID, int64(record.Generation), record.ContentHash,
		record.Payload, record.Bundle, record.SignerIdentity, record.OwnershipDomain, record.ReceivedAt,
	).Scan(&storedHash, &storedPayload)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	// The insert was a no-op, so this generation already exists. Which of the
	// two meanings it has depends on the bytes.
	if err := executor.QueryRow(ctx, `
		SELECT content_hash, payload FROM public.solution_delivery_documents
		 WHERE kind = $1 AND document_id = $2 AND generation = $3`,
		string(record.Kind), record.DocumentID, int64(record.Generation),
	).Scan(&storedHash, &storedPayload); err != nil {
		return false, err
	}
	// The hash is compared AND the bytes are, rather than the hash alone. The
	// hash is what the schema constrains and what an index could be built on,
	// but it is also a value a writer supplies: comparing the payload as well
	// means a collision cannot be talked past by sending a matching hash with
	// different content.
	if storedHash == record.ContentHash && bytes.Equal(storedPayload, record.Payload) {
		return false, nil
	}
	return false, fmt.Errorf("%w: %s %s generation %d was delivered as %s and is now offered as %s",
		business.ErrSolutionDeliveryRewritten,
		record.Kind, record.DocumentID, record.Generation, storedHash, record.ContentHash)
}

// ListNewestDeliveredDocuments returns the newest generation per document for
// one kind: the desired set.
//
// DISTINCT ON over (document_id) ordered by generation DESC, which is the
// newest-per-key read the index `solution_delivery_documents_newest` exists for.
// Superseded generations stay in the table — they are the evidence of what was
// delivered — and are deliberately not returned: the desired set is what
// delivery wants NOW, and handing the reconciler every generation would make it
// judge a set containing its own history.
func (s *PostgresStore) ListNewestDeliveredDocuments(
	ctx context.Context, kind business.SolutionDeliveryKind,
) ([]*business.SolutionDeliveryRecord, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT DISTINCT ON (document_id) `+solutionDeliveryColumns+`
		  FROM public.solution_delivery_documents
		 WHERE kind = $1
		 ORDER BY document_id, generation DESC`, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []*business.SolutionDeliveryRecord{}
	for rows.Next() {
		var (
			record     business.SolutionDeliveryRecord
			kindText   string
			generation int64
		)
		if err := rows.Scan(&kindText, &record.DocumentID, &generation, &record.ContentHash,
			&record.Payload, &record.Bundle, &record.SignerIdentity, &record.OwnershipDomain,
			&record.ReceivedAt); err != nil {
			return nil, err
		}
		record.Kind = business.SolutionDeliveryKind(kindText)
		record.Generation = uint64(generation)
		records = append(records, &record)
	}
	return records, rows.Err()
}

var _ business.SolutionDeliveryStore = (*PostgresStore)(nil)
