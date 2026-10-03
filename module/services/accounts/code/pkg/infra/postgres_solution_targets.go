package infra

import (
	"context"
	"errors"
	"strings"
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

// RevokeInstallationsOfTarget ends every active installation of a target the
// reconcile pass has just closed, and returns the rows it flipped.
//
// It runs on the control-plane connection, in the same transaction as the close.
// That is not an optimisation: a withdrawal crosses every organisation that
// installed the target, so there is no single tenant transaction this could run
// in, and leaving it to a later sweep would mean a window in which an
// installation records consent to a presence that has ended.
//
// `installations` FORCEs row-level security and its policies admit the
// control-plane role, so this UPDATE matches rows without lifting anything — the
// relation is reached the same way every other control-plane installation write
// reaches it.
//
// The status/revoked_at consistency CHECK is satisfied by setting both, and the
// `installations_target_immutable` trigger is untouched because target_id is not
// in the SET list: the row keeps naming the identity whose consent ended, which
// is what makes the revocation attributable.
func (s *PostgresStore) RevokeInstallationsOfTarget(
	ctx context.Context, targetID, reason string, now time.Time,
) ([]business.RevokedInstallation, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		UPDATE public.installations
		   SET status = 'revoked', revoked_at = $3, revoked_reason = $2
		 WHERE target_id = $1::uuid AND status = 'active'
		RETURNING id::text, org_id::text`, targetID, reason, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revoked := []business.RevokedInstallation{}
	for rows.Next() {
		var entry business.RevokedInstallation
		if err := rows.Scan(&entry.InstallationID, &entry.OrgID); err != nil {
			return nil, err
		}
		revoked = append(revoked, entry)
	}
	return revoked, rows.Err()
}

// ListAvailableSolutionTargets is the catalogue: live targets whose binding's
// newest APPLIED generation is a present one.
//
// The acceptance predicate is the join itself, which is why it is one statement:
// a target is offered only when `solution_host_bindings` holds an applied
// generation for its binding that is NOT a tombstone. A binding whose newest
// DESIRED generation was refused still has its previous applied generation, so it
// stays offered at the release this host actually admitted — which is the correct
// answer and the one a diagnostic listing would get wrong by reporting the
// refused desired generation beside it.
//
// `installed` is resolved in the same statement rather than by a second query per
// row, for the reason the installation listing resolves health inline: a
// catalogue of n rows must not cost n+1 round trips.
//
// Control-plane: it reads presence state, which request traffic holds no grant
// on. The org is a parameter, never the session's — the caller is an org admin
// whose organisation the gateway projected from a verified identity.
func (s *PostgresStore) ListAvailableSolutionTargets(
	ctx context.Context, query business.AvailableSolutionQuery,
) ([]*business.AvailableSolutionTarget, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT t.id::text, t.binding_id, t.solution_id,
		       b.applied_release, t.opened_generation, b.applied_generation,
		       ($1 <> '' AND EXISTS (
		           SELECT 1 FROM public.installations i
		            WHERE i.target_id = t.id AND i.org_id = $1::uuid AND i.status = 'active'
		       ))
		  FROM public.solution_targets t
		  JOIN public.solution_host_bindings b ON b.binding_id = t.binding_id
		 WHERE t.closed_generation IS NULL
		   AND b.applied_generation IS NOT NULL
		   AND b.applied_removed = FALSE
		   AND ($4 = '' OR t.id::text = $4)
		   AND ($2 = '' OR t.id::text > $2)
		 ORDER BY t.id
		 LIMIT $3`, query.OrgID, query.Cursor, limit, query.TargetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	available := []*business.AvailableSolutionTarget{}
	for rows.Next() {
		var (
			entry             business.AvailableSolutionTarget
			release           *string
			openedGeneration  int64
			appliedGeneration int64
		)
		if err := rows.Scan(&entry.TargetID, &entry.BindingID, &entry.RouteAlias,
			&release, &openedGeneration, &appliedGeneration, &entry.Installed); err != nil {
			return nil, err
		}
		entry.OpenedGeneration = uint64(openedGeneration)
		entry.AppliedGeneration = uint64(appliedGeneration)
		entry.ReleasePublisher, entry.ReleaseName, entry.ReleaseVersion = splitReleaseIdentity(release)
		available = append(available, &entry)
	}
	return available, rows.Err()
}

// splitReleaseIdentity reverses core's Release.Identity() —
// `publisher/name@version` — whose publisher and name are single path segments,
// so the joined string maps back to exactly one of each.
//
// A release that does not match the shape yields three empty strings rather than
// a partial guess: a catalogue row showing half a release version is worse than
// one showing none, because only the second is visibly wrong.
func splitReleaseIdentity(identity *string) (publisher, name, version string) {
	if identity == nil {
		return "", "", ""
	}
	owner, version, found := strings.Cut(*identity, "@")
	if !found {
		return "", "", ""
	}
	publisher, name, found = strings.Cut(owner, "/")
	if !found {
		return "", "", ""
	}
	return publisher, name, version
}
