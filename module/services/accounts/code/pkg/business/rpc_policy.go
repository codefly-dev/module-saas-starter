package business

//go:generate go run ../../cmd/authz-matrix -output ../../../AUTHZ_MATRIX.md
//go:generate go run ../../cmd/service-catalog -output ../../../generated/service-catalog.json

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	annotations "google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// RPCPolicyTier is the compact compatibility view exposed by service
// introspection. The complete, authoritative policy is MethodPolicy.
type RPCPolicyTier string

const (
	RPCPolicyPublic        RPCPolicyTier = "public"
	RPCPolicyAuthenticated RPCPolicyTier = "auth"
	RPCPolicyOrgMember     RPCPolicyTier = "org_member"
	RPCPolicyOrgAdmin      RPCPolicyTier = "org_admin"
	RPCPolicyPlatformAdmin RPCPolicyTier = "platform_admin"
	RPCPolicyMFA           RPCPolicyTier = "mfa"
	RPCPolicyInternal      RPCPolicyTier = "internal"
)

// RPCPolicy is the normalized, protocol-independent policy for one protobuf
// method. FullMethod is the canonical gRPC/Connect procedure path. Description
// remains editorial metadata; every enforcement field comes from the method's
// saas.policy.v1.method_policy option.
type RPCPolicy struct {
	FullMethod      string
	FullService     string
	Service         string
	Method          string
	InputType       string
	OutputType      string
	ClientStreaming bool
	ServerStreaming bool
	Deprecated      bool
	SourceProto     string
	HTTPMethod      string
	HTTPPath        string
	HTTPBindings    []RPCHTTPBinding
	Description     string
	Scopes          []string
	Tier            RPCPolicyTier
	EmitsAudit      bool
	Streaming       bool
	MethodPolicy    *policyv1.MethodPolicy
	PolicyError     string
}

// RPCHTTPBinding is the protocol-neutral view of one google.api.http rule.
// HTTPMethod and HTTPPath on RPCPolicy retain the first binding for existing
// runtime introspection consumers.
type RPCHTTPBinding struct {
	Method       string
	Path         string
	Body         string
	ResponseBody string
}

// RPCPolicies derives the complete method inventory and its transport policy
// directly from protobuf descriptors. Missing or invalid policy remains in the
// returned inventory with PolicyError set, but LookupRPCPolicy excludes it so
// runtime admission fails closed.
func RPCPolicies() []RPCPolicy {
	var out []RPCPolicy
	for _, service := range accountServiceDescriptors() {
		for i := 0; i < service.Methods().Len(); i++ {
			method := service.Methods().Get(i)
			serviceName := string(service.Name())
			methodName := string(method.Name())
			description := rpcDescriptions[serviceName+"/"+methodName]
			methodPolicy, policyErr := descriptorMethodPolicy(method)
			httpBindings := descriptorHTTPBindings(method)
			var httpMethod, httpPath string
			if len(httpBindings) > 0 {
				httpMethod = httpBindings[0].Method
				httpPath = httpBindings[0].Path
			}
			options, _ := method.Options().(*descriptorpb.MethodOptions)
			out = append(out, RPCPolicy{
				FullMethod:      "/" + string(service.FullName()) + "/" + methodName,
				FullService:     string(service.FullName()),
				Service:         serviceName,
				Method:          methodName,
				InputType:       string(method.Input().FullName()),
				OutputType:      string(method.Output().FullName()),
				ClientStreaming: method.IsStreamingClient(),
				ServerStreaming: method.IsStreamingServer(),
				Deprecated:      options.GetDeprecated(),
				SourceProto:     method.ParentFile().Path(),
				HTTPMethod:      httpMethod,
				HTTPPath:        httpPath,
				HTTPBindings:    httpBindings,
				Description:     description,
				Scopes:          cloneStrings(methodPolicy.GetScopes()),
				Tier:            descriptorPolicyTier(methodPolicy),
				EmitsAudit:      policyEmitsAudit(methodPolicy),
				Streaming:       method.IsStreamingClient() || method.IsStreamingServer(),
				MethodPolicy:    methodPolicy,
				PolicyError:     policyErr,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullMethod < out[j].FullMethod })
	return out
}

func accountServiceDescriptors() []protoreflect.ServiceDescriptor {
	var services []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if string(file.Package()) != "saas.accounts.v1" {
			return true
		}
		for i := 0; i < file.Services().Len(); i++ {
			services = append(services, file.Services().Get(i))
		}
		return true
	})
	sort.Slice(services, func(i, j int) bool {
		return services[i].FullName() < services[j].FullName()
	})
	return services
}

