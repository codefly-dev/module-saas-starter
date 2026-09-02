package business

import (
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// CentralCoverage classifies how completely the central authorization
// interceptor can enforce a method's declared policy floor. It is the
// measurement vocabulary for the shadow-mode rollout: real traffic is tagged
// with one of these values so operators can confirm zero gap/broadening/
// unsupported coverage before request-time enforcement is turned on.
type CentralCoverage string

const (
	// CoverageOK means the interceptor already enforces the full declared
	// floor: exposure plus a verified caller is sufficient, and any remaining
	// row-scoping is the RLS/handler floor's job, not the interceptor's.
	CoverageOK CentralCoverage = "ok"

	// CoverageGap means the policy declares an org-tenant, permission,
	// platform-role, or MFA requirement the interceptor does not yet resolve.
	// These are the requirements hand-written require* handler sites cover
	// today and that central enforcement must learn before it can flip on.
	CoverageGap CentralCoverage = "gap"

	// CoverageUnsupported means the policy binds an owned resource whose
	// organization the interceptor cannot resolve without a lookup it does not
	// have. Such methods stay unsupported until an ownership resolver exists.
	CoverageUnsupported CentralCoverage = "unsupported"

	// CoverageBroadening means enforcing the declared floor would admit a call
	// the current path denies. It is an enforce-stage runtime comparison, not a
	// property of the declaration; static classification never returns it.
	CoverageBroadening CentralCoverage = "broadening"
)

// ClassifyCentralCoverage reports how completely the central interceptor can
// enforce policy's declared authorization floor.
//
// Public and internal methods are fully covered by exposure gating alone. An
// authenticated method is fully covered only when its floor reduces to "a
// verified user" — tenant NONE or USER with no permission, platform-role, MFA,
// or owned-resource requirement — because the interceptor establishes a
// verified identity and the RLS floor scopes rows to it. A stronger tenant,
// any declared permission, a platform-role, or an MFA requirement is a gap
// until the interceptor learns to resolve it; an owned-resource binding is
// unsupported until an ownership resolver exists. Scopes are the API-key
// ceiling enforced separately by requireScope and are not part of this floor.
func ClassifyCentralCoverage(policy RPCPolicy) CentralCoverage {
	p := policy.MethodPolicy
	if p == nil {
		return CoverageUnsupported
	}
	switch p.GetExposure() {
	case policyv1.Exposure_EXPOSURE_PUBLIC, policyv1.Exposure_EXPOSURE_INTERNAL:
		return CoverageOK
	}
	for _, binding := range p.GetResourceBindings() {
		if binding.GetTarget() == policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE {
			return CoverageUnsupported
		}
	}
	requiresPlatformRole := p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE
	requiresMFA := p.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE
	if requiresOrgTenant(p.GetTenant()) || len(p.GetPermissions()) > 0 || requiresPlatformRole || requiresMFA {
		return CoverageGap
	}
	return CoverageOK
}

func requiresOrgTenant(tenant policyv1.TenantRequirement) bool {
	switch tenant {
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_OWNER,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_TEAM_MEMBER:
		return true
	default:
		return false
	}
}
