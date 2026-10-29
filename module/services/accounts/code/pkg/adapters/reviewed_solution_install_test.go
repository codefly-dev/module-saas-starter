package adapters

import (
	"context"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCombinedInstallationRPCsRequireCurrentOrganizationAdministrator(t *testing.T) {
	enableEnforcement(t)
	s := &InstallationServer{}
	review := &gen.ReviewSolutionInstallRequest{OrgId: enforceOrgID, TargetId: installID}
	install := &gen.InstallReviewedSolutionRequest{OrgId: enforceOrgID, Review: &gen.ReviewedSolutionInstall{}}
	for _, invoke := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := s.ReviewSolutionInstall(ctx, review); return err },
		func(ctx context.Context) error { _, err := s.InstallReviewedSolution(ctx, install); return err },
	} {
		require.Equal(t, codes.Unauthenticated, status.Code(invoke(context.Background())))
		installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
		require.Equal(t, codes.PermissionDenied, status.Code(invoke(enforceActorCtx())))
		installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_ADMIN))
		review.OrgId, install.OrgId = exposureOtherOrgID, exposureOtherOrgID
		require.Equal(t, codes.PermissionDenied, status.Code(invoke(enforceActorCtx())))
		review.OrgId, install.OrgId = enforceOrgID, enforceOrgID
	}
	for _, method := range []string{"ReviewSolutionInstall", "InstallReviewedSolution"} {
		require.NoError(t, enforceCentralPolicy(enforceActorCtx(), "/saas.accounts.v1.InstallationService/"+method))
	}
}
