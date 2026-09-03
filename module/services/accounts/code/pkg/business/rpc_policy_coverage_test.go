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

// An organization binding is covered only when the method also declares an
// org-member/admin tenant: the interceptor enforces that tenant against the
// caller's verified organization, which the request scope pins to the bound org.
// A binding paired with a self-service tenant is not — the interceptor never
// validates the bound org field on its own — so it stays a gap.
func TestBoundOrgResourceIsNeverFalselyOK(t *testing.T) {
	// PlatformRole and Mfa are set to NONE, not left zero: a real policy is
	// validated to declare both (UNSPECIFIED is a policy error that excludes the
	// method from the index), and the classifier compares against NONE. Leaving
	// them zero would read UNSPECIFIED as "declares a platform-role/MFA
	// requirement" and force gap regardless of tenant — the classifier only ever
	// runs on validated policies, so the synthetic one must be valid too.
	policy := RPCPolicy{MethodPolicy: &policyv1.MethodPolicy{
		Exposure:     policyv1.Exposure_EXPOSURE_AUTHENTICATED,
		Tenant:       policyv1.TenantRequirement_TENANT_REQUIREMENT_USER,
		PlatformRole: policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		Mfa:          policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
		ResourceBindings: []*policyv1.ResourceBinding{{
			RequestField: "org_id",
			Target:       policyv1.ResourceTarget_RESOURCE_TARGET_ORGANIZATION,
			Lookup:       policyv1.ResourceLookup_RESOURCE_LOOKUP_DIRECT_ID,
		}},
	}}
	require.Equal(t, CoverageGap, ClassifyCentralCoverage(policy))

	// The same binding under an org-admin tenant is enforced against the caller's
	// verified org, so it is covered.
	policy.MethodPolicy.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN
	require.Equal(t, CoverageOK, ClassifyCentralCoverage(policy))

	// A declared permission may admit a non-admin who holds it, so the interceptor
	// defers the whole method to the handler even under an org-admin tenant.
	policy.MethodPolicy.Permissions = []string{"billing:write"}
	require.Equal(t, CoverageGap, ClassifyCentralCoverage(policy))
	policy.MethodPolicy.Permissions = nil

	// An owned-resource binding cannot even resolve the org.
	policy.MethodPolicy.ResourceBindings[0].Target = policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE
	require.Equal(t, CoverageUnsupported, ClassifyCentralCoverage(policy))
}

// A team or owned-resource binding still implies a server-side resolution the
// interceptor does not perform, so no method carrying one may classify ok. An
// organization binding is excluded from this invariant on purpose: it is covered
// when an org tenant accompanies it (TestBoundOrgResourceIsNeverFalselyOK).
func TestTeamAndOwnedResourceBoundMethodsAreNeverOK(t *testing.T) {
	for _, policy := range RPCPolicies() {
		if !policy.Tier.Valid() || policy.PolicyError != "" {
			continue
		}
		bound := false
		for _, binding := range policy.MethodPolicy.GetResourceBindings() {
			switch binding.GetTarget() {
			case policyv1.ResourceTarget_RESOURCE_TARGET_TEAM,
				policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE:
				bound = true
			}
		}
		if bound {
			require.NotEqualf(t, CoverageOK, ClassifyCentralCoverage(policy),
				"%s binds a team or owned resource but classifies ok", policy.FullMethod)
		}
	}
}

// The classifier and the interceptor must agree: whenever the interceptor
// enforces a tenant floor for a method, that method must classify ok — otherwise
// shadow telemetry would understate what enforcement already covers, and the
// flip gate could never read green for an enforced route.
func TestCentralTenantEnforcementImpliesOK(t *testing.T) {
	for _, policy := range RPCPolicies() {
		if !policy.Tier.Valid() || policy.PolicyError != "" {
			continue
		}
		if _, enforced := CentralTenantEnforcement(policy); enforced {
			require.Equalf(t, CoverageOK, ClassifyCentralCoverage(policy),
				"%s is centrally enforced but does not classify ok", policy.FullMethod)
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
		// org-member/admin tenants are now resolved against the caller's verified
		// organization, so a pure org-tenant method is centrally enforceable.
		"/saas.accounts.v1.OrganizationService/AddMember":   CoverageOK,
		"/saas.accounts.v1.OrganizationService/ListMembers": CoverageOK,
		// CreateAPIKey adds an org permission on top of the org-admin tenant; the
		// permission may admit a non-admin who holds it, so it stays handler-gated.
		"/saas.accounts.v1.APIKeyService/CreateAPIKey": CoverageGap,
		// Platform-role and MFA requirements remain handler-enforced.
		"/saas.accounts.v1.PlatformAdminService/GetJob": CoverageGap,
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
