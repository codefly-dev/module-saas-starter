package infra

import (
	"context"
	"encoding/json"
	"errors"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
)

// GetAPIKeyAuthentication resolves a presented API key and the current policy
// projection for its owner in one repeatable-read, named control-plane
// transaction. It is deliberately narrower than exposing WithControlPlane to
// the business layer, and membership revocation is observed atomically with
// the key lookup.
func (s *PostgresStore) GetAPIKeyAuthentication(ctx context.Context, keyHash string) (*business.APIKeyAuthentication, error) {
	w := wool.Get(ctx).In("GetAPIKeyAuthentication")
	var authentication *business.APIKeyAuthentication
	err := s.WithAuthLookupTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		key, err := s.GetAPIKeyByHash(ctx, keyHash)
		if err != nil {
			return err
		}
		if key == nil {
			return nil
		}
		authentication = &business.APIKeyAuthentication{
			Key: key,
			Claims: business.APIKeyIdentityClaims{
				Attributes: map[string]string{},
			},
		}
		claims := &authentication.Claims

		var profile []byte
		err = tx.QueryRow(ctx, `
			SELECT om.role, COALESCE(pa.platform_role::text, ''), u.profile
			FROM organization_members om
			JOIN users u ON u.uuid = om.user_id
			LEFT JOIN platform_admins pa ON pa.user_id = om.user_id
			WHERE om.org_id = $1 AND om.user_id = $2`, key.OrganizationId, key.UserId,
		).Scan(&claims.OrgRole, &claims.PlatformRole, &profile)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return w.Wrapf(err, "failed to load API key owner")
		}
		claims.Member = true

		var rawAttributes map[string]any
		if err := json.Unmarshal(profile, &rawAttributes); err != nil {
			return w.Wrapf(err, "malformed user profile json")
		}
		for key, value := range rawAttributes {
			if text, ok := value.(string); ok {
				claims.Attributes[key] = text
			}
		}

		rows, err := tx.Query(ctx, `
			SELECT t.path
			FROM team_members m
			JOIN teams t ON t.id = m.team_id
			WHERE m.user_id = $1 AND t.org_id = $2
			ORDER BY t.path`, key.UserId, key.OrganizationId)
		if err != nil {
			return w.Wrapf(err, "failed to load team paths")
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return w.Wrapf(err, "failed to scan team path")
			}
			claims.Workspaces = append(claims.Workspaces, path)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return w.Wrapf(err, "failed iterating team paths")
		}
		rows.Close()

		rows, err = tx.Query(ctx, `
			SELECT DISTINCT r.name
			FROM role_assignments a
			JOIN roles r ON r.id = a.role_id
			WHERE a.org_id = $2
			  AND (
			    (a.subject_kind = 'principal' AND a.subject_id = $1)
			    OR (a.subject_kind = 'team' AND a.subject_id IN (
			      SELECT team_id FROM team_members WHERE user_id = $1
			    ))
			  )
			ORDER BY r.name`, key.UserId, key.OrganizationId)
		if err != nil {
			return w.Wrapf(err, "failed to load role names")
		}
		defer rows.Close()
		for rows.Next() {
			var role string
			if err := rows.Scan(&role); err != nil {
				return w.Wrapf(err, "failed to scan role name")
			}
			claims.Roles = append(claims.Roles, role)
		}
		if err := rows.Err(); err != nil {
			return w.Wrapf(err, "failed iterating role names")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return authentication, nil
}

// Identity-claims queries (Identity Claims v1) — the three facts ValidateAPIKey
// assembles for a policy-enforcement consumer: team paths (workspaces), RBAC
// role names, and profile attributes. Kept together: this file IS the read
// surface of the claims contract.

// ListTeamPathsForUser returns the PATHS of the teams the user belongs to in
// this org ("engineering/platform"). Literal membership — consumers that want
// subtree semantics expand ancestors themselves.
func (s *PostgresStore) ListTeamPathsForUser(ctx context.Context, userID string, orgID string) ([]string, error) {
	w := wool.Get(ctx).In("ListTeamPathsForUser")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT t.path
		FROM team_members m
		JOIN teams t ON t.id = m.team_id
		WHERE m.user_id = $1 AND t.org_id = $2
		ORDER BY t.path`, userID, orgID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list team paths")
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, w.Wrapf(err, "failed to scan team path")
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// ListRoleNamesForUser returns the names of roles assigned to the user in this
// org (direct principal assignments; team-subject assignments resolve via the
// teams the human principal is in).
//
// This path is team-inclusive because it is resolved fresh on every API-key
// validation, so team-inheritance carries no staleness risk. The access-token
// `sr` scoped-roles claim (pkg/auth/pg.resolveScopedRoles) deliberately does
// NOT mirror this — it is direct-principal-only because it caches into a
// ~15-minute token, and team-inheritance there would need extra session
// invalidation to avoid an over-authorization window. Keep the two divergent.
func (s *PostgresStore) ListRoleNamesForUser(ctx context.Context, userID string, orgID string) ([]string, error) {
	w := wool.Get(ctx).In("ListRoleNamesForUser")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT DISTINCT r.name
		FROM role_assignments a
		JOIN roles r ON r.id = a.role_id
		WHERE a.org_id = $2
		  AND (
		    (a.subject_kind = 'principal' AND a.subject_id = $1)
		    OR (a.subject_kind = 'team' AND a.subject_id IN (
		      SELECT team_id FROM team_members WHERE user_id = $1
		    ))
		  )
		ORDER BY r.name`, userID, orgID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list role names")
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, w.Wrapf(err, "failed to scan role name")
		}
		names = append(names, n)
	}
	return names, nil
}

// GetUserAttributes returns the STRING-valued entries of the user's profile
// JSONB. Non-string values are skipped (the claims contract is map<string,
// string>; richer typing is a later evolution, not a silent coercion).
func (s *PostgresStore) GetUserAttributes(ctx context.Context, userID string) (map[string]string, error) {
	w := wool.Get(ctx).In("GetUserAttributes")
	executor := s.getQueryExecutor(ctx)

	var raw []byte
	err := executor.QueryRow(ctx, `SELECT profile FROM users WHERE uuid = $1`, userID).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return map[string]string{}, nil
		}
		return nil, w.Wrapf(err, "failed to get profile")
	}
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, w.Wrapf(err, "malformed profile json")
	}
	attrs := make(map[string]string, len(profile))
	for k, v := range profile {
		if s, ok := v.(string); ok {
			attrs[k] = s
		}
	}
	return attrs, nil
}
