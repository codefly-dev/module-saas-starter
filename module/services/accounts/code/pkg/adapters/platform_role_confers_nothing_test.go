package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A platform super_admin who is not a member of an organization administers
// none of it: every organization gate asks for membership, and refuses by name.
// The gates used to wave a super_admin through; this fails against that code.
func TestPlatformSuperAdminAdministersNoOrganizationItIsNotAMemberOf(t *testing.T) {
	store := targetTenantStore()
	store.platformRoles[supportActorID] = "super_admin"
	installLayeredAuthzService(t, store)

	identity, err := auth.ParseRequestIdentity(supportActorID, supportActorID, targetOrgID, "")
	require.NoError(t, err)
	ctx := stampRequestIdentity(context.Background(), identity, auth.Assurance{})

	for name, gate := range map[string]func() error{
		"organization admin": func() error { return requireOrgAdmin(ctx, supportActorID, targetOrgID) },
		"organization owner": func() error { return requireOrgOwner(ctx, supportActorID, targetOrgID) },
		"billing admin":      func() error { return requireBillingAdmin(ctx, supportActorID, targetOrgID) },
		"audit log read": func() error {
			_, err := (&AuditServer{}).QueryAuditLog(ctx, &gen.QueryAuditLogRequest{OrgId: targetOrgID, PageSize: 5})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := gate()
			require.Equal(t, codes.PermissionDenied, status.Code(err), "a platform role is not membership: %v", err)
			require.Contains(t, status.Convert(err).Message(), "not a member of this organization",
				"the refusal names why")
		})
	}
}
