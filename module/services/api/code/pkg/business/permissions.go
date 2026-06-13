package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	"api/pkg/gen"
)

// ListRoles returns global built-in roles + org-specific custom roles.
//
// Built-in roles (org_id=NULL) are globally readable under RLS thanks
// to the polymorphic policy on `roles`; tenant rows still require
// either WithOrgTx or bypass. We wrap in WithOrgTx when an org is
// requested so tenant rows come through, and in WithBypass for the
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
		err = s.store.WithBypass(ctx, wrap)
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
// a lookup. Handler authz already required platform_admin (see
// adapters/rpcs.go DeleteRole), so the caller is privileged-by-policy
// and WithBypass is the right wrapper.
func (s *Service) DeleteRole(ctx context.Context, actorID string, req *gen.DeleteRoleRequest) error {
	w := wool.Get(ctx).In("DeleteRole")

	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		return s.store.DeleteRole(ctx, req.Id)
	}); err != nil {
		return w.Wrapf(err, "cannot delete role")
	}

	s.emit(ctx, actorID, "user", "role.deleted", "role", req.Id, "")
	return nil
}

// ListRoleAssignments returns the assignments in an org. Always
// runs under WithOrgTx — the proto requires org_id (no platform-
// admin global view yet; if needed later, route req.OrgId == "" via
// WithBypass with platform-admin handler authz).
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

// RevokeRole removes a role assignment. Org-scoped revocations go
// through WithOrgTx; platform-level revocations (req.OrgId == "")
// require platform_admin (handler-enforced) and run under bypass.
func (s *Service) RevokeRole(ctx context.Context, actorID string, req *gen.RevokeRoleRequest) error {
	w := wool.Get(ctx).In("RevokeRole")

	wrap := func(ctx context.Context) error {
		return s.store.RevokeRole(ctx, req.SubjectId, req.RoleId, req.OrgId, req.Scope)
	}
	var err error
	if req.OrgId == "" {
		err = s.store.WithBypass(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, req.OrgId, wrap)
	}
	if err != nil {
		return w.Wrapf(err, "cannot revoke role")
	}

	s.emit(ctx, actorID, "user", "role.revoked", "role", req.RoleId, req.OrgId)
	return nil
}
