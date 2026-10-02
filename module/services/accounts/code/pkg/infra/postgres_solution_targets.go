package infra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// solution_targets is a control-plane-owned platform relation (no tenant column,
// no RLS), written only by the reconcile pass. Every method assumes the caller
// opened a WithControlPlane transaction, which getQueryExecutor picks up from ctx.

const solutionTargetColumns = `id::text, binding_id, solution_id,
	opened_generation, closed_generation, opened_at, closed_at`

func scanSolutionTarget(row pgx.Row) (*business.SolutionTarget, error) {
	var (
		target           business.SolutionTarget
		openedGeneration int64
		closedGeneration *int64
		closedAt         *time.Time
	)
	if err := row.Scan(&target.ID, &target.BindingID, &target.SolutionID,
		&openedGeneration, &closedGeneration, &target.OpenedAt, &closedAt); err != nil {
		return nil, err
	}
	target.OpenedGeneration = uint64(openedGeneration)
	if closedGeneration != nil {
		generation := uint64(*closedGeneration)
		target.ClosedGeneration = &generation
	}
	target.ClosedAt = closedAt
	return &target, nil
}

// GetLiveSolutionTargetForUpdate reads and row-locks the open target for a
// binding, returning nil when the binding has none — which is both "never
// present" and "withdrawn", deliberately: a withdrawn binding has no live target
// to inherit, and that is the whole point of the record.
func (s *PostgresStore) GetLiveSolutionTargetForUpdate(
	ctx context.Context, bindingID string,
) (*business.SolutionTarget, error) {
	target, err := scanSolutionTarget(s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+solutionTargetColumns+`
		 FROM public.solution_targets
		 WHERE binding_id = $1 AND closed_generation IS NULL
		 FOR UPDATE`, bindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return target, nil
}

// OpenSolutionTarget mints a new target. The partial unique indexes on
// (binding_id) and (solution_id) where the target is open are what make this
// safe for two replicas reconciling one pass: the second gets a unique violation
// rather than a second identity for the same presence.
func (s *PostgresStore) OpenSolutionTarget(
	ctx context.Context, bindingID, solutionID string, generation uint64, now time.Time,
) (*business.SolutionTarget, error) {
	return scanSolutionTarget(s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO public.solution_targets
			(binding_id, solution_id, opened_generation, opened_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4, $4)
		RETURNING `+solutionTargetColumns,
		bindingID, solutionID, int64(generation), now))
}

// RetargetSolutionTarget records that the applied generation moved this target's
// route alias. The identity does not change — that is the invariant this whole
// record exists for — so only the alias and the audit columns move.
func (s *PostgresStore) RetargetSolutionTarget(
	ctx context.Context, targetID, solutionID string, now time.Time,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE public.solution_targets
		   SET solution_id = $2, updated_at = $3
		 WHERE id = $1::uuid AND closed_generation IS NULL`, targetID, solutionID, now)
	return err
}

// CloseSolutionTarget ends a target at the tombstone generation that withdrew it.
// The row survives: a closed target is the evidence that an installation's
// consent ended, and without it a reused alias is indistinguishable from a
// continuous presence.
func (s *PostgresStore) CloseSolutionTarget(
	ctx context.Context, targetID string, generation uint64, now time.Time,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE public.solution_targets
		   SET closed_generation = $2, closed_at = $3, updated_at = $3
		 WHERE id = $1::uuid AND closed_generation IS NULL`, targetID, int64(generation), now)
	return err
}

// ListSolutionTargets returns every target, open and closed, ordered so a
// binding's periods read in sequence.
func (s *PostgresStore) ListSolutionTargets(ctx context.Context) ([]*business.SolutionTarget, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+solutionTargetColumns+`
		 FROM public.solution_targets ORDER BY binding_id, opened_generation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []*business.SolutionTarget{}
	for rows.Next() {
		target, err := scanSolutionTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
