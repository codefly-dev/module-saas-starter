package infra

import (
	"context"
	"fmt"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/solutionhost"
)

// ReadAppliedWorkloadPresence reads immutable applied decisions and their exact
// inbox bytes. Stream history without the catalogue's diagnostic row limit, so
// later headless generations cannot erase a carrier's original namespace.
func (s *PostgresStore) ReadAppliedWorkloadPresence(ctx context.Context, binding string, through uint64) (*business.SolutionHostBindingGeneration, error) {
	if through == 0 || through > uint64(1<<63-1) {
		return nil, fmt.Errorf("applied generation is outside the stored range")
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
 SELECT h.generation, h.digest, d.payload, h.decided_at
 FROM public.solution_generation_history h
 LEFT JOIN public.solution_delivery_documents d ON d.kind = 'presence'
   AND d.document_id = h.binding_id AND d.generation = h.generation AND d.content_hash = h.digest
 WHERE h.binding_id = $1 AND h.generation <= $2 AND h.decision = 'applied'
 ORDER BY h.generation DESC`, binding, int64(through))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var result business.SolutionHostBindingGeneration
		var generation int64
		var payload []byte
		if err := rows.Scan(&generation, &result.Digest, &payload, &result.At); err != nil {
			return nil, err
		}
		document, err := solutionhost.Parse(payload)
		if err != nil {
			return nil, fmt.Errorf("read applied carrier presence: %w", err)
		}
		digest, err := document.Digest()
		if err != nil || digest != result.Digest || document.Binding != binding || document.Generation != uint64(generation) || document.Removed {
			return nil, fmt.Errorf("applied carrier presence is inconsistent")
		}
		if len(document.Workloads) == 0 {
			continue
		}
		result.Generation = uint64(generation)
		result.Document = string(payload)
		return &result, nil
	}
	return nil, rows.Err()
}

var _ business.SolutionDeliveryAppliedReader = (*PostgresStore)(nil)
