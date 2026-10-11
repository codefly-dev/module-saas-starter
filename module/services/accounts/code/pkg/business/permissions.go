package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ListRoles returns global built-in roles + org-specific custom roles.
//
// Built-in roles (org_id=NULL) are globally readable under RLS thanks
// to the polymorphic policy on `roles`; tenant rows still require
// either WithOrgTx or bypass. We wrap in WithOrgTx when an org is
// requested so tenant rows come through, and in WithControlPlane for the
// global-only ListRoles("") path (callers wanting just the built-ins).
func (s *Service) ListRoles(ctx context.Context, req *gen.ListRolesRequest) (*gen.ListRolesResponse, error) {
	w := wool.Get(ctx).In("ListRoles")

	var roles []*gen.Role
	wrap := func(ctx context.Context) error {
		rs, err := s.store.ListRoles(ctx, req.OrgId)
		roles = rs
		return err
	}
	var err error
	if req.OrgId == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, req.OrgId, wrap)
	}
	if err != nil {
		return nil, w.Wrapf(err, "cannot list roles")
	}
	return &gen.ListRolesResponse{Roles: roles}, nil
}

// DeleteRole deletes a custom role (built-in roles cannot be deleted).
//
// req only carries the role id — we don't know the role's org without
// a lookup. Handler authz already required platform super_admin (see
// adapters/rpcs.go DeleteRole), so the caller is privileged-by-policy
// and WithControlPlane is the right wrapper.
func (s *Service) DeleteRole(ctx context.Context, actorID string, req *gen.DeleteRoleRequest) error {
	w := wool.Get(ctx).In("DeleteRole")

	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := s.store.DeleteRole(ctx, req.Id); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventRoleDeleted, "role", req.Id, "")
	}); err != nil {
		return w.Wrapf(err, "cannot delete role")
	}
	return nil
}

// UpdateRole replaces a custom role's description and permission set. The
// scope wrapper mirrors CreateRole: a global role is a platform write and
// runs under the control plane, an org role runs inside its tenant
// transaction so RLS holds the boundary alongside the store's own scope
// predicate.
func (s *Service) UpdateRole(ctx context.Context, actorID string, req *gen.UpdateRoleRequest) (*gen.UpdateRoleResponse, error) {
	w := wool.Get(ctx).In("UpdateRole")

	var role *gen.Role
	wrap := func(ctx context.Context) error {
		updated, err := s.store.UpdateRole(ctx, req.Id, req.OrgId, req.Description, req.Permissions)
		if err != nil {
			return err
		}
		role = updated
		return s.emitTx(ctx, actorID, "user", EventRoleUpdated, "role", role.Id, req.OrgId, map[string]any{
			"name":        role.Name,
			"permissions": permissionLabels(role.Permissions),
		})
	}
	var err error
	if req.OrgId == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, req.OrgId, wrap)
	}
	if err != nil {
		return nil, w.Wrapf(err, "cannot update role")
	}
	return &gen.UpdateRoleResponse{Role: role}, nil
}

// permissionLabels renders a permission set for the audit payload, which
// carries strings rather than structured grants.
func permissionLabels(permissions []*gen.Permission) []string {
	labels := make([]string, 0, len(permissions))
	for _, p := range permissions {
		labels = append(labels, p.Resource+":"+p.Action)
	}
	return labels
}

// ListRoleAssignments returns the assignments in an org. Always
// runs under WithOrgTx — the proto requires org_id (no platform-
// admin global view yet; if needed later, route req.OrgId == "" via
// WithControlPlane with platform-admin handler authz).
func (s *Service) ListRoleAssignments(ctx context.Context, req *gen.ListRoleAssignmentsRequest) (*gen.ListRoleAssignmentsResponse, error) {
	w := wool.Get(ctx).In("ListRoleAssignments")
	var assignments []*gen.RoleAssignment
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		as, err := s.store.ListRoleAssignments(ctx, req.OrgId, req.SubjectId, req.SubjectKind)
		assignments = as
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list role assignments")
	}
	return &gen.ListRoleAssignmentsResponse{Assignments: assignments}, nil
}

// RevokeRole removes a role assignment.
//
// A NARROWING, so it runs under the policy log: the entry is appended to the
// external record and receipted here BEFORE the assignment goes, and the delete
// commits in the same transaction as the receipt's commit. A host that cannot
// witness the append refuses rather than revoking unwitnessed — a revocation a
// restore could silently undo is worse than a refusal the caller can see.
//
// The transaction is the policy log's control-plane one for BOTH the
// organization-scoped and the platform-level revocation, where before this the
// first ran in its tenant transaction and the second under the control plane.
// The receipt relation is control-plane only and the two writes have to be
// atomic, so there is no choice of transaction left to make; what the tenant one
// used to check is checked by the statement instead, which names req.OrgId and
// matches it with IS NOT DISTINCT FROM — so a caller naming another
// organization's assignment deletes nothing, exactly as the tenant policy would
// have produced.
func (s *Service) RevokeRole(ctx context.Context, actorID string, req *gen.RevokeRoleRequest) error {
	w := wool.Get(ctx).In("RevokeRole")

	if err := s.WithPolicyLoggedNarrowing(ctx,
		revokeRolePolicyLogEntry(actorID, req.OrgId, req.SubjectId, req.RoleId, req.Scope),
		func(ctx context.Context) error {
			if err := s.store.RevokeRole(ctx, req.SubjectId, req.RoleId, req.OrgId, req.Scope); err != nil {
				return err
			}
			return s.emitTx(ctx, actorID, "user", EventRoleRevoked, "role", req.RoleId, req.OrgId)
		}); err != nil {
		return w.Wrapf(err, "cannot revoke role")
	}
	return nil
}
