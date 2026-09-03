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

	// CoverageGap means the policy declares a permission, platform-role, MFA,
	// team, or org-owner requirement the interceptor does not resolve at
	// admission. These are the requirements hand-written require* handler sites
	// still cover; the org-member/admin tenant is no longer among them, because
	// the interceptor now resolves it against the caller's verified organization.
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
// Public and internal methods are fully covered by exposure gating alone. Among
// authenticated methods, the interceptor resolves the ORG_MEMBER / ORG_ADMIN
// tenant against the caller's verified organization (CentralTenantEnforcement),
// so a method whose floor reduces to that tenant — or to "a verified user"
// (tenant NONE or USER) — is fully covered. A declared permission (which may
// admit a non-admin who holds it), a platform-role, an MFA requirement, an
// org-owner or team tenant, or a bound team resource is a gap the require*
// handler sites still cover; an owned-resource binding is unsupported until an
// ownership resolver exists. A bound organization not pinned by an org tenant is
// also a gap, since the interceptor never validates the bound field on its own.
// Scopes are the API-key ceiling enforced separately by requireScope and are not
// part of this floor.
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
		switch binding.GetTarget() {
		case policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE:
			return CoverageUnsupported
		case policyv1.ResourceTarget_RESOURCE_TARGET_TEAM:
			return CoverageGap
		}
	}
	if _, enforced := CentralTenantEnforcement(policy); enforced {
		return CoverageOK
	}
	if len(p.GetPermissions()) > 0 ||
		p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE ||
		p.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE {
		return CoverageGap
	}
	switch p.GetTenant() {
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_OWNER,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_TEAM_MEMBER:
		return CoverageGap
	}
	// tenant NONE or USER: a bound organization the tenant floor does not pin is
	// never validated by the interceptor.
	for _, binding := range p.GetResourceBindings() {
		if binding.GetTarget() == policyv1.ResourceTarget_RESOURCE_TARGET_ORGANIZATION {
			return CoverageGap
		}
	}
	return CoverageOK
}

// CentralTenantEnforcement reports the tenant floor the interceptor enforces at
// admission for policy, and whether it enforces one at all. The interceptor
// resolves only the ORG_MEMBER / ORG_ADMIN tenant, against the caller's verified
// organization, and only when the method declares no finer permission (which
// could admit a non-admin who holds it), no platform-role or MFA requirement,
// and no team or owned-resource binding. Every other requirement stays with the
// handler require* sites, so this check is never stricter than the handler's own.
func CentralTenantEnforcement(policy RPCPolicy) (policyv1.TenantRequirement, bool) {
	p := policy.MethodPolicy
	if p == nil {
		return policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED, false
	}
	if len(p.GetPermissions()) > 0 ||
		p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE ||
		p.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE {
		return policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED, false
	}
	for _, binding := range p.GetResourceBindings() {
		switch binding.GetTarget() {
		case policyv1.ResourceTarget_RESOURCE_TARGET_TEAM,
			policyv1.ResourceTarget_RESOURCE_TARGET_OWNED_RESOURCE:
			return policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED, false
		}
	}
	switch tenant := p.GetTenant(); tenant {
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN:
		return tenant, true
	}
	return policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED, false
}
