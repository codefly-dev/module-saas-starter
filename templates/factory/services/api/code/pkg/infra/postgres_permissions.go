package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"backend/pkg/business"
	"backend/pkg/gen"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PostgresStore) CreateRole(ctx context.Context, role *gen.Role) error {
	w := wool.Get(ctx).In("CreateRole")

	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Insert role
		var orgID *string
		if role.OrgId != "" {
			orgID = &role.OrgId
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO roles (id, name, description, built_in, org_id, created_at)
			VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP)`,
			role.Id, role.Name, role.Description, role.BuiltIn, orgID,
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert role")
		}

		// Insert permissions
		for _, perm := range role.Permissions {
			_, err := tx.Exec(ctx, `
				INSERT INTO role_permissions (role_id, resource, action)
				VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`,
				role.Id, perm.Resource, perm.Action,
			)
			if err != nil {
				return w.Wrapf(err, "failed to insert permission")
			}
		}
		return nil
	})
}

func (s *PostgresStore) ListRoles(ctx context.Context, orgID string) ([]*gen.Role, error) {
	w := wool.Get(ctx).In("ListRoles")
	executor := s.getQueryExecutor(ctx)

	// List global built-in roles + org-specific roles
	var rows pgx.Rows
	var err error
	if orgID == "" {
		rows, err = executor.Query(ctx, `
			SELECT r.id, r.name, r.description, r.built_in, r.org_id, r.created_at
			FROM roles r
			WHERE r.org_id IS NULL
			ORDER BY r.built_in DESC, r.name`,
		)
	} else {
		rows, err = executor.Query(ctx, `
			SELECT r.id, r.name, r.description, r.built_in, r.org_id, r.created_at
			FROM roles r
			WHERE r.org_id IS NULL OR r.org_id = $1
			ORDER BY r.built_in DESC, r.name`, orgID,
		)
	}
	if err != nil {
		return nil, w.Wrapf(err, "failed to list roles")
	}
	defer rows.Close()

	var roles []*gen.Role
	for rows.Next() {
		var r gen.Role
		var createdAt time.Time
		var orgIDVal *string
		if err := rows.Scan(&r.Id, &r.Name, &r.Description, &r.BuiltIn, &orgIDVal, &createdAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan role")
		}
		if orgIDVal != nil {
			r.OrgId = *orgIDVal
		}

		// Load permissions for this role
		permRows, err := executor.Query(ctx, `
			SELECT resource, action FROM role_permissions WHERE role_id = $1`, r.Id,
		)
		if err != nil {
			return nil, w.Wrapf(err, "failed to list role permissions")
		}
		for permRows.Next() {
			var p gen.Permission
			if err := permRows.Scan(&p.Resource, &p.Action); err != nil {
				permRows.Close()
				return nil, w.Wrapf(err, "failed to scan permission")
			}
			r.Permissions = append(r.Permissions, &p)
		}
		permRows.Close()

		roles = append(roles, &r)
	}
	return roles, nil
}

func (s *PostgresStore) DeleteRole(ctx context.Context, roleID string) error {
	w := wool.Get(ctx).In("DeleteRole")
	executor := s.getQueryExecutor(ctx)

	// Prevent deleting built-in roles
	var builtIn bool
	err := executor.QueryRow(ctx, `SELECT built_in FROM roles WHERE id = $1`, roleID).Scan(&builtIn)
	if err != nil {
		return w.Wrapf(err, "failed to check role")
	}
	if builtIn {
		return w.NewError("cannot delete built-in role")
	}

	_, err = executor.Exec(ctx, `DELETE FROM roles WHERE id = $1`, roleID)
	if err != nil {
		return w.Wrapf(err, "failed to delete role")
	}
	return nil
}

func (s *PostgresStore) AssignRole(ctx context.Context, assignment *gen.RoleAssignment) error {
	w := wool.Get(ctx).In("AssignRole")
	executor := s.getQueryExecutor(ctx)

	subjectKind := "user"
	if assignment.SubjectKind == gen.SubjectKind_SUBJECT_KIND_TEAM {
		subjectKind = "team"
	}

	// Handle nullable org_id and scope
	var orgID, scope interface{}
	if assignment.OrgId != "" {
		orgID = assignment.OrgId
	}
	if assignment.Scope != "" {
		scope = assignment.Scope
	}

	_, err := executor.Exec(ctx, `
		INSERT INTO role_assignments (id, subject_id, subject_kind, role_id, org_id, scope, assigned_at)
		VALUES ($1, $2, $3, $4, $5, $6, CURRENT_TIMESTAMP)
		ON CONFLICT DO NOTHING`,
		assignment.Id, assignment.SubjectId, subjectKind,
		assignment.RoleId, orgID, scope,
	)
	if err != nil {
		return w.Wrapf(err, "failed to assign role")
	}
	return nil
}

func (s *PostgresStore) RevokeRole(ctx context.Context, subjectID string, roleID string, orgID string, scope string) error {
	w := wool.Get(ctx).In("RevokeRole")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		DELETE FROM role_assignments
		WHERE subject_id = $1 AND role_id = $2
		  AND (org_id = $3 OR ($3 = '' AND org_id IS NULL))
		  AND (scope = $4 OR ($4 = '' AND scope IS NULL))`,
		subjectID, roleID, orgID, scope,
	)
	if err != nil {
		return w.Wrapf(err, "failed to revoke role")
	}
	return nil
}

