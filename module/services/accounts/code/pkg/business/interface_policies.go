package business

import (
	"google.golang.org/protobuf/reflect/protoreflect"

	// The interface descriptors must be linked for RPCPolicies and the catalog
	// generators to see them; the policy sidecar below is what names them.
	_ "accounts/pkg/gen/host/authority/v1"
	_ "accounts/pkg/gen/host/composition/v1"
	_ "accounts/pkg/gen/host/installation/v1"
	_ "accounts/pkg/gen/host/session/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// InterfaceMethodPolicies is the host's policy for the methods of the published
// host interfaces (codefly.dev/authority, composition,
// installation, session). Those protos carry no saas.policy option on purpose:
// exposure, tenancy, audit and rate limits are how THIS implementation guards an
// operation, not what a caller is promised, and the same interface is served by
// the mini gateway with none of them. So the policy lives beside the
// implementation, keyed by full method, and RPCPolicies reads it when the
// descriptor carries no option. A method of these packages that is missing here
// is a policy error like a missing option, and the interceptor refuses it.
func InterfaceMethodPolicies() map[string]*policyv1.MethodPolicy {
	authenticatedMint := func(event string) *policyv1.MethodPolicy {
		return &policyv1.MethodPolicy{
			Exposure:            policyv1.Exposure_EXPOSURE_AUTHENTICATED,
			Tenant:              policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER,
			Mfa:                 policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			Audit:               &policyv1.AuditPolicy{Events: []string{event}, Emission: policyv1.AuditEmission_AUDIT_EMISSION_SUCCESS},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_FORBIDDEN,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_SENSITIVE,
			RequestSensitivity:  policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL,
			ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_SECRET,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
			Impersonation:       policyv1.ImpersonationRequirement_IMPERSONATION_REQUIREMENT_FORBIDDEN,
		}
	}
	read := func(exposure policyv1.Exposure, tenant policyv1.TenantRequirement, sensitivity policyv1.Sensitivity) *policyv1.MethodPolicy {
		return &policyv1.MethodPolicy{
			Exposure: exposure, Tenant: tenant,
			Mfa:                 policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			Audit:               &policyv1.AuditPolicy{Emission: policyv1.AuditEmission_AUDIT_EMISSION_NONE},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_OPTIONAL,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_READ,
			RequestSensitivity:  sensitivity,
			ResponseSensitivity: sensitivity,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
			Impersonation:       impersonationFor(exposure),
		}
	}
	adminWrite := func(event string) *policyv1.MethodPolicy {
		p := authenticatedMint(event)
		p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN
		p.RateLimit = policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_WRITE
		p.ResponseSensitivity = policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL
		return p
	}
	return map[string]*policyv1.MethodPolicy{
		"/host.authority.v1.AuthorityService/Issue": authenticatedMint("saas.work_context.task_started"),
		"/host.authority.v1.AuthorityService/Check": read(policyv1.Exposure_EXPOSURE_AUTHENTICATED, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL),
		"/host.authority.v1.AuthorityService/Keys":  read(policyv1.Exposure_EXPOSURE_PUBLIC, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_PUBLIC),
		"/host.composition.v1.CompositionService/ApplyManifest": {
			Exposure: policyv1.Exposure_EXPOSURE_INTERNAL, Tenant: policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE,
			Mfa:                 policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			Audit:               &policyv1.AuditPolicy{Events: []string{"saas.solution.host_binding_applied"}, Emission: policyv1.AuditEmission_AUDIT_EMISSION_SUCCESS},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_OPTIONAL,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_INTERNAL,
			RequestSensitivity:  policyv1.Sensitivity_SENSITIVITY_INTERNAL,
			ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_INTERNAL,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		},
		"/host.installation.v1.InstallationService/Install":     adminWrite("saas.installation.created"),
		"/host.installation.v1.InstallationService/GrantToTeam": adminWrite("saas.installation.created"), /* GAP: no exposure event is registered; narrowed is the nearest */
		"/host.installation.v1.InstallationService/List":        read(policyv1.Exposure_EXPOSURE_AUTHENTICATED, policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL),
		"/host.session.v1.SessionService/SignIn": {
			Exposure: policyv1.Exposure_EXPOSURE_PUBLIC, Tenant: policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE,
			Mfa:                 policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			Audit:               &policyv1.AuditPolicy{Events: []string{"saas.auth.login"}, Emission: policyv1.AuditEmission_AUDIT_EMISSION_SUCCESS},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_FORBIDDEN,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION,
			RequestSensitivity:  policyv1.Sensitivity_SENSITIVITY_SECRET,
			ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_SECRET,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		},
	}
}

// impersonationFor: impersonation is a statement about a user identity, so only
// an authenticated policy says anything about it.
func impersonationFor(exposure policyv1.Exposure) policyv1.ImpersonationRequirement {
	if exposure == policyv1.Exposure_EXPOSURE_AUTHENTICATED {
		return policyv1.ImpersonationRequirement_IMPERSONATION_REQUIREMENT_FORBIDDEN
	}
	return policyv1.ImpersonationRequirement_IMPERSONATION_REQUIREMENT_UNSPECIFIED
}

// ServiceAPIPackage is this service's own protobuf package; every other package
// it serves is a published interface it implements.
const ServiceAPIPackage = "saas.accounts.v1"

// implementedHere reports whether this service serves a descriptor. Every
// accounts service is served; of the interface packages, InstallableService is
// the solution's side of the installation interface and is not served here.
func implementedHere(service protoreflect.ServiceDescriptor) bool {
	return string(service.FullName()) != "host.installation.v1.InstallableService"
}

// interfacePackage reports whether a proto package is one of the published host
// interfaces this service implements.
func interfacePackage(name string) bool {
	switch name {
	case "host.authority.v1", "host.composition.v1", "host.installation.v1", "host.session.v1":
		return true
	}
	return false
}

// interfaceDescriptions are the one-line descriptions the catalog requires for
// every served procedure, keyed as rpcDescriptions is: "Service/Method".
var interfaceDescriptions = map[string]string{
	"AuthorityService/Issue":           "Issue a Work Context for one owner shape, one audience and the scopes asked for, bounded by what the owner holds.",
	"AuthorityService/Check":           "Verify a presented Work Context for the audience the caller is, re-checking the revision and the seal live.",
	"AuthorityService/Keys":            "Return the issuer and the public keys a verifier pins.",
	"CompositionService/ApplyManifest": "Deliver one module instance's manifest at one generation; the ceiling is the manifest.",
	"InstallationService/Install":      "Install one target at one organisation under the least role it needs; pending until its install step is acknowledged.",
	"InstallationService/GrantToTeam":  "Expose an installation to a team under the installation's role.",
	"InstallationService/List":         "List what one organisation has installed.",
	"SessionService/SignIn":            "Open a session for a fixture person by their fixture token.",
}

func init() {
	for key, description := range interfaceDescriptions {
		if _, taken := rpcDescriptions[key]; taken {
			panic("interface description collides with an accounts procedure: " + key)
		}
		rpcDescriptions[key] = description
	}
}
