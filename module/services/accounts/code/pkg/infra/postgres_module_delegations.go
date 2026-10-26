package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Module delegations (migration 51). The tenant-facing statements run inside
// the organization's transaction, so its policy scopes them; a module's lookup
// by id runs under the control plane, whose policy does not, and returns the
// organization for the caller to compare. Every statement names the
// organization when it has one rather than trusting the policy to.

const moduleDelegationColumns = `id::text, org_id::text, principal_id::text, module_prefix, scopes, purpose,
	created_at, revoked_at, COALESCE(revoked_by::text, '')`

func scanModuleDelegations(rows pgx.Rows) ([]*business.ModuleDelegation, error) {
	defer rows.Close()
	var out []*business.ModuleDelegation
	for rows.Next() {
		var d business.ModuleDelegation
		var revokedAt *time.Time
		var scopes []byte
		if err := rows.Scan(&d.ID, &d.OrgID, &d.PrincipalID, &d.ModulePrefix, &scopes, &d.Purpose,
			&d.CreatedAt, &revokedAt, &d.RevokedBy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopes, &d.Scopes); err != nil {
			return nil, fmt.Errorf("module delegation %s: scopes do not decode: %w", d.ID, err)
		}
		d.RevokedAt = revokedAt
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) InsertModuleDelegation(ctx context.Context, d *business.ModuleDelegation) error {
	scopes, err := json.Marshal(d.Scopes)
	if err != nil {
		return fmt.Errorf("encode module delegation scopes: %w", err)
	}
	return s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO module_delegations (id, org_id, principal_id, module_prefix, scopes, purpose)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::jsonb, $6)
		RETURNING created_at`,
		d.ID, d.OrgID, d.PrincipalID, d.ModulePrefix, scopes, d.Purpose).Scan(&d.CreatedAt)
}

func (s *PostgresStore) ModuleDelegations(ctx context.Context, orgID string, ids []string) ([]*business.ModuleDelegation, error) {
	// An id that is no uuid names no delegation; it is dropped rather than
	// failing the statement, since a hop's delegation id need not be one.
	wanted := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := uuid.Parse(id); err == nil {
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+moduleDelegationColumns+`
		FROM module_delegations
		WHERE id = ANY($2::uuid[])
		  AND (NULLIF($1, '')::uuid IS NULL OR org_id = NULLIF($1, '')::uuid)
		ORDER BY created_at, id`,
		orgID, wanted)
	if err != nil {
		return nil, err
	}
	return scanModuleDelegations(rows)
}

func (s *PostgresStore) RevokeModuleDelegation(ctx context.Context, orgID, principalID, id string) (*business.ModuleDelegation, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, nil
	}
	// The stamp lands only on an active row; an already revoked one is read
	// back unchanged, so a second revoke is answered as the first left it.
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		WITH revoked AS (
			UPDATE module_delegations
			SET revoked_at = CURRENT_TIMESTAMP, revoked_by = $2::uuid
			WHERE org_id = $1::uuid AND principal_id = $2::uuid AND id = $3::uuid AND revoked_at IS NULL
			RETURNING `+moduleDelegationColumns+`
		)
		SELECT * FROM revoked
		UNION ALL
		SELECT `+moduleDelegationColumns+`
		FROM module_delegations
		WHERE org_id = $1::uuid AND principal_id = $2::uuid AND id = $3::uuid
		  AND NOT EXISTS (SELECT 1 FROM revoked)`,
		orgID, principalID, id)
	if err != nil {
		return nil, err
	}
	found, err := scanModuleDelegations(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return found[0], nil
}
