package business

import (
	"sort"

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

	// CoverageGap means the policy declares a requirement the interceptor does
	// not resolve in full at admission: a platform role, MFA, a team or
	// org-owner tenant, or the tenant beside a declared permission. These are
	// the requirements hand-written require* handler sites still cover. The
	// declared permission itself is not among them: admission enforces it
	// (CentralPermissionEnforcement), as it does the org-member/admin tenant of
	// a method that declares nothing finer.
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

// globalScopeEscapeMethods are the PermissionService mutations whose handler
// gates through requireRoleScope: org admin on the request's org, OR platform
// admin when the org scope is empty (a global, org-less role/scope/share). The
// flat MethodPolicy IDL is a conjunction and cannot express that "ORG_ADMIN for
// this org OR platform admin for global scope" alternative, so their declared
// ORG_ADMIN tenant reads to the central interceptor as an unconditional org-admin
// floor. Enforcing that floor against the caller's verified org would both
// over-deny a platform admin operating on global scope (they have no org
// membership to satisfy it) and admit an org admin operating on global scope the
// handler denies. The interceptor never reads the request body, so it cannot
// resolve the empty-org alternative itself; these methods therefore stay
// handler-enforced (gap) and are never centrally enforced. The set is pinned to
// the requireRoleScope call sites by TestGlobalScopeEscapeHatchMatchesHandlers.
var globalScopeEscapeMethods = map[string]struct{}{
	"/saas.accounts.v1.PermissionService/CreateRole":        {},
	"/saas.accounts.v1.PermissionService/UpdateRole":        {},
	"/saas.accounts.v1.PermissionService/AssignRole":        {},
	"/saas.accounts.v1.PermissionService/RevokeRole":        {},
	"/saas.accounts.v1.PermissionService/RegisterScopeNode": {},
	"/saas.accounts.v1.PermissionService/GrantScope":        {},
	"/saas.accounts.v1.PermissionService/RevokeScope":       {},
	"/saas.accounts.v1.PermissionService/ShareRecord":       {},
	"/saas.accounts.v1.PermissionService/RevokeShare":       {},
	"/saas.accounts.v1.PermissionService/ListShares":        {},
}

func hasGlobalScopeEscape(fullMethod string) bool {
	_, ok := globalScopeEscapeMethods[fullMethod]
	return ok
}

// GlobalScopeEscapeHatchMethods returns the methods the central classifier treats
// as global-scope escapes — handler-gated, never centrally enforced. It is
// exported so the adapters lockstep test can prove this set equals the handlers
// that actually gate through requireRoleScope, in both directions: an entry whose
// handler stopped routing through requireRoleScope, or a requireRoleScope handler
// missing here, fails that test rather than silently classifying a route as a gap
// no handler backs (or as ok when it must defer).
func GlobalScopeEscapeHatchMethods() []string {
	out := make([]string, 0, len(globalScopeEscapeMethods))
	for method := range globalScopeEscapeMethods {
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

// ClassifyCentralCoverage reports how completely the central interceptor can
// enforce policy's declared authorization floor.
//
// Public and internal methods are fully covered by exposure gating alone. Among
// authenticated methods, the interceptor resolves the ORG_MEMBER / ORG_ADMIN
// tenant against the caller's verified organization (CentralTenantEnforcement),
// so a method whose floor reduces to that tenant — or to "a verified user"
// (tenant NONE or USER) — is fully covered. A shape-authenticated method is a
// gap: its caller may be a credential in the request body, so there is no
// verified organization at admission to resolve the tenant against, and the
// adapter resolves it from the credential itself. A declared permission is
// enforced at admission (CentralPermissionEnforcement), but the tenant declared
// beside it is not, since the permission may admit a non-admin who holds it;
// that tenant, a platform-role, an MFA requirement, an
// org-owner or team tenant, a bound team resource, or a global-scope escape
// method (globalScopeEscapeMethods) is a gap the require* handler sites still
// cover. An owned-resource binding is unsupported unless an
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
	if hasGlobalScopeEscape(policy.FullMethod) {
		return CoverageGap
	}
	if ShapeAuthenticated(policy.FullMethod) {
		return CoverageGap
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
// no team or owned-resource binding, is not shape-authenticated, and no
// global-scope escape
// (globalScopeEscapeMethods, whose empty-org alternative the interceptor cannot
// resolve). Every other requirement stays with the handler require* sites, so
// this check is never stricter than the handler's own.
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
	if hasGlobalScopeEscape(policy.FullMethod) {
		return policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED, false
	}
	// A shape-authenticated method (ShapeAuthenticated: the host interface's
	// Issue and GetDelegation) carries its caller's credential in the request
	// body, not necessarily as a bearer. Admission never reads the body, so
	// there is no verified organization to resolve the declared tenant against:
	// for a workload, an installation or a child hop there is no verified
	// database scope at all, and enforcing the floor refuses every such call
	// with "verified authentication context required" before the adapter can
	// authenticate the credential it was handed and resolve the organization
	// from it. The tenant stays with that adapter, which checks the
	// organization against the credential rather than against a session.
	//
	// This is the exemption the enforce-by-default flip required: the floor has
	// to decline a method whose authenticating credential it cannot see, and
	// declining it here keeps the coverage classifier and this check agreeing on
	// one answer per method.
	if ShapeAuthenticated(policy.FullMethod) {
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

// permissionDecidedByHandler names, for each method whose declared permission
// admission does not resolve, the one handler rule that decides it. Admission
// (CentralPermissionEnforcement) enforces every other declared permission
// against the caller's verified organization, so a method is here only when
// that question is the wrong one to ask of it:
//
//   - the audit reads take an organization or, with none named, are the
//     platform-wide read a platform role makes. Admission never reads the
//     request and cannot tell the two apart, so the handlers ask
//     requireAuditRead, which resolves audit:read through the same helper
//     admission uses. The export is an administrator's, and the administrator
//     holds every permission;
//   - listing every user is a platform role's read: it names no organization
//     to hold users:read in, and the platform role is the authority;
//   - a source operation's datasource:read / datasource:invoke is an action on
//     one source, decided by that source's own access (its owner, its grants, or
//     a delegated capability bound to it), never by an organization-wide role.
//
// TestEveryDeclaredPermissionIsEnforcedOrNamesItsRule holds every method that
// declares a permission to one side or the other, and
// TestTheAuditTrailNeedsAuditRead proves the audit rule on the handlers.
var permissionDecidedByHandler = map[string]string{
	"/saas.accounts.v1.AuditService/QueryAuditLog":       "requireAuditRead",
	"/saas.accounts.v1.AuditService/AggregateAuditLog":   "requireAuditRead",
	"/saas.accounts.v1.AuditService/ExportAuditLog":      "requireOrgAdmin",
	"/saas.accounts.v1.AuditService/ListAuditEventTypes": "requireAuditRead",

	"/saas.accounts.v1.UserService/ListUsers": "requirePlatformAdmin",

	"/saas.accounts.v1.DatasourceService/InvokeSourceOperation":              "SourceOperationAuthority",
	"/saas.accounts.v1.DatasourceService/LookupInvokeSourceOperation":        "SourceOperationAuthority",
	"/saas.accounts.v1.DatasourceService/ListSourceOperations":               "sourceOperationAccess",
	"/saas.accounts.v1.DatasourceService/PruneSourceOperationReceipts":       "SourceReceiptRetentionAuthority",
	"/saas.accounts.v1.DatasourceService/LookupPruneSourceOperationReceipts": "SourceReceiptRetentionAuthority",
	"/saas.accounts.v1.DatasourceService/ReconcileSource":                    "ReconcileSourceAuthority",
	"/saas.accounts.v1.DatasourceService/LookupReconcileSource":              "ReconcileSourceAuthority",
}

// PermissionDecidedByHandler returns the handler rule that decides fullMethod's
// declared permission, empty when admission enforces it.
func PermissionDecidedByHandler(fullMethod string) string {
	return permissionDecidedByHandler[fullMethod]
}

// CentralPermissionEnforcement reports the declared permissions the
// interceptor enforces at admission for policy, and whether it enforces any.
// A host enforces every permission it declares: an authenticated method that
// declares one admits a caller only when they hold it in their verified
// organization, whatever else the handler checks afterwards.
//
// Two shapes are not resolved here. A shape-authenticated method carries its
// caller in the request body. And the methods in permissionDecidedByHandler
// name the handler rule that decides instead.
func CentralPermissionEnforcement(policy RPCPolicy) ([]string, bool) {
	p := policy.MethodPolicy
	if p == nil || len(p.GetPermissions()) == 0 {
		return nil, false
	}
	if p.GetExposure() != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
		return nil, false
	}
	if ShapeAuthenticated(policy.FullMethod) || PermissionDecidedByHandler(policy.FullMethod) != "" {
		return nil, false
	}
	return cloneStrings(p.GetPermissions()), true
}
