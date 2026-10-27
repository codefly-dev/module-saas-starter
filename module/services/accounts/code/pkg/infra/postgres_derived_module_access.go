package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
)

// The persistence half of "a solution's grant carries the module access it
// declares it consumes" (business/derived_module_access.go, migration 42).
// Three statements: which of a declared scope set a chosen role permits, the
// derived role itself, and the derived grants of a cause.

// rolePermitsPredicate is the one role_permissions match in this host. Every
// authority decision resolves a role the same way — an exact resource and action,
// or the '*' wildcard in either column — so the intersection a derived role is
// built from must resolve it the same way too, or a carried grant could permit
// something no authority check would have allowed, or deny something every other
// check allows. It is written once and read by both callers below.
const rolePermitsPredicate = `
	SELECT 1 FROM roles r
	  JOIN role_permissions rp ON rp.role_id = r.id
	 WHERE r.id::text = $1
	   AND (rp.resource = '*' OR rp.resource = p.resource)
	   AND (rp.action = '*' OR rp.action = p.action)`

// RolePermittedScopes keeps the (resource, action) pairs roleID permits, in the
// order they were asked about, through the same match RolePermits uses.
//
// One statement for a whole declared scope set rather than one per pair: a
// derivation asks about every scope of every module a solution reaches, and
// asking one round trip each inside the grant's transaction would make the cost
// of exposing a solution grow with what the solution consumes.
func (s *PostgresStore) RolePermittedScopes(ctx context.Context, roleID string, resources, actions []string) ([]bool, error) {
	if len(resources) != len(actions) {
		return nil, fmt.Errorf("role permitted scopes: %d resources against %d actions", len(resources), len(actions))
	}
	if len(resources) == 0 {
		return nil, nil
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT EXISTS (`+rolePermitsPredicate+`)
		  FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS p(resource, action, n)
		 ORDER BY p.n`,
		roleID, resources, actions)
	if err != nil {
		return nil, fmt.Errorf("read role permissions: %w", err)
	}
	defer rows.Close()
	permitted := make([]bool, 0, len(resources))
	for rows.Next() {
		var permits bool
		if err := rows.Scan(&permits); err != nil {
			return nil, fmt.Errorf("scan role permission: %w", err)
		}
		permitted = append(permitted, permits)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate role permissions: %w", err)
	}
	if len(permitted) != len(resources) {
		return nil, fmt.Errorf("role permitted scopes: asked about %d pairs, answered %d", len(resources), len(permitted))
	}
	return permitted, nil
}

// EnsureDerivedRole writes the role this host derived for one (solution
// installation, reached module, chosen role) and returns its id, re-deriving its
// permission set when the declaration or the chosen role has moved since.
//
// `derived_from` is the provenance AND the key: it names all three, so the same
// triple upserts the one row instead of accumulating a role per grant, and
// re-granting a solution reconciles what the role permits to the declaration as
// it is NOW. That is where the reconciliation happens — there is no background
// pass, and a declaration that changed between two grants of the same cause is
// picked up by the second one.
//
// The permission set is REPLACED rather than merged: it is a pure function of
// the declaration and the chosen role, and a scope the solution no longer
// declares must stop being permitted, not linger because it was once granted.
//
// Runs inside the caller's organization transaction. The role is org-scoped, so
// the roles policy accepts it exactly as it accepts a role an administrator
// creates; it is never a built-in and never catalogue-managed.
func (s *PostgresStore) EnsureDerivedRole(ctx context.Context, role business.DerivedRole) (string, error) {
	w := wool.Get(ctx).In("EnsureDerivedRole", wool.Field("derived_from", role.DerivedFrom))
	executor := s.getQueryExecutor(ctx)
	if role.DerivedFrom == "" {
		return "", fmt.Errorf("a derived role must name what it was derived from")
	}

	var roleID string
	// The conflict target is the partial unique index on derived_from, so two
	// grants of the same cause serialize on the row rather than both inserting.
	//
	// Only the DESCRIPTION is refreshed. `app_tenant` holds UPDATE on that one
	// column (migration 5), column-scoped on purpose, and the derived role's name
	// is the provenance precisely so that it never needs rewriting — see
	// business.derivedRoleName.
	if err := executor.QueryRow(ctx, `
		INSERT INTO roles (id, name, description, built_in, org_id, derived_from, created_at)
		VALUES ($1, $2, $3, false, $4, $5, CURRENT_TIMESTAMP)
		ON CONFLICT (derived_from) WHERE derived_from IS NOT NULL
		DO UPDATE SET description = EXCLUDED.description
		RETURNING id::text`,
		business.NewIDString(), role.Name, role.Description, role.OrgID, role.DerivedFrom,
	).Scan(&roleID); err != nil {
		return "", w.Wrapf(err, "failed to write the derived role")
	}

	if _, err := executor.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, roleID); err != nil {
		return "", w.Wrapf(err, "failed to clear the derived role's permissions")
	}
	for _, permission := range role.Permissions {
		if _, err := executor.Exec(ctx, `
			INSERT INTO role_permissions (role_id, resource, action)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			roleID, permission.Resource, permission.Action,
		); err != nil {
			return "", w.Wrapf(err, "failed to write the derived role's permission %s:%s",
				permission.Resource, permission.Action)
		}
	}
	return roleID, nil
}

