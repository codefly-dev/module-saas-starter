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
	// organization the interceptor cannot resolve: no ownership resolver is
	// registered for the method, or the registered resolver could not map this
	// request's resource id to an org. Such methods cannot be centrally
	// enforced until their owning org resolves.
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
// or bound org/team/owned resource — because the interceptor establishes a
// verified identity and the RLS floor scopes rows to it. A stronger tenant,
// any declared permission, a platform-role, an MFA requirement, or a resource
// binding that names an organization or team the caller must relate to is a
// gap until the interceptor learns to resolve it. An owned-resource binding is
// unsupported unless an ownership resolver is registered for the method; once
// it is, the owning org is recoverable and the binding classifies as the gap
// its bound-resource check implies. Scopes are the API-key ceiling enforced
// separately by requireScope and are not part of this floor.
func ClassifyCentralCoverage(policy RPCPolicy) CentralCoverage {
	p := policy.MethodPolicy
	if p == nil {
		return CoverageUnsupported
	}
	switch p.GetExposure() {
	case policyv1.Exposure_EXPOSURE_PUBLIC, policyv1.Exposure_EXPOSURE_INTERNAL:
		return CoverageOK
	}
	requiresBoundResource := false
	for _, binding := range p.GetResourceBindings() {
		switch binding.GetTarget() {
		case policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE:
			// An owned resource whose org the interceptor can resolve is no
			// worse than a directly bound organization: it still needs the
			// bound-resource check the interceptor does not yet perform (gap),
			// but it is no longer unsupported. Without a resolver the org is
			// unrecoverable and the method stays unsupported.
			if !ownedResourceResolvable(policy.FullMethod) {
				return CoverageUnsupported
			}
			requiresBoundResource = true
		case policyv1.ResourceTarget_RESOURCE_TARGET_ORGANIZATION,
			policyv1.ResourceTarget_RESOURCE_TARGET_TEAM:
			requiresBoundResource = true
		}
	}
	requiresPlatformRole := p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE
	requiresMFA := p.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE
	if requiresOrgTenant(p.GetTenant()) || len(p.GetPermissions()) > 0 || requiresPlatformRole || requiresMFA || requiresBoundResource {
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
