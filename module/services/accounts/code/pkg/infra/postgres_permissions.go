package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PostgresStore) CreateRole(ctx context.Context, role *gen.Role) error {
	w := wool.Get(ctx).In("CreateRole")

	// Reuse the caller's tx if one is on context — that's where
	// WithOrgTx / WithControlPlane put it, and stacking a fresh BeginTxFunc
	// would lose the SET LOCAL app.current_org_id state and the role
	// INSERT would fail the WITH CHECK on the polymorphic policy
	// (Phase 2E migration). Same pattern as postgres_org.go's
	// CreateOrganization.
	exec := func(ctx context.Context) error {
		executor := s.getQueryExecutor(ctx)
		var orgID *string
		if role.OrgId != "" {
			orgID = &role.OrgId
		}
		if _, err := executor.Exec(ctx, `
			INSERT INTO roles (id, name, description, built_in, org_id, created_at)
			VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP)`,
			role.Id, role.Name, role.Description, role.BuiltIn, orgID,
		); err != nil {
			return w.Wrapf(err, "failed to insert role")
		}
		for _, perm := range role.Permissions {
			if _, err := executor.Exec(ctx, `
				INSERT INTO role_permissions (role_id, resource, action)
				VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`,
				role.Id, perm.Resource, perm.Action,
			); err != nil {
				return w.Wrapf(err, "failed to insert permission")
			}
		}
		return nil
	}

	if _, hasTx := ctx.Value("tx").(pgx.Tx); hasTx {
		return exec(ctx)
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ctx = context.WithValue(ctx, "tx", tx) //nolint:staticcheck
		return exec(ctx)
	})
}

