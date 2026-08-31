package infra

import (
	"context"
	"fmt"
	"strconv"

	"accounts/pkg/business"
	"accounts/pkg/rolecatalog"

	"github.com/codefly-dev/core/wool"
)

// ImportOptions controls how a catalog is reconciled against the database.
type ImportOptions struct {
	// DryRun computes and returns the plan without writing anything.
	DryRun bool
	// Force permits removals that cascade away existing role assignments.
	Force bool
	// Source is an optional provenance label (e.g. the catalog file path)
	// recorded on every audit event this import emits.
	Source string
}

// ImportResult reports the outcome of an import.
type ImportResult struct {
	Plan *rolecatalog.Plan
	// Applied is true when changes were written and committed.
	Applied bool
	// Refused is true when a safety guard blocked the import and Force was not
	// set; no changes were written. RefusalReason explains which guard fired.
	Refused       bool
	RefusalReason string
}

// ImportRoleCatalog reconciles the built-in role catalog toward cat. It runs
// under the audited control-plane role: built-in roles (org_id IS NULL) can only
// be written with RLS bypassed (migrations 32, 65), and cross-tenant assignment
// counts must be visible for the orphan guard.
//
// The whole reconciliation — snapshot, plan, apply, and audit — happens in one
// transaction so a same-catalog rerun is a true no-op and a failure leaves no
// partial state.
func (s *PostgresStore) ImportRoleCatalog(ctx context.Context, cat *rolecatalog.Catalog, opts ImportOptions) (*ImportResult, error) {
	w := wool.Get(ctx).In("ImportRoleCatalog")
	result := &ImportResult{}

	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		state, err := s.snapshotBuiltinRoles(ctx)
		if err != nil {
			return err
		}
		plan := rolecatalog.Diff(state, cat)
		result.Plan = plan

		if opts.DryRun {
			return nil
		}
		if reason, refused := refuseImport(cat, plan, opts.Force); refused {
			result.Refused = true
			result.RefusalReason = reason
			return nil
		}
		if err := s.applyRoleCatalogPlan(ctx, plan, catalogProvenance(cat, opts.Source)); err != nil {
			return err
		}
		result.Applied = true
		return nil
	})
	if err != nil {
		return nil, w.Wrapf(err, "failed to import role catalog")
	}
	return result, nil
}

// refuseImport decides whether a safety guard blocks applying the plan. Force
// overrides every guard. Two destructive shapes are refused:
//
//   - A catalog that declares no roles at all would remove EVERY catalog-managed
//     role. That is almost always a truncated or empty file rather than a
//     deliberate "delete everything", and the orphan guard below does not catch
//     it for roles that happen to have no assignments. Require an explicit Force
//     so the wipe is a deliberate act, not an accident.
//   - Any removal that would cascade away existing role_assignments.
func refuseImport(cat *rolecatalog.Catalog, plan *rolecatalog.Plan, force bool) (string, bool) {
	if force {
		return "", false
	}
	if len(cat.Roles) == 0 && len(plan.Removes) > 0 {
		return fmt.Sprintf(
			"catalog declares no roles; applying it would remove all %d catalog-managed role(s). Rerun with force to confirm.",
			len(plan.Removes)), true
	}
	if orphaning := plan.OrphaningRemovals(); len(orphaning) > 0 {
		return fmt.Sprintf(
			"%d role removal(s) would orphan existing assignments. Rerun with force to apply.",
			len(orphaning)), true
	}
	return "", false
}