func descriptorMethodPolicy(method protoreflect.MethodDescriptor) (*policyv1.MethodPolicy, string) {
	options, ok := method.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(options, policyv1.E_MethodPolicy) {
		return nil, "method_policy option is missing"
	}
	methodPolicy, ok := proto.GetExtension(options, policyv1.E_MethodPolicy).(*policyv1.MethodPolicy)
	if !ok || methodPolicy == nil {
		return nil, "method_policy option has an unexpected type"
	}
	copy, _ := proto.Clone(methodPolicy).(*policyv1.MethodPolicy)
	return copy, validateDescriptorPolicy(method, copy)
}

func descriptorHTTPBindings(method protoreflect.MethodDescriptor) []RPCHTTPBinding {
	options, ok := method.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(options, annotations.E_Http) {
		return nil
	}
	rule, ok := proto.GetExtension(options, annotations.E_Http).(*annotations.HttpRule)
	if !ok || rule == nil {
		return nil
	}
	return flattenHTTPRule(rule)
}

func flattenHTTPRule(rule *annotations.HttpRule) []RPCHTTPBinding {
	if rule == nil {
		return nil
	}
	var method, path string
	switch {
	case rule.GetGet() != "":
		method, path = "GET", rule.GetGet()
	case rule.GetPost() != "":
		method, path = "POST", rule.GetPost()
	case rule.GetPut() != "":
		method, path = "PUT", rule.GetPut()
	case rule.GetPatch() != "":
		method, path = "PATCH", rule.GetPatch()
	case rule.GetDelete() != "":
		method, path = "DELETE", rule.GetDelete()
	case rule.GetCustom() != nil:
		method, path = strings.ToUpper(rule.GetCustom().GetKind()), rule.GetCustom().GetPath()
	}
	bindings := make([]RPCHTTPBinding, 0, 1+len(rule.GetAdditionalBindings()))
	if method != "" && path != "" {
		bindings = append(bindings, RPCHTTPBinding{
			Method:       method,
			Path:         path,
			Body:         rule.GetBody(),
			ResponseBody: rule.GetResponseBody(),
		})
	}
	for _, additional := range rule.GetAdditionalBindings() {
		bindings = append(bindings, flattenHTTPRule(additional)...)
	}
	return bindings
}

func descriptorPolicyTier(policy *policyv1.MethodPolicy) RPCPolicyTier {
	if policy == nil {
		return ""
	}
	switch policy.GetExposure() {
	case policyv1.Exposure_EXPOSURE_PUBLIC:
		return RPCPolicyPublic
	case policyv1.Exposure_EXPOSURE_INTERNAL:
		return RPCPolicyInternal
	case policyv1.Exposure_EXPOSURE_AUTHENTICATED:
	default:
		return ""
	}
	if policy.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE {
		return RPCPolicyPlatformAdmin
	}
	if policy.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE {
		return RPCPolicyMFA
	}
	switch policy.GetTenant() {
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_OWNER:
		return RPCPolicyOrgAdmin
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_TEAM_MEMBER:
		return RPCPolicyOrgMember
	default:
		return RPCPolicyAuthenticated
	}
}

func policyEmitsAudit(policy *policyv1.MethodPolicy) bool {
	return policy != nil && policy.GetAudit() != nil &&
		policy.GetAudit().GetEmission() != policyv1.AuditEmission_AUDIT_EMISSION_NONE &&
		policy.GetAudit().GetEmission() != policyv1.AuditEmission_AUDIT_EMISSION_UNSPECIFIED
}

