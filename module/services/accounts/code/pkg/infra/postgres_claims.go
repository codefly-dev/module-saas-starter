package infra

import (
	"context"
	"encoding/json"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
)

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
// org (direct user assignments; team-subject assignments resolve via the teams
// the user is in).
func (s *PostgresStore) ListRoleNamesForUser(ctx context.Context, userID string, orgID string) ([]string, error) {
	w := wool.Get(ctx).In("ListRoleNamesForUser")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT DISTINCT r.name
		FROM role_assignments a
		JOIN roles r ON r.id = a.role_id
		WHERE a.org_id = $2
		  AND (
		    (a.subject_kind = 'user' AND a.subject_id = $1)
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
