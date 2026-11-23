package business

import (
	"testing"

	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// A procedure is budgeted as machine traffic only when no session names its
// caller: it must be a public interface procedure, and never one an anonymous
// visitor signs in through.
func TestCredentialInRequestProceduresArePublicInterfaceProcedures(t *testing.T) {
	policies := InterfaceMethodPolicies()
	for procedure := range credentialInRequest {
		policy, ok := policies[procedure]
		if !ok {
			t.Errorf("%s is budgeted as machine traffic but is not an interface procedure", procedure)
			continue
		}
		if policy.GetExposure() != policyv1.Exposure_EXPOSURE_PUBLIC {
			t.Errorf("%s is budgeted as machine traffic but a session names its caller (%s)", procedure, policy.GetExposure())
		}
	}
	for _, procedure := range []string{
		"/host.session.v1.SessionService/SignIn",
		"/host.session.v1.SessionService/ListFixturePeople",
	} {
		if CredentialInRequest(procedure) {
			t.Errorf("%s is an anonymous visitor's call and must keep the anonymous budget", procedure)
		}
	}
}
