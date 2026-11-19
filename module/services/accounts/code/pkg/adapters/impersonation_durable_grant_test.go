package adapters

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// An impersonated session lasts minutes; a grant made from one does not. These
// are the procedures that were open to a support operator acting as someone
// else and that leave authority behind when the session ends: an installation
// and the roles and team memberships set up with it, a delegation to a module,
// a provider group bound onto a team, an invitation sent again, and a new
// organization owned by the person impersonated.
var durableGrantsOnceOpenToImpersonation = []string{
	"/saas.accounts.v1.DatasourceService/CompleteDatasourceAccountLink",
	"/saas.accounts.v1.DatasourceService/AddSource",
	"/saas.accounts.v1.TeamService/CreateTeam",
	"/saas.accounts.v1.DashboardService/ShareDashboard",
	"/saas.accounts.v1.InstallationService/InstallReviewedSolution",
	"/saas.accounts.v1.InstallationService/InstallSolution",
	"/saas.accounts.v1.InstallationService/SetUpModulePage",
	"/saas.accounts.v1.InstallationService/NarrowInstallation",
	"/saas.accounts.v1.InstallationService/RetryInstallation",
	"/saas.accounts.v1.InstallationService/TransferInstallationOwnership",
	"/saas.accounts.v1.InstallationService/UninstallSolution",
	"/saas.accounts.v1.DelegationService/RequestDelegation",
	"/saas.accounts.v1.DelegationService/DecideDelegation",
	"/saas.accounts.v1.DatasourceService/DelegateSourceTriggers",
	"/saas.accounts.v1.DatasourceService/RevokeSourceDelegation",
	"/saas.accounts.v1.DatasourceService/BindDatasourceGroup",
	"/saas.accounts.v1.DatasourceService/UnbindDatasourceGroup",
	"/saas.accounts.v1.InvitationService/ResendInvitation",
	"/saas.accounts.v1.OrganizationService/CreateOrganization",
}

func TestADurableGrantIsRefusedToAnImpersonatedSession(t *testing.T) {
	withoutCentralEnforcement(t)

	for _, procedure := range durableGrantsOnceOpenToImpersonation {
		t.Run(strings.TrimPrefix(procedure, "/saas.accounts.v1."), func(t *testing.T) {
			executed, err := callThroughInterceptor(t, impersonatingMinter(), procedure)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "unavailable to an impersonated session")
			require.False(t, executed, "admission must refuse before the handler can grant anything")

			executed, err = callThroughInterceptor(t, selfMinter(restrictionTargetID), procedure)
			require.NoError(t, err, "the person's own session keeps the procedure")
			require.True(t, executed)
		})
	}
}

// durableGrantEvents are the audit-event families that record authority given
// to, or taken from, a person, a team or a module: roles and scopes,
// memberships, installations, delegations, invitations and credentials that
// stand for a principal. An event is declared on the method that emits it, so
// the declaration a method already makes says whether it makes a durable grant.
var durableGrantEvents = []string{
	"saas.authority.role_",
	"saas.authority.installation_",
	"saas.installation.",
	"saas.role.",
	"saas.scope.",
	"saas.team.member_",
	"saas.org.member_",
	"saas.invitation.",
	"saas.delegation.",
	"saas.module_delegation.",
	"saas.datasource.delegation.",
	"saas.datasource.group_",
	"saas.api_key.",
	"saas.principal.",
	"saas.platform.role_",
}

func durableGrantEvent(event string) bool {
	for _, family := range durableGrantEvents {
		if strings.HasPrefix(event, family) {
			return true
		}
	}
	return false
}

// The one rule: a method a session calls that declares a durable-grant event is
// withheld from an impersonated session. It is held on the declaration, so the
// one admission gate that reads it (enforceImpersonationPolicy) refuses the
// call, and a new granting method cannot be added open by leaving the option
// out.
func TestEveryDurableGrantIsWithheldFromAnImpersonatedSession(t *testing.T) {
	for _, policy := range business.RPCPolicies() {
		if policy.MethodPolicy.GetExposure() != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
			continue
		}
		for _, event := range policy.MethodPolicy.GetAudit().GetEvents() {
			if !durableGrantEvent(event) {
				continue
			}
			require.Truef(t, business.ImpersonationForbidden(policy),
				"%s emits %s and is open to an impersonated session; declare impersonation: IMPERSONATION_REQUIREMENT_FORBIDDEN",
				policy.FullMethod, event)
		}
	}
}
