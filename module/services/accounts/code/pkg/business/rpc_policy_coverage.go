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
// Public and internal methods are fully covered by exposure gating alone. Among
// authenticated methods, the interceptor resolves the ORG_MEMBER / ORG_ADMIN
// tenant against the caller's verified organization (CentralTenantEnforcement),
// so a method whose floor reduces to that tenant — or to "a verified user"
// (tenant NONE or USER) — is fully covered. A declared permission (which may
// admit a non-admin who holds it), a platform-role, an MFA requirement, an
// org-owner or team tenant, or a bound team resource is a gap the require*
// handler sites still cover. An owned-resource binding is unsupported unless an
// ownership resolver is registered for the method; once it is, the owning org is
// recoverable and the binding classifies as the gap its bound-resource check
// implies. A bound organization not pinned by an org tenant is also a gap, since
// the interceptor never validates the bound field on its own. Scopes are the
// API-key ceiling enforced separately by requireScope and are not part of this
// floor.
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
			// Without a registered resolver the owning org is unrecoverable, so
			// the method stays unsupported. With one it is no worse than a
			// directly org-bound method: still a gap (the interceptor does not
			// yet perform the bound-resource check), but no longer unsupported.
			if !ownedResourceResolvable(policy.FullMethod) {
				return CoverageUnsupported
			}
			return CoverageGap
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
//
// The interceptor checks the caller's verified (token) organization and never
// reads the request body. Classifying such a method ok is sound only because
// auth.RequireVerifiedDatabaseScope pins req.OrgId to the verified org at every
// handler require* site: a request naming a different org is already rejected
// there, so "verified org" and "request org" coincide. For an ORG_MEMBER method
// that makes coverage rest on the token carrying a valid org (the caller is a
// member of their own active org) plus that pin — not on an independent check of
// the request's org field, which the interceptor cannot see. If the pin is ever
// removed, an ORG_MEMBER / ORG_ADMIN method must no longer classify ok.
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
