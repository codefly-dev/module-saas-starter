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
// HostIssueMethod is the interface's issue, one of the two methods whose
// authentication is carried by the request's shape rather than by a bearer
// alone. A person is the signed-in bearer; a workload is the credential in the
// request; a child hop, or an issue from a delegation, is the actor's credential
// (with the parent token). The interceptor lets a bearer-less call reach the
// adapter, which authenticates the shape and refuses a bearer-less person by
// name; a bearer, when present, is verified as on every other method.
const HostIssueMethod = "/host.authority.v1.AuthorityService/Issue"

// HostGetDelegationMethod is the other: a delegation is read by its grantor's
// bearer or by its module's workload credential in the request.
const HostGetDelegationMethod = "/host.authority.v1.AuthorityService/GetDelegation"

// ShapeAuthenticated reports whether a bearer-less call to method reaches its
// adapter, which authenticates the request's shape itself.
func ShapeAuthenticated(method string) bool {
	return method == HostIssueMethod || method == HostGetDelegationMethod
}

// HostApplyManifestMethod is the composition interface's delivery.
const HostApplyManifestMethod = "/host.composition.v1.CompositionService/ApplyManifest"

// HostRegisterAdmissionKeyMethod is the hop registering the public key it
// signs call admissions with.
const HostRegisterAdmissionKeyMethod = "/host.authority.v1.AuthorityService/RegisterAdmissionKey"

// The installation-owned resource methods (authority 0.3.0), each called by a
// kind's serving module with its workload credential. This host refuses them
// by name until it records installation-owned resources.
const (
	HostAttachResourceMethod    = "/host.authority.v1.AuthorityService/AttachResource"
	HostDetachResourceMethod    = "/host.authority.v1.AuthorityService/DetachResource"
	HostAuthorizeTransferMethod = "/host.authority.v1.AuthorityService/AuthorizeTransfer"
)

// servedInClusterOnly are the procedures this module serves on its own
// listener and never routes at the gateway edge. Each authenticates in the
// request itself, and its caller is an in-cluster workload that reaches
// accounts directly, so routing it through the edge would put its credential
// across the perimeter for no gain.
//
//   - ApplyManifest takes the module's `delivery` service account token and
//     the signed carrier, exactly as the HTTP delivery route does, which is
//     kept off the gateway for the same reason (work.go): delivery is
//     in-cluster and never the public edge.
//   - RegisterAdmissionKey takes the hop's workload credential; the hop runs
//     in the cell and calls accounts directly.
//   - AttachResource, DetachResource and AuthorizeTransfer take the serving
//     module's workload credential, from inside the cell.
//
// Their exposure stays public on the Connect listener, where each in-cluster
// caller presents its own credential; an internal exposure would refuse it
// there and leave only the private listener, which asks the shared internal
// token.
var servedInClusterOnly = map[string]struct{}{
	HostApplyManifestMethod:        {},
	HostRegisterAdmissionKeyMethod: {},
	HostAttachResourceMethod:       {},
	HostDetachResourceMethod:       {},
	HostAuthorizeTransferMethod:    {},
}

