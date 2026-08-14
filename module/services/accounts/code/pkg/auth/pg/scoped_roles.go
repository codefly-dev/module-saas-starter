package pgauth

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"accounts/pkg/auth"
)

// resolveScopedRoles returns the caller's direct, fine-grained role grants in
// orgID: a map from each non-NULL role_assignments.scope to the sorted role
// names granted at that scope. Only principal-subject assignments are read —
// team-inherited and org-global (NULL-scope) grants stay on the authoritative
// CheckPermission path, keeping this claim's staleness bounded by the single
// role_assignments session-invalidation trigger.
//
// The result feeds the compact `sr` access-token claim. More than
// auth.MaxScopedRoleAssignments pairs returns ErrScopedRolesExceedLimit so an
// over-large grant set fails loudly rather than minting a silently truncated,
// under-authorized token. Callers must run inside the same locked, RLS-bypassed
// transaction that resolves org/platform authorization.
func resolveScopedRoles(ctx context.Context, tx pgx.Tx, userID, orgID uuid.UUID) (map[string][]string, error) {
	if orgID == uuid.Nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT a.scope, r.name
		FROM role_assignments a
		JOIN roles r ON r.id = a.role_id
		WHERE a.subject_kind = 'principal'
		  AND a.subject_id = $1
		  AND a.org_id = $2
		  AND a.scope IS NOT NULL
		ORDER BY a.scope, r.name`, userID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var count int
	scoped := map[string][]string{}
	for rows.Next() {
		var scope, role string
		if err := rows.Scan(&scope, &role); err != nil {
			return nil, err
		}
		count++
		if count > auth.MaxScopedRoleAssignments {
			return nil, auth.ErrScopedRolesExceedLimit
		}
		scoped[scope] = append(scoped[scope], role)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(scoped) == 0 {
		return nil, nil
	}
	return scoped, nil
}
