package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// The row-backed half of a Work Context's seal (business/work_context_seal.go).
//
// Every read runs on the control plane, at one read-only snapshot, because a
// verifier answers it for a capability before any request identity exists — the
// capability IS the identity being established. Each statement filters on the
// organization and principal explicitly, as WithControlPlane requires of every
// caller: nothing here leans on a row policy to narrow it.

func (s *PostgresStore) sealRead(ctx context.Context, read func(context.Context, QueryExecutor) error) error {
	return s.WithControlPlaneReadSnapshot(ctx, func(ctx context.Context) error {
		return read(ctx, s.getQueryExecutor(ctx))
	})
}

func (s *PostgresStore) WorkContextHeldInstallation(
	ctx context.Context, orgID, principalID, installationID string,
) (uint64, error) {
	var revision int64
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		return q.QueryRow(ctx, `
			SELECT i.revision
			  FROM public.installations i
			 WHERE i.org_id = $1::uuid
			   AND i.id::text = $3
			   -- A member of the organization holds its installations that are
			   -- active AND the ones still pending: an installation is pending
			   -- until its install step completes, and that step is the one
			   -- capability minted to a pending installation — sealing it is
			   -- what lets the step be delivered at all. A failed or
			   -- revoked installation is held by nobody.
			   AND i.status IN ('active', 'pending')
			   AND EXISTS (SELECT 1 FROM public.organization_members m
			                WHERE m.org_id = i.org_id AND m.user_id::text = $2)`,
			orgID, principalID, installationID,
		).Scan(&revision)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, business.ErrWorkContextSealUnheld
	}
	if err != nil {
		return 0, fmt.Errorf("read held installation: %w", err)
	}
	return uint64(revision), nil
}

func (s *PostgresStore) WorkContextPrincipalEpoch(ctx context.Context, orgID, principalID string) (uint64, error) {
	var revision int64
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		return q.QueryRow(ctx, `
			SELECT revision FROM public.principal_authorization_revisions
			 WHERE org_id = $1::uuid AND principal_id::text = $2`,
			orgID, principalID,
		).Scan(&revision)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, business.ErrWorkContextPrincipalUnknown
	}
	if err != nil {
		return 0, fmt.Errorf("read principal epoch: %w", err)
	}
	return uint64(revision), nil
}

func (s *PostgresStore) WorkContextOwnerRevision(ctx context.Context, orgID, ownerID string) (uint64, error) {
	var revision int64
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		// The same pair resolveWorkContextAuthority reads and EffectiveRevision
		// combines, so a capability minted from resolved facts and one checked
		// here agree on what "current" means.
		return q.QueryRow(ctx, `
			SELECT GREATEST(o.revision, p.revision)
			  FROM public.organization_authorization_revisions o
			  JOIN public.principal_authorization_revisions p
			    ON p.org_id = o.org_id AND p.principal_id::text = $2
			  JOIN public.organization_members m
			    ON m.org_id = o.org_id AND m.user_id::text = $2
			 WHERE o.org_id = $1::uuid`,
			orgID, ownerID,
		).Scan(&revision)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, business.ErrWorkContextPrincipalUnknown
	}
	if err != nil {
		return 0, fmt.Errorf("read owner revision: %w", err)
	}
	if revision <= 0 {
		return 0, errors.New("authorization revision must be positive")
	}
	return uint64(revision), nil
}

func (s *PostgresStore) WorkContextSolutionInstallation(ctx context.Context, orgID, bindingID string) (string, error) {
	var id string
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		return q.QueryRow(ctx, `
			SELECT i.id::text
			  FROM public.installations i
			  JOIN public.solution_targets t ON t.id = i.target_id AND t.binding_id = i.registry_binding_id
			 WHERE i.org_id = $1::uuid
			   AND i.registry_binding_id = $2
			   AND i.status = 'active'
			   AND t.closed_generation IS NULL`,
			orgID, bindingID,
		).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: solution binding %s", business.ErrWorkContextNoAudienceInstallation, bindingID)
	}
	if err != nil {
		return "", fmt.Errorf("read solution installation: %w", err)
	}
	return id, nil
}

var _ business.WorkContextSealStore = (*PostgresStore)(nil)

// WorkContextOrgMember answers, on the control-plane snapshot the other seal
// reads use, whether a principal is a member of the organisation: the question a
// composed module's installation asks, since a member holds every one of them.
func (s *PostgresStore) WorkContextOrgMember(ctx context.Context, orgID, principalID string) (bool, error) {
	var member bool
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		return q.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM public.organization_members m
			                WHERE m.org_id = $1::uuid AND m.user_id::text = $2)`,
			orgID, principalID,
		).Scan(&member)
	})
	if err != nil {
		return false, fmt.Errorf("read membership: %w", err)
	}
	return member, nil
}
