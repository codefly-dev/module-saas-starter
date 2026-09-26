package business

import (
	"testing"

	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// No Work Context is ever minted on an impersonated request.
//
// This is not a style rule — it is the premise the platform-read branch of
// workContextPermissionAllowed rests on. platformReadAdmissible refuses a
// platform administrator's authority on an impersonated request, and
// platform_read_authority.go justifies leaving that check out of every
// downstream seam with "A Work Context cannot be minted while impersonating, so
// a module's forwarded viewer is always someone acting as themselves."
//
// That premise was five hand-written proto annotations with nothing holding
// them: the policy matrix reads whatever a method declares and never asks
// whether a mint declared this one. The content-read branch made it load-
// bearing in a new place — it now guards the issuance of a durable, forwardable
// capability rather than one read decision — and an omission fails open,
// silently.
//
// The invariant has two halves, and both are checked here, because
// auth.ImpersonatedRequest reads the verified identity an end-user session
// installs: a mint reachable from such a session must forbid impersonation, and
// a mint that is internal-only has no session to impersonate within. The second
// half is what makes this a gate rather than an allowlist — flipping an
// internal mint to EXPOSURE_AUTHENTICATED without adding the annotation fails
// here.
func TestNoWorkContextIsMintedOnAnImpersonatedRequest(t *testing.T) {
	const issued = "saas.accounts.v1.IssuedWorkContext"

	mints, forbid, internal := 0, 0, 0
	for _, service := range accountServiceDescriptors() {
		methods := service.Methods()
		for i := 0; i < methods.Len(); i++ {
			method := methods.Get(i)
			if string(method.Output().FullName()) != issued {
				continue
			}
			mints++
			policy, problem := descriptorMethodPolicy(method)
			if policy == nil {
				t.Errorf("%s mints a Work Context but declares no method_policy: %s", method.FullName(), problem)
				continue
			}
			forbidden := policy.GetImpersonation() ==
				policyv1.ImpersonationRequirement_IMPERSONATION_REQUIREMENT_FORBIDDEN
			if forbidden {
				forbid++
				continue
			}
			if policy.GetExposure() == policyv1.Exposure_EXPOSURE_INTERNAL {
				internal++
				// No end-user session reaches it, so no verified identity carries
				// an impersonation marker into the mint.
				continue
			}
			t.Errorf(
				"%s mints a Work Context with exposure=%s and impersonation=%s; a mint reachable from an "+
					"end-user session must declare IMPERSONATION_REQUIREMENT_FORBIDDEN, because platform read "+
					"authority is admitted at the mint only on the premise that no mint runs while impersonating",
				method.FullName(), policy.GetExposure(), policy.GetImpersonation(),
			)
		}
	}
	// Both halves must actually be exercised, or a rename of the issued message
	// (or of the exposure enum) would leave this passing while checking nothing.
	if mints == 0 {
		t.Fatalf("no RPC returning %s was found: the gate is looking at the wrong output type and proves nothing", issued)
	}
	if forbid == 0 {
		t.Fatal("no mint declared IMPERSONATION_REQUIREMENT_FORBIDDEN: the gate is not reading the policy it claims to")
	}
	if internal == 0 {
		t.Fatal("no mint was EXPOSURE_INTERNAL: the internal-only half of the invariant is unexercised")
	}
}