// CheckPermission checks whether a subject (user or team) has a given permission.
// It supports:
//   - Wildcard permissions: resource="*" or action="*" match everything
//   - Scope matching: assignment scope must match or be empty (global)
//   - Team inheritance: if the subject is a user, also check permissions
//     assigned to teams the user belongs to
func (s *PostgresStore) CheckPermission(ctx context.Context, subjectID string, subjectKind gen.SubjectKind, resource string, action string, orgID string, scope string) (bool, string, error) {
	w := wool.Get(ctx).In("CheckPermission")
	executor := s.getQueryExecutor(ctx)

	// Query: find any role assignment for this subject (or their teams) that grants
	// the requested resource:action permission via wildcard matching.
	//
	// This single query handles:
	// 1. Direct user role assignments
	// 2. Team role assignments (for users who are team members)
	// 3. Wildcard permission matching (* on resource or action)
	// 4. Scope matching (NULL scope = global, specific scope = scoped)
	// 5. Org scoping (NULL org = global role, specific org = org role)
	// Build query dynamically to avoid passing empty strings as UUID parameters
	query := `
		SELECT rp.resource, rp.action, r.name as role_name
		FROM role_assignments ra
		JOIN roles r ON ra.role_id = r.id
		JOIN role_permissions rp ON r.id = rp.role_id
		WHERE (
			ra.subject_id = $1
			OR
			(ra.subject_kind = 'team' AND ra.subject_id IN (
				SELECT team_id FROM team_members WHERE user_id = $1
			))
		)
		AND (rp.resource = '*' OR rp.resource = $2)
		AND (rp.action = '*' OR rp.action = $3)`

	args := []any{subjectID, resource, action}

	if orgID != "" {
		query += ` AND (ra.org_id IS NULL OR ra.org_id = $4)`
		args = append(args, orgID)
	} else {
		query += ` AND ra.org_id IS NULL`
	}

	if scope != "" {
		query += fmt.Sprintf(` AND (ra.scope IS NULL OR ra.scope = $%d)`, len(args)+1)
		args = append(args, scope)
	}

	query += ` LIMIT 1`

	var matchedResource, matchedAction, roleName string
	err := executor.QueryRow(ctx, query, args...).Scan(&matchedResource, &matchedAction, &roleName)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "no matching permission found", nil
		}
		return false, "", w.Wrapf(err, "failed to check permission")
	}

	return true, "granted via role: " + roleName, nil
}

// ResolveIdentity maps an auth provider ID to internal user/org/roles.
// Used by the auth sidecar to translate external auth IDs into internal identifiers.
// Returns org_role (from organization_members) and platform_role (from platform_admins).
func (s *PostgresStore) ResolveIdentity(ctx context.Context, provider string, providerID string) (*business.ResolvedIdentity, error) {
	w := wool.Get(ctx).In("ResolveIdentity")
	executor := s.getQueryExecutor(ctx)

	// Step 1: Find user by provider identity
	var userID string
	err := executor.QueryRow(ctx, `
		SELECT u.uuid FROM users u
		JOIN user_identities ui ON u.uuid = ui.user_uuid
		WHERE ui.provider = $1 AND ui.provider_id = $2`,
		provider, providerID,
	).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &business.ResolvedIdentity{Found: false}, nil
		}
		return nil, w.Wrapf(err, "failed to resolve identity")
	}

	// Step 2: Find primary org and org role
	var orgID string
	var orgRole string
	err = executor.QueryRow(ctx, `
		SELECT org_id, role FROM organization_members
		WHERE user_id = $1
		ORDER BY joined_at ASC LIMIT 1`,
		userID,
	).Scan(&orgID, &orgRole)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, w.Wrapf(err, "failed to get user org")
	}

	// Step 3: Check platform role
	var platformRole string
	err = executor.QueryRow(ctx, `
		SELECT platform_role FROM platform_admins WHERE user_id = $1`,
		userID,
	).Scan(&platformRole)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, w.Wrapf(err, "failed to get platform role")
	}

	// Step 4: Get role names (backward compat, deprecated)
	rows, err := executor.Query(ctx, `
		SELECT DISTINCT r.name FROM roles r
		JOIN role_assignments ra ON r.id = ra.role_id
		WHERE (
			ra.subject_id = $1
			OR (ra.subject_kind = 'team' AND ra.subject_id IN (
				SELECT team_id FROM team_members WHERE user_id = $1
			))
		)
		AND (ra.org_id IS NULL OR ra.org_id = $2)`,
		userID, orgID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to get user roles")
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, w.Wrapf(err, "failed to scan role")
		}
		roles = append(roles, name)
	}

	// Update last_used on identity
	_, _ = executor.Exec(ctx, `
		UPDATE user_identities SET last_used = CURRENT_TIMESTAMP
		WHERE provider = $1 AND provider_id = $2`,
		provider, providerID,
	)

	return &business.ResolvedIdentity{
		UserID:       userID,
		OrgID:        orgID,
		OrgRole:      orgRole,
		PlatformRole: platformRole,
		Roles:        roles,
		Found:        true,
	}, nil
}