// DerivedScopeGrants lists the derived grants caused by one grant, or by every
// grant at one scope node, so a revocation can remove exactly what it caused.
//
// Two filters because there are two causes to answer for, and both are read
// through the join to the cause rather than recomputed:
//
//   - causeGrantID: one exposure grant being revoked (RevokeInstallationFromTeam).
//   - causeScopeNodeID: every exposure grant at one installation's authority-root
//     NODE, for an uninstall — which removes the installation rather than one
//     team's grant on it, and knows its root as the node id the installation row
//     carries, so nothing has to resolve a path from a row it just revoked.
//
// Ordered so a revocation's events are in a fixed order
// rather than the order Postgres happened to return.
//
// Every relation is named with orgID explicitly, the cause and the node included.
// The uninstall path runs this under a narrowing's CONTROL-PLANE transaction,
// where there is no RLS floor to fall back on, and the predicate the tenant
// policy would have applied has to be in the statement instead.
func (s *PostgresStore) DerivedScopeGrants(ctx context.Context, orgID, causeGrantID, causeScopeNodeID string) ([]*gen.ScopeGrant, error) {
	w := wool.Get(ctx).In("DerivedScopeGrants", wool.Field("org_id", orgID))
	var cause, causeNode any
	if causeGrantID != "" {
		cause = causeGrantID
	}
	if causeScopeNodeID != "" {
		causeNode = causeScopeNodeID
	}
	if cause == nil && causeNode == nil {
		return nil, fmt.Errorf("derived scope grants: name a cause grant or a cause scope node")
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT d.id::text, d.subject_id::text, d.subject_kind, d.scope_path::text, d.role_id::text,
		       d.derived_from_grant_id::text
		  FROM scope_grants d
		  JOIN scope_grants c ON c.id = d.derived_from_grant_id
		 WHERE d.org_id = $1 AND c.org_id = $1
		   AND ($2::uuid IS NULL OR d.derived_from_grant_id = $2::uuid)
		   AND ($3::uuid IS NULL OR c.scope_path =
		        (SELECT scope_path FROM scope_nodes WHERE id = $3::uuid AND org_id = $1))
		 ORDER BY d.scope_path, d.role_id, d.subject_id`,
		orgID, cause, causeNode)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list derived scope grants")
	}
	defer rows.Close()
	var out []*gen.ScopeGrant
	for rows.Next() {
		grant := &gen.ScopeGrant{OrgId: orgID}
		var kind string
		if err := rows.Scan(&grant.Id, &grant.SubjectId, &kind, &grant.ScopePath, &grant.RoleId,
			&grant.DerivedFromGrantId); err != nil {
			return nil, w.Wrapf(err, "failed to scan a derived scope grant")
		}
		grant.SubjectKind = gen.SubjectKind_SUBJECT_KIND_PRINCIPAL
		if kind == "team" {
			grant.SubjectKind = gen.SubjectKind_SUBJECT_KIND_TEAM
		}
		out = append(out, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "failed iterating derived scope grants")
	}
	return out, nil
}

// errNoSuchRole keeps a missing role distinguishable from a read failure for the
// callers below, which have different answers for the two.
var errNoSuchRole = errors.New("role not found")

// derivedRoleProvenance reads the provenance of a role, or reports that the role
// does not exist. Used by the guards that must refuse a derived role.
func derivedRoleProvenance(ctx context.Context, executor QueryExecutor, roleID string) (string, error) {
	var derivedFrom *string
	if err := executor.QueryRow(ctx, `SELECT derived_from FROM roles WHERE id = $1`, roleID).Scan(&derivedFrom); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errNoSuchRole
		}
		return "", err
	}
	if derivedFrom == nil {
		return "", nil
	}
	return *derivedFrom, nil
}
