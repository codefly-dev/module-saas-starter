package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	"api/pkg/gen"
)

// ListRoles returns global built-in roles + org-specific custom roles.
func (s *Service) ListRoles(ctx context.Context, req *gen.ListRolesRequest) (*gen.ListRolesResponse, error) {
	w := wool.Get(ctx).In("ListRoles")

	roles, err := s.store.ListRoles(ctx, req.OrgId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list roles")
	}
	return &gen.ListRolesResponse{Roles: roles}, nil
}

// DeleteRole deletes a custom role (built-in roles cannot be deleted).
func (s *Service) DeleteRole(ctx context.Context, actorID string, req *gen.DeleteRoleRequest) error {
	w := wool.Get(ctx).In("DeleteRole")

	if err := s.store.DeleteRole(ctx, req.Id); err != nil {
		return w.Wrapf(err, "cannot delete role")
	}

	s.emit(ctx, actorID, "user", "role.deleted", "role", req.Id, "")
	return nil
}

// RevokeRole removes a role assignment.
func (s *Service) RevokeRole(ctx context.Context, actorID string, req *gen.RevokeRoleRequest) error {
	w := wool.Get(ctx).In("RevokeRole")

	if err := s.store.RevokeRole(ctx, req.SubjectId, req.RoleId, req.OrgId, req.Scope); err != nil {
		return w.Wrapf(err, "cannot revoke role")
	}

	s.emit(ctx, actorID, "user", "role.revoked", "role", req.RoleId, req.OrgId)
	return nil
}
