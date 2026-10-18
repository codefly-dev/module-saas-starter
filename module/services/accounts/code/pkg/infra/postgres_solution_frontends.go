package infra

import (
	"accounts/pkg/business"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Read one indexed applied identity; keep the last ten and every generation
// within the lazy-load window. Never ship that unbounded history in a snapshot.
func (s *PostgresStore) GetRetainedSolutionFrontend(ctx context.Context, binding, version string, since time.Time) (*business.SolutionFrontendGeneration, error) {
	var generation business.SolutionFrontendGeneration
	var number int64
	var document []byte
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
 SELECT h.generation, h.digest, d.payload, h.decided_at, h.target_id::text
 FROM public.solution_generation_history h
 JOIN public.solution_targets t ON t.id = h.target_id AND t.binding_id = h.binding_id
 JOIN public.solution_delivery_documents d ON d.kind = 'manifest'
   AND d.document_id = h.binding_id AND d.generation = h.generation AND d.content_hash = h.digest
 WHERE h.binding_id = $1 AND h.asset_version = $2 AND h.decision = 'applied'
   AND t.closed_generation IS NULL
   AND (h.decided_at >= $3 OR NOT EXISTS (
     SELECT 1 FROM public.solution_generation_history newer
     WHERE newer.binding_id = h.binding_id AND newer.target_id = h.target_id
       AND newer.decision = 'applied' AND newer.generation > h.generation
     ORDER BY newer.generation DESC OFFSET 9 LIMIT 1))`, binding, version, since).
		Scan(&number, &generation.Digest, &document, &generation.At, &generation.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	generation.Generation = uint64(number)
	generation.Document = string(document)
	return &generation, nil
}

var _ business.SolutionFrontendHistory = (*PostgresStore)(nil)
