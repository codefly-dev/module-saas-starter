package infra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// solution_host_bindings is a control-plane-owned platform relation (no tenant
// column, no RLS): only app_control_plane may touch it. Every method assumes the
// caller opened a WithControlPlane transaction, which getQueryExecutor picks up
// from ctx.

const solutionHostBindingColumns = `binding_id, host_coordinate, host_component,
	desired_generation, desired_digest, desired_document, desired_seen_at,
	applied_generation, applied_digest, applied_document, applied_removed, applied_routes,
	applied_solution_id, applied_release, applied_domain, applied_at,
	pending_reason, pending_since, updated_at`

func scanSolutionHostBinding(row pgx.Row) (*business.SolutionHostBindingRecord, error) {
	var (
		record            business.SolutionHostBindingRecord
		desiredGeneration *int64
		desiredDigest     *string
		desiredDocument   *string
		desiredSeenAt     *time.Time
		appliedGeneration *int64
		appliedDigest     *string
		appliedDocument   *string
		appliedRemoved    bool
		appliedRoutes     []string
		appliedSolutionID *string
		appliedRelease    *string
		appliedDomain     *string
		appliedAt         *time.Time
		pendingReason     *string
		pendingSince      *time.Time
	)
	if err := row.Scan(
		&record.BindingID, &record.HostCoordinate, &record.HostComponent,
		&desiredGeneration, &desiredDigest, &desiredDocument, &desiredSeenAt,
		&appliedGeneration, &appliedDigest, &appliedDocument, &appliedRemoved, &appliedRoutes,
		&appliedSolutionID, &appliedRelease, &appliedDomain, &appliedAt,
		&pendingReason, &pendingSince, &record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	// The whole-or-absent CHECK constraints guarantee a group's columns are all
	// set or all null, so the generation column alone decides whether it is
	// present.
	if desiredGeneration != nil {
		record.Desired = &business.SolutionHostBindingGeneration{
			Generation: uint64(*desiredGeneration),
			Digest:     *desiredDigest,
			Document:   *desiredDocument,
			At:         *desiredSeenAt,
		}
	}
	if appliedGeneration != nil {
		record.Applied = &business.SolutionHostBindingApplied{
			SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{
				Generation: uint64(*appliedGeneration),
				Digest:     *appliedDigest,
				Document:   *appliedDocument,
				At:         *appliedAt,
			},
			Removed:    appliedRemoved,
			Routes:     appliedRoutes,
			SolutionID: derefString(appliedSolutionID),
			Release:    derefString(appliedRelease),
			Domain:     derefString(appliedDomain),
		}
	}
	record.PendingReason = derefString(pendingReason)
	record.PendingSince = pendingSince
	return &record, nil
}

// GetSolutionHostBindingForUpdate reads and row-locks one binding record,
// returning nil when nothing has been delivered for it. The lock holds for the
// caller's transaction, which is what serializes two replicas applying the same
// generation.
func (s *PostgresStore) GetSolutionHostBindingForUpdate(
	ctx context.Context, bindingID string,
) (*business.SolutionHostBindingRecord, error) {
	q := s.getQueryExecutor(ctx)
	record, err := scanSolutionHostBinding(q.QueryRow(ctx,
		`SELECT `+solutionHostBindingColumns+`
		 FROM public.solution_host_bindings WHERE binding_id = $1 FOR UPDATE`, bindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

// SaveSolutionHostBinding writes the whole record. Every column is written on
// every save, so a generation that clears a pending reason clears it in the same
// statement that records the apply.
func (s *PostgresStore) SaveSolutionHostBinding(
	ctx context.Context, record *business.SolutionHostBindingRecord,
) error {
	var (
		desiredGeneration *int64
		desiredDigest     *string
		desiredDocument   *string
		desiredSeenAt     *time.Time
		appliedGeneration *int64
		appliedDigest     *string
		appliedDocument   *string
		appliedSolutionID *string
		appliedRelease    *string
		appliedDomain     *string
		appliedAt         *time.Time
		pendingReason     *string
	)
	appliedRoutes := []string{}
	appliedRemoved := false
	if desired := record.Desired; desired != nil {
		generation := int64(desired.Generation)
		desiredGeneration, desiredDigest = &generation, &desired.Digest
		desiredDocument, desiredSeenAt = &desired.Document, &desired.At
	}
	if applied := record.Applied; applied != nil {
		generation := int64(applied.Generation)
		appliedGeneration, appliedDigest = &generation, &applied.Digest
		appliedDocument, appliedAt = &applied.Document, &applied.At
		appliedSolutionID, appliedRelease = &applied.SolutionID, &applied.Release
		appliedDomain = &applied.Domain
		appliedRemoved = applied.Removed
		if applied.Routes != nil {
			appliedRoutes = applied.Routes
		}
	}
	if record.PendingReason != "" {
		pendingReason = &record.PendingReason
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO public.solution_host_bindings (
			binding_id, host_coordinate, host_component,
			desired_generation, desired_digest, desired_document, desired_seen_at,
			applied_generation, applied_digest, applied_document, applied_removed, applied_routes,
			applied_solution_id, applied_release, applied_domain, applied_at,
			pending_reason, pending_since, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (binding_id) DO UPDATE SET
			host_coordinate = EXCLUDED.host_coordinate,
			host_component = EXCLUDED.host_component,
			desired_generation = EXCLUDED.desired_generation,
			desired_digest = EXCLUDED.desired_digest,
			desired_document = EXCLUDED.desired_document,
			desired_seen_at = EXCLUDED.desired_seen_at,
			applied_generation = EXCLUDED.applied_generation,
			applied_digest = EXCLUDED.applied_digest,
			applied_document = EXCLUDED.applied_document,
			applied_removed = EXCLUDED.applied_removed,
			applied_routes = EXCLUDED.applied_routes,
			applied_solution_id = EXCLUDED.applied_solution_id,
			applied_release = EXCLUDED.applied_release,
			applied_domain = EXCLUDED.applied_domain,
			applied_at = EXCLUDED.applied_at,
			pending_reason = EXCLUDED.pending_reason,
			pending_since = EXCLUDED.pending_since,
			updated_at = EXCLUDED.updated_at`,
		record.BindingID, record.HostCoordinate, record.HostComponent,
		desiredGeneration, desiredDigest, desiredDocument, desiredSeenAt,
		appliedGeneration, appliedDigest, appliedDocument, appliedRemoved, appliedRoutes,
		appliedSolutionID, appliedRelease, appliedDomain, appliedAt,
		pendingReason, record.PendingSince, record.UpdatedAt)
	return err
}

// ListSolutionHostBindings returns every binding record ordered by binding ID.
// The reconciler reads the whole set on every pass: a host holds tens of
// bindings at most, and core judges the desired set as a whole.
func (s *PostgresStore) ListSolutionHostBindings(
	ctx context.Context,
) ([]*business.SolutionHostBindingRecord, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+solutionHostBindingColumns+`
		 FROM public.solution_host_bindings ORDER BY binding_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []*business.SolutionHostBindingRecord{}
	for rows.Next() {
		record, err := scanSolutionHostBinding(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
