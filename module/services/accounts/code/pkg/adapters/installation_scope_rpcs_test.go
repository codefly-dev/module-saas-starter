package adapters

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNarrowInstallationRequiresOrgAdminAndOwnOrganization(t *testing.T) {
	enableEnforcement(t)
	const method = "/saas.accounts.v1.InstallationService/NarrowInstallation"
	server := &InstallationServer{}
	request := &gen.NarrowInstallationRequest{OrgId: enforceOrgID, InstallationId: installID, Scope: `{}`}
	_, err := server.NarrowInstallation(context.Background(), request)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
	require.Equal(t, codes.PermissionDenied, status.Code(enforceCentralPolicy(enforceActorCtx(), method)))
	_, err = server.NarrowInstallation(enforceActorCtx(), request)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_ADMIN))
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), method))
	request.OrgId = exposureOtherOrgID
	_, err = server.NarrowInstallation(enforceActorCtx(), request)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	request.OrgId = enforceOrgID
	request.Scope = `{"tenant":"foreign"}`
	_, err = server.NarrowInstallation(enforceActorCtx(), request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestInstallationNarrowingErrorMapping(t *testing.T) {
	require.Equal(t, codes.Unavailable, status.Code(mapInstallationError(business.ErrPolicyLogUnreachable)))
	require.Equal(t, codes.PermissionDenied, status.Code(mapInstallationError(status.Error(codes.PermissionDenied, "outside ceiling"))))
	// A wrapped status keeps its code and answers in its own words, never as
	// "cannot retry installation: rpc error: code = … desc = …".
	wrapped := mapInstallationError(fmt.Errorf("cannot retry installation: %w",
		status.Error(codes.FailedPrecondition, "the installation is not in a failed state")))
	require.Equal(t, codes.FailedPrecondition, status.Code(wrapped))
	require.Equal(t, "the installation is not in a failed state", status.Convert(wrapped).Message())
}

// HOST-INST-005: retry is an administration act, never an unauthenticated
// resubmission or a way to operate on another organization.
func TestRetryInstallationRequiresOrgAdmin(t *testing.T) {
	enableEnforcement(t)
	const method = "/saas.accounts.v1.InstallationService/RetryInstallation"
	s := &InstallationServer{}
	req := &gen.RetryInstallationRequest{OrgId: enforceOrgID, InstallationId: installID}
	_, err := s.RetryInstallation(context.Background(), req)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
	_, err = s.RetryInstallation(enforceActorCtx(), req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_ADMIN))
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), method))
	req.OrgId = exposureOtherOrgID
	_, err = s.RetryInstallation(enforceActorCtx(), req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
