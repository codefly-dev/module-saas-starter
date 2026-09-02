package business

import (
	"testing"

	policyv1 "accounts/pkg/gen/saas/policy/v1"

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

// A method that binds an organization or team resource requires the
// interceptor to resolve and check that binding, which it cannot do yet — so
// even with a self-service tenant and no declared permission it is a gap, not
// ok. Without this the classifier would report "safe to enforce centrally" for
// a method whose bound org/team field the interceptor never validates.
func TestBoundOrgResourceIsNeverFalselyOK(t *testing.T) {
	policy := RPCPolicy{MethodPolicy: &policyv1.MethodPolicy{
		Exposure: policyv1.Exposure_EXPOSURE_AUTHENTICATED,
		Tenant:   policyv1.TenantRequirement_TENANT_REQUIREMENT_USER,
		ResourceBindings: []*policyv1.ResourceBinding{{
			RequestField: "org_id",
			Target:       policyv1.ResourceTarget_RESOURCE_TARGET_ORGANIZATION,
			Lookup:       policyv1.ResourceLookup_RESOURCE_LOOKUP_DIRECT_ID,
		}},
	}}
	require.Equal(t, CoverageGap, ClassifyCentralCoverage(policy))

	// The same shape with an owned-resource binding cannot even resolve the org.
	policy.MethodPolicy.ResourceBindings[0].Target = policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE
	require.Equal(t, CoverageUnsupported, ClassifyCentralCoverage(policy))
}

// Across the real catalog, no method carrying an organization/team/owned
// resource binding may classify ok: such a binding always implies a
// server-side resolution the interceptor does not perform. This guards the
// invariant that today holds only because every bound method also carries an
// org tenant — a future method that drops the tenant must not slip to ok.
func TestResourceBoundMethodsAreNeverOK(t *testing.T) {
	for _, policy := range RPCPolicies() {
		if !policy.Tier.Valid() || policy.PolicyError != "" {
			continue
		}
		bound := false
		for _, binding := range policy.MethodPolicy.GetResourceBindings() {
			switch binding.GetTarget() {
			case policyv1.ResourceTarget_RESOURCE_TARGET_ORGANIZATION,
				policyv1.ResourceTarget_RESOURCE_TARGET_TEAM,
				policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE:
				bound = true
			}
		}
		if bound {
			require.NotEqualf(t, CoverageOK, ClassifyCentralCoverage(policy),
				"%s binds a resource but classifies ok", policy.FullMethod)
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
