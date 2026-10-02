package infra

import (
	"context"
	"time"

	"accounts/pkg/business"
)

// solution_generation_history is a control-plane-owned platform relation, and the
// only one in this service the control plane may INSERT but not UPDATE: a
// decision is a fact about the past, and a trail whose rows can be edited is not
// a trail. Every method assumes a WithControlPlane transaction.

const solutionGenerationHistoryColumns = `binding_id, target_id::text, generation, digest,
	decision, reason, release, registry_revision, decided_at`

// RecordSolutionGenerationDecision appends one decision.
//
// ON CONFLICT DO NOTHING against the unique index is what makes a polling
// reconciler safe: re-reading an unchanged document decides `current` at the same
// generation and digest every pass, and appending that per pass would turn a
// thirty-second poll into the event volume this trail exists to make readable. A
// genuinely new decision about the same generation differs in digest (a rewrite)
// or in decision (refused, then applied), so it still records.
func (s *PostgresStore) RecordSolutionGenerationDecision(
	ctx context.Context, decision *business.SolutionGenerationDecision,
) error {
	var targetID, reason, release *string
	if decision.TargetID != "" {
		targetID = &decision.TargetID
	}
	if decision.Reason != "" {
		reason = &decision.Reason
	}
	if decision.Release != "" {
		release = &decision.Release
	}
	decidedAt := decision.DecidedAt
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO public.solution_generation_history
			(binding_id, target_id, generation, digest, decision, reason, release,
			 registry_revision, decided_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (binding_id, generation, digest, decision) DO NOTHING`,
		decision.BindingID, targetID, int64(decision.Generation), decision.Digest,
		decision.Decision, reason, release, decision.RegistryRevision, decidedAt)
	return err
}

// ListSolutionGenerationHistory returns one component's decisions, newest first.
func (s *PostgresStore) ListSolutionGenerationHistory(
	ctx context.Context, bindingID string, limit int,
) ([]*business.SolutionGenerationDecision, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+solutionGenerationHistoryColumns+`
		 FROM public.solution_generation_history
		 WHERE binding_id = $1
		 ORDER BY decided_at DESC, generation DESC
		 LIMIT $2`, bindingID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []*business.SolutionGenerationDecision{}
	for rows.Next() {
		var (
			decision         business.SolutionGenerationDecision
			targetID, reason *string
			release          *string
			generation       int64
		)
		if err := rows.Scan(&decision.BindingID, &targetID, &generation, &decision.Digest,
			&decision.Decision, &reason, &release, &decision.RegistryRevision,
			&decision.DecidedAt); err != nil {
			return nil, err
		}
		decision.Generation = uint64(generation)
		decision.TargetID = derefString(targetID)
		decision.Reason = derefString(reason)
		decision.Release = derefString(release)
		history = append(history, &decision)
	}
	return history, rows.Err()
}
