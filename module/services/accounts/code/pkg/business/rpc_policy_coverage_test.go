package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every method the interceptors admit must classify into one of the three
// static coverage outcomes. broadening is an enforce-stage runtime signal and
// must never come from static classification, or the shadow measurement would
// report a phantom regression against real traffic.
func TestClassifyCentralCoverageIsExhaustiveAndNeverBroadening(t *testing.T) {
	for _, policy := range RPCPolicies() {
		if !policy.Tier.Valid() || policy.PolicyError != "" {
			continue
		}
		coverage := ClassifyCentralCoverage(policy)
		switch coverage {
		case CoverageOK, CoverageGap, CoverageUnsupported:
		default:
			t.Fatalf("%s classified as %q", policy.FullMethod, coverage)
		}
	}
}

func TestClassifyCentralCoverageBuckets(t *testing.T) {
	cases := map[string]CentralCoverage{
		// Public and internal exposure is fully enforced by the interceptor.
		"/saas.accounts.v1.UserService/Version":          CoverageOK,
		"/saas.accounts.v1.UserService/RegisterUser":     CoverageOK,
		"/saas.accounts.v1.APIKeyService/ValidateAPIKey": CoverageOK,
		// tenant USER with no permission reduces to "a verified user" + RLS.
		"/saas.accounts.v1.UserService/GetUser": CoverageOK,
		// The declaration-bug fix removes the unresolvable users:write
		// permission, so these self-service writes are centrally enforceable.
		"/saas.accounts.v1.UserService/UpdateUser": CoverageOK,
		"/saas.accounts.v1.UserService/DeleteUser": CoverageOK,
		// org-admin tenant is a real requirement the interceptor does not yet
		// resolve; a require* handler site covers it today.
		"/saas.accounts.v1.APIKeyService/CreateAPIKey": CoverageGap,
		// Owned-resource bindings resolve an org through a lookup the
		// interceptor does not have.
		"/saas.accounts.v1.InvitationService/RevokeInvitation": CoverageUnsupported,
		"/saas.accounts.v1.WebhookService/DeleteSubscription":  CoverageUnsupported,
	}
	for method, want := range cases {
		policy, ok := LookupRPCPolicy(method)
		require.True(t, ok, "%s is not classified by the policy catalog", method)
		require.Equalf(t, want, ClassifyCentralCoverage(policy), "coverage for %s", method)
	}
}

// The eight invitation and webhook owned-resource methods the epic calls out
// stay unsupported until an ownership resolver exists.
func TestOwnedResourceMethodsAreUnsupported(t *testing.T) {
	for _, method := range []string{
		"/saas.accounts.v1.InvitationService/ResendInvitation",
		"/saas.accounts.v1.InvitationService/RevokeInvitation",
		"/saas.accounts.v1.WebhookService/DeleteSubscription",
		"/saas.accounts.v1.WebhookService/GetDelivery",
		"/saas.accounts.v1.WebhookService/ListDeliveries",
		"/saas.accounts.v1.WebhookService/ReplayDelivery",
		"/saas.accounts.v1.WebhookService/RotateSecret",
		"/saas.accounts.v1.WebhookService/TestWebhook",
	} {
		policy, ok := LookupRPCPolicy(method)
		require.True(t, ok, method)
		require.Equalf(t, CoverageUnsupported, ClassifyCentralCoverage(policy), method)
	}
}

// Removing the org-scoped users:write permission is what pulls UpdateUser and
// DeleteUser out of the gap bucket; without the fix they read as gap. This
// pins the declaration so a regression that re-adds the permission is caught.
func TestSelfServiceUserWritesDeclareNoOrgPermission(t *testing.T) {
	for _, method := range []string{
		"/saas.accounts.v1.UserService/UpdateUser",
		"/saas.accounts.v1.UserService/DeleteUser",
	} {
		policy, ok := LookupRPCPolicy(method)
		require.True(t, ok, method)
		require.Emptyf(t, policy.MethodPolicy.GetPermissions(), "%s must not declare an org permission for a tenant USER method", method)
	}
}
