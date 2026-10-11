package adapters

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	exposureInstallationID = "019fec91-3000-7000-8000-0000000000b1"
	exposureTeamID         = "019fec91-3000-7000-8000-0000000000b2"
	exposureRoleID         = "019fec91-3000-7000-8000-0000000000b3"
	exposureOtherOrgID     = "019fec91-3000-7000-8000-0000000000b4"
)

// The three org-admin exposure doors, called the way a browser reaches them.
func exposureCalls() map[string]func(ctx context.Context, orgID string) error {
	server := &InstallationServer{}
	return map[string]func(ctx context.Context, orgID string) error{
		"/saas.accounts.v1.InstallationService/ListOrganizationInstallations": func(ctx context.Context, orgID string) error {
			_, err := server.ListOrganizationInstallations(ctx, &gen.ListOrganizationInstallationsRequest{OrgId: orgID})
			return err
		},
		"/saas.accounts.v1.InstallationService/GrantInstallationToTeam": func(ctx context.Context, orgID string) error {
			_, err := server.GrantInstallationToTeam(ctx, &gen.GrantInstallationToTeamRequest{
				OrgId: orgID, InstallationId: exposureInstallationID, TeamId: exposureTeamID, RoleId: exposureRoleID,
			})
			return err
		},
		"/saas.accounts.v1.InstallationService/RevokeInstallationFromTeam": func(ctx context.Context, orgID string) error {
			_, err := server.RevokeInstallationFromTeam(ctx, &gen.RevokeInstallationFromTeamRequest{
				OrgId: orgID, InstallationId: exposureInstallationID, TeamId: exposureTeamID, RoleId: exposureRoleID,
			})
			return err
		},
	}
}

// A member who is not an organization administrator is refused by both gates —
// the central admission check and the handler's own — before anything is read.
func TestInstallationExposureRequiresOrganizationAdmin(t *testing.T) {
	enableEnforcement(t)
	for method, call := range exposureCalls() {
		t.Run(method, func(t *testing.T) {
			installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
			require.Equal(t, codes.PermissionDenied, status.Code(enforceCentralPolicy(enforceActorCtx(), method)))
			require.Equal(t, codes.PermissionDenied, status.Code(call(enforceActorCtx(), enforceOrgID)))

			installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_ADMIN))
			require.NoError(t, enforceCentralPolicy(enforceActorCtx(), method))
		})
	}
}

// An administrator of one organization naming another is refused: the request's
// organization is pinned to the verified one, so no organization lists or
// changes another's installations.
func TestInstallationExposureRefusesAnotherOrganization(t *testing.T) {
	for method, call := range exposureCalls() {
		t.Run(method, func(t *testing.T) {
			installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_OWNER))
			err := call(enforceActorCtx(), exposureOtherOrgID)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Equal(t, "requested organization is outside the authenticated scope", status.Convert(err).Message())
		})
	}
}

func TestInstallationExposureErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		// Another organization's installation id resolves to nothing under RLS.
		{"unknown installation", business.NewStoreError(errors.New("installation x is not an active installation in this organization"), business.ErrTypeNotFound), codes.NotFound},
		{"team outside organization", fmt.Errorf("cannot expose installation to team: %w", business.ErrSubjectOutsideOrganization), codes.PermissionDenied},
		{"role exposes nothing", business.NewStoreError(business.ErrRoleDoesNotExposeSolution, business.ErrTypeValidation), codes.InvalidArgument},
		// The host's own refusal of an unwitnessed narrowing keeps its words.
		{"unwitnessed narrowing", fmt.Errorf("cannot revoke scope: %w: this host has no policy log, so authority cannot be narrowed", business.ErrPolicyLogUnreachable), codes.FailedPrecondition},
		{"store status", fmt.Errorf("cannot grant scope: %w", status.Error(codes.FailedPrecondition, `role "r" is not a role in this org`)), codes.FailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapInstallationExposureError(tc.err)
			require.Equal(t, tc.want, status.Code(mapped))
			require.Equal(t, tc.err.Error(), status.Convert(mapped).Message())
		})
	}
}
