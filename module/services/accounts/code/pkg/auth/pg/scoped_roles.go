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
// Intentional divergence from infra.ListRoleNamesForUser (the API-key `x-roles`
// claims path), which IS team-inclusive: that path is resolved fresh on every
// API-key validation, so team-inheritance carries no staleness risk. This claim
// instead rides a ~15-minute access token. Making it team-inclusive would leave
// a removed team member holding the team's scoped grants until their next
// refresh — an over-authorization window — unless team_members/teams also
// revoked sessions. Direct-only is deliberately chosen so migration 88 is the
// complete invalidation boundary. Do NOT "harmonize" the two resolvers.
//
// The result feeds the compact `sr` access-token claim. To keep token size
// predictable it retains at most auth.MaxScopedRoleAssignments (scope, role)
// pairs, in a deterministic order; when the caller holds more, the returned
// truncated flag is true so the excess is signalled (via the `srt` claim) and a
// consumer falls back to CheckPermission rather than the mint failing outright
// and locking the user out of authentication. Callers must run inside the same
// locked, RLS-bypassed transaction that resolves org/platform authorization.
func resolveScopedRoles(ctx context.Context, tx pgx.Tx, userID, orgID uuid.UUID) (scoped map[string][]string, truncated bool, err error) {
	if orgID == uuid.Nil {
		return nil, false, nil
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
		return nil, false, err
	}
	defer rows.Close()

	var count int
	scoped = map[string][]string{}
	for rows.Next() {
		// Stop scanning once the bound is reached: one more row proves the
		// grant set is larger than the claim can carry, so mark it truncated
		// and leave the rest unread (memory stays bounded to the cap).
		if count >= auth.MaxScopedRoleAssignments {
			return scopedOrNil(scoped), true, nil
		}
		var scope, role string
		if err := rows.Scan(&scope, &role); err != nil {
			return nil, false, err
		}
		scoped[scope] = append(scoped[scope], role)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return scopedOrNil(scoped), false, nil
}

func scopedOrNil(scoped map[string][]string) map[string][]string {
	if len(scoped) == 0 {
		return nil
	}
	return scoped
}