// ── Platform admin store methods ────────────────────────────────

// GetPlatformRole returns the platform role for a user, or "" if not a platform admin.
func (s *PostgresStore) GetPlatformRole(ctx context.Context, userID string) (string, error) {
	w := wool.Get(ctx).In("GetPlatformRole")
	executor := s.getQueryExecutor(ctx)

	var role string
	err := executor.QueryRow(ctx, `
		SELECT platform_role FROM platform_admins WHERE user_id = $1`,
		userID,
	).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", w.Wrapf(err, "failed to get platform role")
	}
	return role, nil
}

// GrantPlatformRole grants a platform role to a user.
func (s *PostgresStore) GrantPlatformRole(ctx context.Context, userID, role, grantedBy string) error {
	w := wool.Get(ctx).In("GrantPlatformRole")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		INSERT INTO platform_admins (user_id, platform_role, granted_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET platform_role = $2, granted_by = $3, granted_at = CURRENT_TIMESTAMP`,
		userID, role, grantedBy,
	)
	if err != nil {
		return w.Wrapf(err, "failed to grant platform role")
	}
	return nil
}

// RevokePlatformRole removes a user's platform admin status.
func (s *PostgresStore) RevokePlatformRole(ctx context.Context, userID string) error {
	w := wool.Get(ctx).In("RevokePlatformRole")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `DELETE FROM platform_admins WHERE user_id = $1`, userID)
	if err != nil {
		return w.Wrapf(err, "failed to revoke platform role")
	}
	return nil
}

// ListPlatformAdmins returns all platform admins.
func (s *PostgresStore) ListPlatformAdmins(ctx context.Context) ([]business.PlatformAdmin, error) {
	w := wool.Get(ctx).In("ListPlatformAdmins")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT user_id, platform_role, granted_by, granted_at
		FROM platform_admins ORDER BY granted_at`)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list platform admins")
	}
	defer rows.Close()

	var admins []business.PlatformAdmin
	for rows.Next() {
		var a business.PlatformAdmin
		var grantedBy *string
		if err := rows.Scan(&a.UserID, &a.PlatformRole, &grantedBy, &a.GrantedAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan platform admin")
		}
		if grantedBy != nil {
			a.GrantedBy = *grantedBy
		}
		admins = append(admins, a)
	}
	return admins, nil
}

// ListRoleAssignmentsForUser returns all role assignments for a user (direct + team).
func (s *PostgresStore) ListRoleAssignmentsForUser(ctx context.Context, userID string) ([]*gen.RoleAssignment, error) {
	w := wool.Get(ctx).In("ListRoleAssignmentsForUser")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT ra.id, ra.subject_id, ra.subject_kind, ra.role_id, ra.org_id, ra.scope, ra.assigned_at
		FROM role_assignments ra
		WHERE ra.subject_id = $1
		   OR (ra.subject_kind = 'team' AND ra.subject_id IN (
		       SELECT team_id FROM team_members WHERE user_id = $1
		   ))
		ORDER BY ra.assigned_at`, userID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list role assignments")
	}
	defer rows.Close()

	var assignments []*gen.RoleAssignment
	for rows.Next() {
		var a gen.RoleAssignment
		var subjectKind string
		var orgID, scope *string
		var assignedAt time.Time
		if err := rows.Scan(&a.Id, &a.SubjectId, &subjectKind, &a.RoleId, &orgID, &scope, &assignedAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan role assignment")
		}
		if subjectKind == "team" {
			a.SubjectKind = gen.SubjectKind_SUBJECT_KIND_TEAM
		} else {
			a.SubjectKind = gen.SubjectKind_SUBJECT_KIND_USER
		}
		if orgID != nil {
			a.OrgId = *orgID
		}
		if scope != nil {
			a.Scope = *scope
		}
		a.AssignedAt = timestamppb.New(assignedAt)
		assignments = append(assignments, &a)
	}
	return assignments, nil
}
