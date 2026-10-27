package adapters

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestModuleInstallationRefusalsKeepConsentAndAuthorityReasonsAndLogCause(t *testing.T) {
	consent, err := status.New(codes.FailedPrecondition, business.ErrModuleInstallationConsentRequired.Error()).WithDetails(
		&errdetails.ErrorInfo{Reason: business.ModuleInstallationConsentRequiredReason, Domain: "saas.accounts.v1"})
	require.NoError(t, err)
	for _, procedure := range []string{"WorkContext read oracle", "host.authority.v1.AuthorityService/Delegate"} {
		for _, tc := range []struct {
			name    string
			cause   error
			code    codes.Code
			message string
		}{
			{"not installed", fmt.Errorf("%w: example-secret", business.ErrModuleInstallationInactive), codes.PermissionDenied, "not installed"},
			{"consent required", consent.Err(), codes.FailedPrecondition, "InstallationService/Install"},
			{"authority unreadable", business.ErrModuleAuthorityUnreadable, codes.InvalidArgument, "cannot be read"},
			{"tenant moved", business.ErrModuleTenantNotCurrent, codes.PermissionDenied, "not installed"},
			{"authority unavailable", status.Error(codes.Unavailable, "cannot re-read live module authority: database offline"), codes.Unavailable, "database offline"},
		} {
			t.Run(procedure+"/"+tc.name, func(t *testing.T) {
				ctx, logs := capturingContext(metadata.NewIncomingContext(context.Background(), metadata.Pairs(workContextHeader, "example-secret")))
				got := moduleInstallationRefusal(ctx, procedure, tc.cause, status.Error(codes.PermissionDenied, "not installed"))
				require.Equal(t, tc.code, status.Code(got))
				require.ErrorContains(t, got, tc.message)
				if tc.name == "consent required" {
					require.Equal(t, consent.Details(), status.Convert(got).Details())
				}
				_, fields := logs.only(t)
				require.Equal(t, procedure, fields["procedure"])
				require.NotEmpty(t, fields["cause"])
				require.NotContains(t, fields["cause"], "example-secret")
				if errors.Is(tc.cause, business.ErrModuleInstallationInactive) {
					require.Contains(t, fields["cause"], business.ErrModuleInstallationInactive.Error())
					require.Contains(t, fields["cause"], "[redacted credential]")
				} else {
					require.Equal(t, tc.cause.Error(), fields["cause"])
				}
			})
		}
	}
}