// ServedInClusterOnly reports whether procedure is served on this module's own
// listener only, so the gateway route catalog must never carry it.
func ServedInClusterOnly(procedure string) bool {
	_, ok := servedInClusterOnly[procedure]
	return ok
}

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
	sessionMint := func(event string) *policyv1.MethodPolicy {
		p := authenticatedMint(event)
		p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE
		return p
	}
	adminWrite := func(event string) *policyv1.MethodPolicy {
		p := authenticatedMint(event)
		p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN
		p.RateLimit = policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_WRITE
		p.ResponseSensitivity = policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL
		return p
	}
	// The policy describes the grant owner's transactional emission; it does
	// not emit a second event at the interface adapter.
	exposureGrant := adminWrite("saas.scope.granted")
	// A workload's call authenticated by the credential in its request, from
	// inside the cell (ServedInClusterOnly).
	workloadCall := func(request policyv1.Sensitivity) *policyv1.MethodPolicy {
		return &policyv1.MethodPolicy{
			Exposure: policyv1.Exposure_EXPOSURE_PUBLIC, Tenant: policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE,
			Mfa:                 policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			Audit:               &policyv1.AuditPolicy{Emission: policyv1.AuditEmission_AUDIT_EMISSION_NONE},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_OPTIONAL,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION,
			RequestSensitivity:  request,
			ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_INTERNAL,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		}
	}
	admissionKeyRegistration := workloadCall(policyv1.Sensitivity_SENSITIVITY_SECRET)
	admissionKeyRegistration.Audit = &policyv1.AuditPolicy{
		Events: []string{string(EventHopAdmissionKeyPublished)}, Emission: policyv1.AuditEmission_AUDIT_EMISSION_SUCCESS,
	}
	return map[string]*policyv1.MethodPolicy{
		"/host.authority.v1.AuthorityService/Issue": authenticatedMint("saas.work_context.task_started"),
		// Check is authenticated by the token it checks: the caller is whoever holds it,
		// and the answer says nothing the token does not already carry but its standing.
		"/host.authority.v1.AuthorityService/Check": read(policyv1.Exposure_EXPOSURE_PUBLIC, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL),
		"/host.authority.v1.AuthorityService/Keys":  read(policyv1.Exposure_EXPOSURE_PUBLIC, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_PUBLIC),
		// The hop proves itself with its workload credential and only a public
		// key travels. A key's first publication is audited in the transaction
		// that publishes it; its renewal every lease is not an event.
		HostRegisterAdmissionKeyMethod: admissionKeyRegistration,
		HostAttachResourceMethod:       workloadCall(policyv1.Sensitivity_SENSITIVITY_SECRET),
		HostDetachResourceMethod:       workloadCall(policyv1.Sensitivity_SENSITIVITY_SECRET),
		HostAuthorizeTransferMethod:    workloadCall(policyv1.Sensitivity_SENSITIVITY_SECRET),
		// ListResources is authenticated by the capability it is asked about, like Check.
		"/host.authority.v1.AuthorityService/ListResources": read(policyv1.Exposure_EXPOSURE_PUBLIC, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL),
		// A delegation names no organisation: it is the bearer's, so the floor is
		// the authenticated caller and the organisation is the session's.
		"/host.authority.v1.AuthorityService/Delegate":         sessionMint(string(EventModuleDelegationGranted)),
		"/host.authority.v1.AuthorityService/RevokeDelegation": sessionMint(string(EventModuleDelegationRevoked)),
		"/host.authority.v1.AuthorityService/GetDelegation":    read(policyv1.Exposure_EXPOSURE_AUTHENTICATED, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL),
		// Authentication belongs to signed delivery and its workload credential.
		// In-cluster only (ServedInClusterOnly): never routed at the gateway.
		HostApplyManifestMethod: {
			Exposure: policyv1.Exposure_EXPOSURE_PUBLIC, Tenant: policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE,
			Mfa: policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			// The delivery path this will route into emits saas.solution.host_binding_applied itself (outside RPC); declaring it here too would double it.
			Audit:               &policyv1.AuditPolicy{Emission: policyv1.AuditEmission_AUDIT_EMISSION_NONE},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_OPTIONAL,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION,
			RequestSensitivity:  policyv1.Sensitivity_SENSITIVITY_SECRET,
			ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_INTERNAL,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		},
		"/host.installation.v1.InstallationService/Install":     adminWrite("saas.installation.created"),
		"/host.installation.v1.InstallationService/GrantToTeam": exposureGrant,
		"/host.installation.v1.InstallationService/List":        read(policyv1.Exposure_EXPOSURE_AUTHENTICATED, policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL), // an administrator's read; a member's view is the entitlement listing
		// Current is the caller's own session: authenticated by the bearer it
		// describes, organisation-less like the session itself may be.
		"/host.session.v1.SessionService/Current": read(policyv1.Exposure_EXPOSURE_AUTHENTICATED, policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE, policyv1.Sensitivity_SENSITIVITY_CONFIDENTIAL),
		// The sign-in page reads it before anyone signs in; it answers none
		// unless fixture sign-in is enabled, and then the tokens SignIn takes.
		"/host.session.v1.SessionService/ListFixturePeople": {
			Exposure: policyv1.Exposure_EXPOSURE_PUBLIC, Tenant: policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE,
			Mfa:                 policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
			Audit:               &policyv1.AuditPolicy{Emission: policyv1.AuditEmission_AUDIT_EMISSION_NONE},
			Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_OPTIONAL,
			RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION,
			RequestSensitivity:  policyv1.Sensitivity_SENSITIVITY_PUBLIC,
			ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_SECRET,
			PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		},
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
	"AuthorityService/Issue":                "Issue a Work Context for one owner shape, one audience and the scopes asked for, bounded by what the owner holds.",
	"AuthorityService/Check":                "Verify a presented Work Context for the audience the caller is, re-checking the revision and the seal live.",
	"AuthorityService/Keys":                 "Return the issuer and the public keys a verifier pins: the Work Context keys and, apart from them, the hop's admission keys.",
	"AuthorityService/RegisterAdmissionKey": "Publish the hop's admission public key in Keys for one lease; only the hop's workload registers one.",
	"AuthorityService/AttachResource":       "Record that one resource belongs to an installation; refused by name until this host records installation-owned resources.",
	"AuthorityService/DetachResource":       "Release one resource from the installation that owns it; refused by name until this host records installation-owned resources.",
	"AuthorityService/AuthorizeTransfer":    "Decide whether an existing resource may join an installed solution's principal; refused by name until this host records installation-owned resources.",
	"CompositionService/ApplyManifest":      "Deliver one module instance's manifest at one generation; the ceiling is the manifest.",
	"InstallationService/Install":           "Install one target at one organisation under the least role it needs; pending until its install step is acknowledged.",
	"InstallationService/GrantToTeam":       "Expose an installation to a team under the installation's role.",
	"InstallationService/List":              "List what one organisation has installed.",
	"AuthorityService/ListResources":        "Answer which resources of one kind a checked capability covers for one action, from its owner's current grants.",
	"AuthorityService/Delegate":             "Record a signed-in person's grant to a module, within what they hold.",
	"AuthorityService/RevokeDelegation":     "End a delegation; every capability issued from it is refused from then on.",
	"AuthorityService/GetDelegation":        "Read whether one delegation still stands, for its module or its grantor.",
	"SessionService/SignIn":                 "Open a session for a fixture person by their fixture token.",
	"SessionService/Current":                "Answer the session the caller's bearer belongs to, without the bearer.",
	"SessionService/ListFixturePeople":      "List the people fixture sign-in admits, in the fixture's order; none where it is not enabled.",
}

func init() {
	for key, description := range interfaceDescriptions {
		if _, taken := rpcDescriptions[key]; taken {
			panic("interface description collides with an accounts procedure: " + key)
		}
		rpcDescriptions[key] = description
	}
}
