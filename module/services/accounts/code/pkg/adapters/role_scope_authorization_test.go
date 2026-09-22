package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestGlobalRoleScopeRequiresSuperAdmin(t *testing.T) {
	for _, role := range []string{"", "support", "billing", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			installLayeredAuthzService(t, &platformAdminAuthzStore{role: role})
			ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
			err := requireRoleScope(ctx, platformActorID, "")
			if role == "super_admin" {
				require.NoError(t, err)
			} else {
				require.Equal(t, codes.PermissionDenied, status.Code(err))
			}
		})
	}
}

func TestGlobalRoleMutationHandlersDenyLowerPlatformRoles(t *testing.T) {
	handlers := []struct {
		name   string
		invoke func(context.Context, *PermServer) error
	}{
		{"create", func(ctx context.Context, srv *PermServer) error {
			_, err := srv.CreateRole(ctx, &gen.CreateRoleRequest{Name: "Example role"})
			return err
		}},
		{"update", func(ctx context.Context, srv *PermServer) error {
			_, err := srv.UpdateRole(ctx, &gen.UpdateRoleRequest{Id: platformTargetD})
			return err
		}},
		{"delete", func(ctx context.Context, srv *PermServer) error {
			_, err := srv.DeleteRole(ctx, &gen.DeleteRoleRequest{Id: platformTargetD})
			return err
		}},
		{"assign", func(ctx context.Context, srv *PermServer) error {
			_, err := srv.AssignRole(ctx, &gen.AssignRoleRequest{RoleId: platformTargetD, SubjectId: platformActorID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL})
			return err
		}},
		{"revoke", func(ctx context.Context, srv *PermServer) error {
			_, err := srv.RevokeRole(ctx, &gen.RevokeRoleRequest{RoleId: platformTargetD, SubjectId: platformActorID})
			return err
		}},
	}
	for _, role := range []string{"", "support", "billing"} {
		for _, handler := range handlers {
			t.Run(role+"/"+handler.name, func(t *testing.T) {
				// All write methods on this store are deliberately unimplemented:
				// reaching one instead of denying the request fails the test.
				installLayeredAuthzService(t, &platformAdminAuthzStore{role: role})
				ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
				err := handler.invoke(ctx, &PermServer{})
				require.Equal(t, codes.PermissionDenied, status.Code(err))
			})
		}
	}
}