// snapshotBuiltinRoles reads every built-in role (org_id IS NULL) with its
// permission set, plus the assignment count for catalog-managed roles (the only
// ones a removal can target). Must run inside the control-plane transaction.
func (s *PostgresStore) snapshotBuiltinRoles(ctx context.Context) (rolecatalog.State, error) {
	w := wool.Get(ctx).In("snapshotBuiltinRoles")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT r.id, r.name, r.description, r.scope, r.catalog_managed,
		       rp.resource, rp.action
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		WHERE r.org_id IS NULL AND r.built_in = true
		ORDER BY r.name, rp.resource, rp.action`)
	if err != nil {
		return rolecatalog.State{}, w.Wrapf(err, "failed to snapshot built-in roles")
	}
	defer rows.Close()

	byID := make(map[string]*rolecatalog.StateRole)
	var order []string
	for rows.Next() {
		var (
			id, name           string
			description, scope *string
			catalogManaged     bool
			resource, action   *string
		)
		if err := rows.Scan(&id, &name, &description, &scope, &catalogManaged, &resource, &action); err != nil {
			return rolecatalog.State{}, w.Wrapf(err, "failed to scan role row")
		}
		role, seen := byID[id]
		if !seen {
			role = &rolecatalog.StateRole{ID: id, Name: name, CatalogManaged: catalogManaged}
			if description != nil {
				role.Description = *description
			}
			if scope != nil {
				role.Scope = *scope
			}
			byID[id] = role
			order = append(order, id)
		}
		if resource != nil && action != nil {
			role.Permissions = append(role.Permissions, rolecatalog.Permission{Resource: *resource, Action: *action})
		}
	}
	if err := rows.Err(); err != nil {
		return rolecatalog.State{}, w.Wrapf(err, "failed iterating role rows")
	}

	if err := s.attachAssignmentCounts(ctx, byID); err != nil {
		return rolecatalog.State{}, err
	}

	state := rolecatalog.State{Roles: make([]rolecatalog.StateRole, 0, len(order))}
	for _, id := range order {
		state.Roles = append(state.Roles, *byID[id])
	}
	return state, nil
}

func (s *PostgresStore) attachAssignmentCounts(ctx context.Context, byID map[string]*rolecatalog.StateRole) error {
	w := wool.Get(ctx).In("attachAssignmentCounts")
	executor := s.getQueryExecutor(ctx)

	managed := make([]string, 0, len(byID))
	for id, role := range byID {
		if role.CatalogManaged {
			managed = append(managed, id)
		}
	}
	if len(managed) == 0 {
		return nil
	}

	rows, err := executor.Query(ctx, `
		SELECT role_id, COUNT(*)
		FROM role_assignments
		WHERE role_id = ANY($1::uuid[])
		GROUP BY role_id`, managed)
	if err != nil {
		return w.Wrapf(err, "failed to count role assignments")
	}
	defer rows.Close()

	for rows.Next() {
		var roleID string
		var count int
		if err := rows.Scan(&roleID, &count); err != nil {
			return w.Wrapf(err, "failed to scan assignment count")
		}
		if role, ok := byID[roleID]; ok {
			role.AssignmentCount = count
		}
	}
	if err := rows.Err(); err != nil {
		return w.Wrapf(err, "failed iterating assignment counts")
	}
	return nil
}

// catalogProvenance builds the provenance stamped onto every audit event of an
// import: the catalog fingerprint always, and the caller-supplied source label
// when present.
func catalogProvenance(cat *rolecatalog.Catalog, source string) map[string]string {
	provenance := map[string]string{"catalog_sha256": cat.Fingerprint()}
	if source != "" {
		provenance["catalog_source"] = source
	}
	return provenance
}

// applyRoleCatalogPlan writes the plan's changes and emits one system audit
// event per changed role. Runs inside the control-plane transaction opened by
// ImportRoleCatalog.
func (s *PostgresStore) applyRoleCatalogPlan(ctx context.Context, plan *rolecatalog.Plan, provenance map[string]string) error {
	w := wool.Get(ctx).In("applyRoleCatalogPlan")
	executor := s.getQueryExecutor(ctx)

	for _, create := range plan.Creates {
		roleID := business.NewIDString()
		if _, err := executor.Exec(ctx, `
			INSERT INTO roles (id, name, description, built_in, org_id, scope, catalog_managed, created_at)
			VALUES ($1, $2, $3, true, NULL, $4, true, CURRENT_TIMESTAMP)`,
			roleID, create.Role.Name, nilIfEmpty(create.Role.Description), nilIfEmpty(create.Role.Scope),
		); err != nil {
			return w.Wrapf(err, "failed to create role %q", create.Role.Name)
		}
		for _, perm := range create.Role.Permissions {
			if err := insertRolePermission(ctx, executor, roleID, perm); err != nil {
				return w.Wrapf(err, "failed to seed permission for role %q", create.Role.Name)
			}
		}
		if err := s.emitCatalogAudit(ctx, "role.created.v1", roleID, map[string]string{
			"name":              create.Role.Name,
			"permissions_added": strconv.Itoa(len(create.Role.Permissions)),
		}, provenance); err != nil {
			return err
		}
	}

	for _, update := range plan.Updates {
		if update.RowChanged() {
			if _, err := executor.Exec(ctx, `
				UPDATE roles SET description = $1, scope = $2, catalog_managed = true
				WHERE id = $3`,
				nilIfEmpty(update.DesiredDescription), nilIfEmpty(update.DesiredScope), update.RoleID,
			); err != nil {
				return w.Wrapf(err, "failed to update role %q", update.Name)
			}
		}
		for _, perm := range update.RemovePermissions {
			if _, err := executor.Exec(ctx, `
				DELETE FROM role_permissions WHERE role_id = $1 AND resource = $2 AND action = $3`,
				update.RoleID, perm.Resource, perm.Action,
			); err != nil {
				return w.Wrapf(err, "failed to remove permission from role %q", update.Name)
			}
		}
		for _, perm := range update.AddPermissions {
			if err := insertRolePermission(ctx, executor, update.RoleID, perm); err != nil {
				return w.Wrapf(err, "failed to add permission to role %q", update.Name)
			}
		}
		if err := s.emitCatalogAudit(ctx, "role.updated.v1", update.RoleID, map[string]string{
			"name":                update.Name,
			"permissions_added":   strconv.Itoa(len(update.AddPermissions)),
			"permissions_removed": strconv.Itoa(len(update.RemovePermissions)),
		}, provenance); err != nil {
			return err
		}
	}

	for _, remove := range plan.Removes {
		// role_assignments and role_permissions cascade on the role FK.
		if _, err := executor.Exec(ctx, `DELETE FROM roles WHERE id = $1`, remove.RoleID); err != nil {
			return w.Wrapf(err, "failed to remove role %q", remove.Name)
		}
		if err := s.emitCatalogAudit(ctx, "role.deleted.v1", remove.RoleID, map[string]string{
			"name":                remove.Name,
			"assignments_removed": strconv.Itoa(remove.AssignmentCount),
		}, provenance); err != nil {
			return err
		}
	}
	return nil
}

func insertRolePermission(ctx context.Context, executor QueryExecutor, roleID string, perm rolecatalog.Permission) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO role_permissions (role_id, resource, action) VALUES ($1, $2, $3)`,
		roleID, perm.Resource, perm.Action,
	)
	return err
}

// emitCatalogAudit writes one system-actor audit event with a NULL org — the
// catalog reconciles global built-in roles, which belong to no tenant. The
// enclosing control-plane transaction lets the polymorphic audit_events policy
// accept the NULL org_id.
func (s *PostgresStore) emitCatalogAudit(ctx context.Context, action, roleID string, metadata, provenance map[string]string) error {
	w := wool.Get(ctx).In("emitCatalogAudit")
	for k, v := range provenance {
		metadata[k] = v
	}
	payload := make(map[string]any, len(metadata))
	for k, v := range metadata {
		payload[k] = v
	}
	if err := s.InsertAuditEvent(ctx, business.AuditEntry{
		ActorType:  "system",
		EventType:  business.EventType(action),
		Resource:   "role",
		ResourceID: roleID,
		Payload:    payload,
	}); err != nil {
		return w.Wrapf(err, "failed to emit audit event %q", action)
	}
	return nil
}