var policyVocabulary = regexp.MustCompile(`^[a-z*][a-z0-9_*]*:[a-z*][a-z0-9_*]*$`)
var auditEventName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
var entitlementKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateDescriptorPolicy(method protoreflect.MethodDescriptor, policy *policyv1.MethodPolicy) string {
	if policy == nil {
		return "method_policy option is missing"
	}
	var errors []string
	if policy.GetExposure() == policyv1.Exposure_EXPOSURE_UNSPECIFIED {
		errors = append(errors, "exposure is unspecified")
	}
	if policy.GetTenant() == policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED {
		errors = append(errors, "tenant is unspecified")
	}
	if policy.GetMfa() == policyv1.MFARequirement_MFA_REQUIREMENT_UNSPECIFIED {
		errors = append(errors, "mfa is unspecified")
	}
	if policy.GetIdempotency() == policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_UNSPECIFIED {
		errors = append(errors, "idempotency is unspecified")
	}
	if policy.GetRateLimit() == policyv1.RateLimitClass_RATE_LIMIT_CLASS_UNSPECIFIED {
		errors = append(errors, "rate_limit is unspecified")
	}
	if policy.GetRequestSensitivity() == policyv1.Sensitivity_SENSITIVITY_UNSPECIFIED {
		errors = append(errors, "request_sensitivity is unspecified")
	}
	if policy.GetResponseSensitivity() == policyv1.Sensitivity_SENSITIVITY_UNSPECIFIED {
		errors = append(errors, "response_sensitivity is unspecified")
	}
	if policy.GetPlatformRole() == policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_UNSPECIFIED {
		errors = append(errors, "platform_role is unspecified")
	}
	for _, value := range append(cloneStrings(policy.GetPermissions()), policy.GetScopes()...) {
		if !policyVocabulary.MatchString(value) {
			errors = append(errors, fmt.Sprintf("invalid permission/scope %q", value))
		}
	}
	for _, binding := range policy.GetResourceBindings() {
		if binding.GetTarget() == policyv1.ResourceTarget_RESOURCE_TARGET_UNSPECIFIED ||
			binding.GetLookup() == policyv1.ResourceLookup_RESOURCE_LOOKUP_UNSPECIFIED {
			errors = append(errors, "resource binding has an unspecified target or lookup")
		}
		if !requestFieldPathExists(method.Input(), binding.GetRequestField()) {
			errors = append(errors, fmt.Sprintf("resource binding field %q does not exist", binding.GetRequestField()))
		}
	}
	audit := policy.GetAudit()
	if audit == nil || audit.GetEmission() == policyv1.AuditEmission_AUDIT_EMISSION_UNSPECIFIED {
		errors = append(errors, "audit emission is unspecified")
	} else if audit.GetEmission() == policyv1.AuditEmission_AUDIT_EMISSION_NONE && len(audit.GetEvents()) != 0 {
		errors = append(errors, "audit NONE must not declare events")
	} else if audit.GetEmission() != policyv1.AuditEmission_AUDIT_EMISSION_NONE && len(audit.GetEvents()) == 0 {
		errors = append(errors, "audited policy has no event")
	}
	if audit != nil {
		for _, event := range audit.GetEvents() {
			if !auditEventName.MatchString(event) {
				errors = append(errors, fmt.Sprintf("invalid audit event %q", event))
			}
		}
	}
	if policy.GetExposure() == policyv1.Exposure_EXPOSURE_PUBLIC {
		if policy.GetTenant() != policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE ||
			policy.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE ||
			policy.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE ||
			len(policy.GetPermissions()) != 0 || len(policy.GetScopes()) != 0 || len(policy.GetResourceBindings()) != 0 {
			errors = append(errors, "public policy declares authenticated authorization requirements")
		}
	}
	if policy.GetExposure() == policyv1.Exposure_EXPOSURE_INTERNAL &&
		(policy.GetTenant() != policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE ||
			policy.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE ||
			policy.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE) {
		errors = append(errors, "internal policy declares user authorization requirements")
	}
	if policy.GetAuthenticationFactorAttempt() &&
		(policy.GetExposure() != policyv1.Exposure_EXPOSURE_PUBLIC ||
			policy.GetRateLimit() != policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION ||
			policy.GetRequestSensitivity() != policyv1.Sensitivity_SENSITIVITY_SECRET) {
		errors = append(errors, "authentication_factor_attempt requires a public secret authentication request")
	}
	return strings.Join(errors, "; ")
}

func requestFieldPathExists(message protoreflect.MessageDescriptor, fieldPath string) bool {
	if message == nil || fieldPath == "" {
		return false
	}
	parts := strings.Split(fieldPath, ".")
	for i, part := range parts {
		field := message.Fields().ByName(protoreflect.Name(part))
		if field == nil {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		message = field.Message()
		if message == nil {
			return false
		}
	}
	return false
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

// LookupRPCPolicy returns the reviewed policy for a canonical gRPC or Connect
// procedure. Unknown, missing, or invalid descriptor policy returns false.
func LookupRPCPolicy(fullMethod string) (RPCPolicy, bool) {
	if !strings.HasPrefix(fullMethod, "/") {
		fullMethod = "/" + fullMethod
	}
	policyIndexOnce.Do(func() {
		policyIndex = make(map[string]RPCPolicy)
		for _, policy := range RPCPolicies() {
			if policy.Tier.Valid() && policy.PolicyError == "" {
				policyIndex[policy.FullMethod] = policy
			}
		}
	})
	policy, ok := policyIndex[fullMethod]
	if !ok {
		return RPCPolicy{}, false
	}
	return policy, true
}

var (
	policyIndexOnce sync.Once
	policyIndex     map[string]RPCPolicy
)

func (tier RPCPolicyTier) Valid() bool {
	switch tier {
	case RPCPolicyPublic, RPCPolicyAuthenticated, RPCPolicyOrgMember,
		RPCPolicyOrgAdmin, RPCPolicyPlatformAdmin, RPCPolicyMFA, RPCPolicyInternal:
		return true
	default:
		return false
	}
}

func (tier RPCPolicyTier) RequiresUser() bool {
	return tier.Valid() && tier != RPCPolicyPublic && tier != RPCPolicyInternal
}
