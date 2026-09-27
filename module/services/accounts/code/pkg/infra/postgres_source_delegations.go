package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// Source delegations (migration 9). The tenant-facing statements run inside the
// organization's transaction, so its policy scopes them; the module-facing ones
// run under the control plane, whose policy does not. Every statement therefore
// names the organization (or, for a lookup by a globally unique id, returns it
// for the caller to compare) rather than trusting the policy to.

const sourceDelegationColumns = `id::text, org_id::text, source_id::text, principal_id::text, module_prefix,
	binding_id, binding_digest, created_at, revoked_at, COALESCE(revoked_reason, ''), COALESCE(revoked_by::text, '')`

func scanSourceDelegations(rows pgx.Rows) ([]*business.SourceDelegation, error) {
	defer rows.Close()
	var out []*business.SourceDelegation
	for rows.Next() {
		var d business.SourceDelegation
		var revokedAt *time.Time
		if err := rows.Scan(&d.ID, &d.OrgID, &d.SourceID, &d.PrincipalID, &d.ModulePrefix,
			&d.BindingID, &d.BindingDigest, &d.CreatedAt, &revokedAt, &d.RevokedReason, &d.RevokedBy); err != nil {
			return nil, err
		}
		d.RevokedAt = revokedAt
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) InsertSourceDelegation(ctx context.Context, d *business.SourceDelegation) error {
	return s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO source_delegations (id, org_id, source_id, principal_id, module_prefix, binding_id, binding_digest)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7)
		RETURNING created_at`,
		d.ID, d.OrgID, d.SourceID, d.PrincipalID, d.ModulePrefix, d.BindingID, d.BindingDigest).Scan(&d.CreatedAt)
}

func (s *PostgresStore) RevokeSourceDelegations(
	ctx context.Context, filter business.SourceDelegationFilter, reason, revokedBy string,
) ([]*business.SourceDelegation, error) {
	if filter.OrgID == "" {
		return nil, errors.New("source delegation revocation requires an organization")
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		UPDATE source_delegations
		SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = $6, revoked_by = NULLIF($7, '')::uuid
		WHERE org_id = $1::uuid
		  AND revoked_at IS NULL
		  AND (NULLIF($2, '')::uuid IS NULL OR id = NULLIF($2, '')::uuid)
		  AND (NULLIF($3, '')::uuid IS NULL OR source_id = NULLIF($3, '')::uuid)
		  AND (NULLIF($4, '')::uuid IS NULL OR principal_id = NULLIF($4, '')::uuid)
		  AND ($5 = '' OR module_prefix = $5)
		RETURNING `+sourceDelegationColumns,
		filter.OrgID, filter.ID, filter.SourceID, filter.PrincipalID, filter.ModulePrefix, reason, revokedBy)
	if err != nil {
		return nil, err
	}
	return scanSourceDelegations(rows)
}

func (s *PostgresStore) ListSourceDelegations(
	ctx context.Context, orgID, sourceID string, includeRevoked bool,
) ([]*business.SourceDelegation, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+sourceDelegationColumns+`
		FROM source_delegations
		WHERE org_id = $1::uuid
		  AND (NULLIF($2, '')::uuid IS NULL OR source_id = NULLIF($2, '')::uuid)
		  AND ($3 OR revoked_at IS NULL)
		ORDER BY created_at DESC, id`,
		orgID, sourceID, includeRevoked)
	if err != nil {
		return nil, err
	}
	return scanSourceDelegations(rows)
}

func (s *PostgresStore) GetSourceDelegation(ctx context.Context, orgID, id string) (*business.SourceDelegation, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+sourceDelegationColumns+`
		FROM source_delegations
		WHERE id = $2::uuid
		  AND (NULLIF($1, '')::uuid IS NULL OR org_id = NULLIF($1, '')::uuid)`,
		orgID, id)
	if err != nil {
		return nil, err
	}
	found, err := scanSourceDelegations(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return found[0], nil
}

func (s *PostgresStore) ActiveSourceDelegation(ctx context.Context, sourceID, modulePrefix string) (*business.SourceDelegation, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+sourceDelegationColumns+`
		FROM source_delegations
		WHERE source_id = $1::uuid
		  AND module_prefix = $2
		  AND revoked_at IS NULL`,
		sourceID, modulePrefix)
	if err != nil {
		return nil, err
	}
	found, err := scanSourceDelegations(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return found[0], nil
}

func (s *PostgresStore) ActiveSourceDelegationsForPrincipal(
	ctx context.Context, orgID, principalID, modulePrefix string,
) ([]*business.SourceDelegation, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+sourceDelegationColumns+`
		FROM source_delegations
		WHERE org_id = $1::uuid
		  AND principal_id = $2::uuid
		  AND module_prefix = $3
		  AND revoked_at IS NULL
		ORDER BY created_at DESC, id`,
		orgID, principalID, modulePrefix)
	if err != nil {
		return nil, err
	}
	return scanSourceDelegations(rows)
}

func (s *PostgresStore) SourceDelegationFacts(
	ctx context.Context, orgID, principalID, sourceID string,
) (*business.SourceDelegationFacts, error) {
	var facts business.SourceDelegationFacts
	var orgRevision, principalRevision int64
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT
		    EXISTS (
		        SELECT 1 FROM datasource_sources
		        WHERE org_id = $1::uuid AND id = $3::uuid
		    ),
		    COALESCE((
		        SELECT role FROM organization_members
		        WHERE org_id = $1::uuid AND user_id = $2::uuid
		    ), ''),
		    COALESCE((
		        SELECT status::text FROM users WHERE uuid = $2::uuid
		    ), ''),
		    COALESCE((
		        SELECT revision FROM organization_authorization_revisions
		        WHERE org_id = $1::uuid
		    ), 0),
		    COALESCE((
		        SELECT revision FROM principal_authorization_revisions
		        WHERE org_id = $1::uuid AND principal_id = $2::uuid
		    ), 0)`,
		orgID, principalID, sourceID,
	).Scan(&facts.SourceExists, &facts.MemberRole, &facts.UserStatus, &orgRevision, &principalRevision)
	if err != nil {
		return nil, err
	}
	if orgRevision > 0 {
		facts.OrganizationRevision = uint64(orgRevision)
	}
	if principalRevision > 0 {
		facts.PrincipalRevision = uint64(principalRevision)
	}
	return &facts, nil
}

func (s *PostgresStore) SourceDelegationMemberRole(ctx context.Context, orgID, userID string) (string, error) {
	var role string
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT role FROM organization_members
		WHERE org_id = $1::uuid AND user_id = $2::uuid`,
		orgID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}