func (s *PostgresStore) ListRoles(ctx context.Context, orgID string) ([]*gen.Role, error) {
	w := wool.Get(ctx).In("ListRoles")
	executor := s.getQueryExecutor(ctx)

	// Single LEFT JOIN query replaces the N+1 "one SELECT per role" pattern
	// that was flagged in the audit. We group by role_id on the client side
	// since pg's array_agg would require aligning Permission's two columns
	// into a single aggregated type and the client-side fold is simpler.
	var rows pgx.Rows
	var err error
	const baseQuery = `
		SELECT r.id, r.name, r.description, r.built_in, r.org_id, r.created_at,
		       rp.resource, rp.action
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		`
	if orgID == "" {
		rows, err = executor.Query(ctx, baseQuery+`
			WHERE r.org_id IS NULL
			ORDER BY r.built_in DESC, r.name, rp.resource, rp.action`,
		)
	} else {
		rows, err = executor.Query(ctx, baseQuery+`
			WHERE r.org_id IS NULL OR r.org_id = $1
			ORDER BY r.built_in DESC, r.name, rp.resource, rp.action`, orgID,
		)
	}
	if err != nil {
		return nil, w.Wrapf(err, "failed to list roles")
	}
	defer rows.Close()

	// Fold rows by role id; LEFT JOIN means a role with no perms returns
	// exactly one row with NULL resource/action.
	byID := make(map[string]*gen.Role)
	var order []string
	for rows.Next() {
		var (
			rid, rname, rdesc string
			builtIn           bool
			orgIDVal          *string
			createdAt         time.Time
			resource, action  *string
		)
		if err := rows.Scan(&rid, &rname, &rdesc, &builtIn, &orgIDVal, &createdAt, &resource, &action); err != nil {
			return nil, w.Wrapf(err, "failed to scan role row")
		}
		r, seen := byID[rid]
		if !seen {
			r = &gen.Role{Id: rid, Name: rname, Description: rdesc, BuiltIn: builtIn}
			if orgIDVal != nil {
				r.OrgId = *orgIDVal
			}
			byID[rid] = r
			order = append(order, rid)
		}
		if resource != nil && action != nil {
			r.Permissions = append(r.Permissions, &gen.Permission{Resource: *resource, Action: *action})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "failed iterating role rows")
	}

	roles := make([]*gen.Role, 0, len(order))
	for _, id := range order {
		roles = append(roles, byID[id])
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

	var subjectKind string
	switch assignment.SubjectKind {
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		subjectKind = "principal"
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		subjectKind = "team"
	default:
		return fmt.Errorf("assign role: unsupported subject kind %s", assignment.SubjectKind)
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

// ListRoleAssignments returns assignments scoped to an org, optionally
// filtered by subject. Inside WithOrgTx the role_assignments policy
// (Phase 2E) filters to org_id = current setting; the subject_id +
// kind filters narrow further on top of that.
func (s *PostgresStore) ListRoleAssignments(ctx context.Context, orgID string, subjectID string, subjectKind gen.SubjectKind) ([]*gen.RoleAssignment, error) {
	w := wool.Get(ctx).In("ListRoleAssignments")
	executor := s.getQueryExecutor(ctx)

	query := `
		SELECT id, subject_id, subject_kind, role_id, org_id, scope, assigned_at
		FROM role_assignments
		WHERE 1=1`
	args := []any{}
	argN := 1
	if orgID != "" {
		query += fmt.Sprintf(" AND org_id = $%d", argN)
		args = append(args, orgID)
		argN++
	}
	if subjectID != "" {
		query += fmt.Sprintf(" AND subject_id = $%d", argN)
		args = append(args, subjectID)
		argN++
	}
	switch subjectKind {
	case gen.SubjectKind_SUBJECT_KIND_UNSPECIFIED:
		// No kind filter: return both direct Principal and Team assignments.
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		query += fmt.Sprintf(" AND subject_kind = $%d", argN)
		args = append(args, "principal")
		argN++
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		query += fmt.Sprintf(" AND subject_kind = $%d", argN)
		args = append(args, "team")
		argN++
	default:
		return nil, fmt.Errorf("list role assignments: unsupported subject kind %s", subjectKind)
	}
	query += " ORDER BY assigned_at DESC"

	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list role assignments")
	}
	defer rows.Close()

	var out []*gen.RoleAssignment
	for rows.Next() {
		var (
			id, subjID, kind, roleID string
			orgIDVal, scopeVal       *string
			assignedAt               time.Time
		)
		if err := rows.Scan(&id, &subjID, &kind, &roleID, &orgIDVal, &scopeVal, &assignedAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan assignment row")
		}
		ra := &gen.RoleAssignment{
			Id:         id,
			SubjectId:  subjID,
			RoleId:     roleID,
			AssignedAt: timestamppb.New(assignedAt),
		}
		switch kind {
		case "principal":
			ra.SubjectKind = gen.SubjectKind_SUBJECT_KIND_PRINCIPAL
		case "team":
			ra.SubjectKind = gen.SubjectKind_SUBJECT_KIND_TEAM
		default:
			return nil, fmt.Errorf("list role assignments: unsupported stored subject kind %q", kind)
		}
		if orgIDVal != nil {
			ra.OrgId = *orgIDVal
		}
		if scopeVal != nil {
			ra.Scope = *scopeVal
		}
		out = append(out, ra)
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "iterating assignment rows")
	}
	return out, nil
}

func (s *PostgresStore) RevokeRole(ctx context.Context, subjectID string, roleID string, orgID string, scope string) error {
	w := wool.Get(ctx).In("RevokeRole")
	executor := s.getQueryExecutor(ctx)

	// PostgreSQL comparisons against NULL yield NULL (unknown), not false —
	// so `org_id = '' OR ...` silently misses NULL org_id rows. Pass NULL
	// explicitly via sql.NullString when the caller passed an empty string
	// so the `IS NOT DISTINCT FROM` semantic works in a single expression.
	var orgArg, scopeArg any
	if orgID == "" {
		orgArg = nil
	} else {
		orgArg = orgID
	}
	if scope == "" {
		scopeArg = nil
	} else {
		scopeArg = scope
	}

	// IS NOT DISTINCT FROM treats NULL = NULL as true; works with both
	// NULL rows and concrete values, and keeps the query parameterized.
	_, err := executor.Exec(ctx, `
		DELETE FROM role_assignments
		WHERE subject_id = $1 AND role_id = $2
		  AND org_id IS NOT DISTINCT FROM $3
		  AND scope IS NOT DISTINCT FROM $4`,
		subjectID, roleID, orgArg, scopeArg,
	)
	if err != nil {
		return w.Wrapf(err, "failed to revoke role")
	}
	return nil
}

// CheckPermission checks whether a subject (direct principal or team) has a
// given permission.
// It supports:
//   - Wildcard permissions: resource="*" or action="*" match everything
//   - Scope matching (strict): an unscoped check (scope=="") is satisfied only
//     by NULL-scope assignments; a scoped check is satisfied by an assignment
//     with the same scope OR a NULL-scope assignment. NULL-scope assignments
//     are deliberately org-wide and subsume all scopes — a scoped grant never
//     widens to satisfy an unscoped check.
//   - Team inheritance: human principals also inherit permissions assigned to
//     teams they belong to
func (s *PostgresStore) CheckPermission(ctx context.Context, subjectID string, subjectKind gen.SubjectKind, resource string, action string, orgID string, scope string) (bool, string, error) {
	w := wool.Get(ctx).In("CheckPermission")
	executor := s.getQueryExecutor(ctx)

	// Query: find any role assignment for this subject (or their teams) that grants
	// the requested resource:action permission via wildcard matching.
	//
	// This single query handles:
	// 1. Direct principal role assignments
	// 2. Team role assignments (for human principals who are team members)
	// 3. Wildcard permission matching (* on resource or action)
	// 4. Scope matching (strict): unscoped checks match only NULL-scope
	//    assignments; scoped checks match the same scope or a NULL (org-wide) scope
	// 5. Org scoping (NULL org = global role, specific org = org role)
	// Build query dynamically to avoid passing empty strings as UUID parameters
	var subjectPredicate string
	switch subjectKind {
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		subjectPredicate = `(
			(ra.subject_kind = 'principal' AND ra.subject_id = $1)
			OR
			(ra.subject_kind = 'team' AND ra.subject_id IN (
				SELECT team_id FROM team_members WHERE user_id = $1
			))
		)`
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		subjectPredicate = `(ra.subject_kind = 'team' AND ra.subject_id = $1)`
	default:
		return false, "", fmt.Errorf("check permission: unsupported subject kind %s", subjectKind)
	}

	query := `
		SELECT rp.resource, rp.action, r.name as role_name
		FROM role_assignments ra
		JOIN roles r ON ra.role_id = r.id
		JOIN role_permissions rp ON r.id = rp.role_id
		WHERE ` + subjectPredicate + `
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
	} else {
		query += ` AND ra.scope IS NULL`
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

	// Auth-flow read: organization_members + role_assignments are
	// RLS-protected and we don't yet have a tenant on context (the
	// whole point of resolving is to FIND the tenant). Open a tx and
	// assume app_control_plane so the SELECTs see all tenants. The defer handles release;
	// queries are read-only so commit-vs-rollback semantics don't
	// matter for data correctness.
	//
	// Role membership is the explicit capability: app_tenant cannot mint it by
	// setting a custom session variable.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, w.Wrapf(err, "begin tx for resolve")
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only tx; rollback at end
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
		return nil, w.Wrapf(err, "assume control-plane role for resolve")
	}
	executor := tx

	// Step 1: Find user by provider identity
	var userID string
	err = executor.QueryRow(ctx, `
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
	roleQuery := `
		SELECT DISTINCT r.name FROM roles r
		JOIN role_assignments ra ON r.id = ra.role_id
		WHERE (
			(ra.subject_kind = 'principal' AND ra.subject_id = $1)
			OR (ra.subject_kind = 'team' AND ra.subject_id IN (
				SELECT team_id FROM team_members WHERE user_id = $1
			))
		)`
	var rows pgx.Rows
	if orgID == "" {
		// A user may legitimately exist before joining or creating an
		// organization. Do not bind "" to a UUID column: PostgreSQL parses
		// parameters by column type before it can evaluate an OR expression.
		rows, err = executor.Query(ctx, roleQuery+` AND ra.org_id IS NULL`, userID)
	} else {
		rows, err = executor.Query(ctx,
			roleQuery+` AND (ra.org_id IS NULL OR ra.org_id = $2)`, userID, orgID,
		)
	}
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
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "failed iterating user roles")
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
	if strings.TrimSpace(userID) == "" {
		return "", status.Error(codes.InvalidArgument, "user id required")
	}
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

// (Removed: ListRoleAssignmentsForUser was dead code with no
// orgID filter and no RLS-wrap requirement. The Service-layer
// ListRoleAssignments — defined on the same store via the org-
// scoped path — replaces it. If a future caller needs cross-org
// "all assignments for user X", add a new method that's explicitly
// WithControlPlane-only and uses a distinct name.)
