package cataloggen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"regexp"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/business"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
	"accounts/pkg/policy/condition"
)

const authorizationCatalogSchemaVersion = "saas.authz.methods.v1"

var (
	policyValuePattern = regexp.MustCompile(`^[a-z*][a-z0-9_*]*:[a-z*][a-z0-9_*]*$`)
	auditEventPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// BuildAuthorizationCatalog projects the descriptor-owned service catalog into
// the transport-independent method metadata consumed by PDPs and edge policy
// adapters. All procedures, including internal ones, remain in this artifact.
func BuildAuthorizationCatalog(serviceDocument []byte) (*catalogv1.AuthorizationCatalog, error) {
	serviceCatalog := &catalogv1.ServiceCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, serviceCatalog); err != nil {
		return nil, fmt.Errorf("decode service catalog: %w", err)
	}
	if err := business.ValidateServiceCatalog(serviceCatalog); err != nil {
		return nil, fmt.Errorf("validate service catalog: %w", err)
	}

	catalog := &catalogv1.AuthorizationCatalog{
		SchemaVersion: authorizationCatalogSchemaVersion,
		Owner:         cloneOwner(serviceCatalog.GetOwner()),
		Methods:       make([]*catalogv1.AuthorizationMethod, 0, len(serviceCatalog.GetMethods())),
	}
	for _, method := range serviceCatalog.GetMethods() {
		methodPolicy, _ := proto.Clone(method.GetPolicy()).(*policyv1.MethodPolicy)
		catalog.Methods = append(catalog.Methods, &catalogv1.AuthorizationMethod{
			Procedure:                   method.GetProcedure(),
			FullService:                 method.GetFullService(),
			Method:                      method.GetMethod(),
			InputType:                   method.GetInputType(),
			OutputType:                  method.GetOutputType(),
			ClientStreaming:             method.GetClientStreaming(),
			ServerStreaming:             method.GetServerStreaming(),
			Policy:                      methodPolicy,
			PolicyTier:                  method.GetPolicyTier(),
			Owner:                       cloneOwner(method.GetOwner()),
			SourceProto:                 method.GetSourceProto(),
			PolicySha256:                policySHA256(methodPolicy),
			RateLimitBackendFailureMode: rateLimitBackendFailureMode(methodPolicy),
		})
	}
	if err := ValidateAuthorizationCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func rateLimitBackendFailureMode(policy *policyv1.MethodPolicy) catalogv1.RateLimitBackendFailureMode {
	if policy != nil && (policy.GetRateLimit() == policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION ||
		policy.GetRateLimit() == policyv1.RateLimitClass_RATE_LIMIT_CLASS_MFA) {
		return catalogv1.RateLimitBackendFailureMode_RATE_LIMIT_BACKEND_FAILURE_MODE_FAIL_CLOSED
	}
	return catalogv1.RateLimitBackendFailureMode_RATE_LIMIT_BACKEND_FAILURE_MODE_FAIL_OPEN
}

func policySHA256(policy *policyv1.MethodPolicy) string {
	document, err := (proto.MarshalOptions{Deterministic: true}).Marshal(policy)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

// ValidateAuthorizationCatalog enforces the standalone PDP artifact contract.
// It deliberately validates policy values again rather than trusting the
// service-catalog producer or repairing malformed generated data.
func ValidateAuthorizationCatalog(catalog *catalogv1.AuthorizationCatalog) error {
	if catalog == nil {
		return fmt.Errorf("authorization catalog is nil")
	}
	if catalog.GetSchemaVersion() != authorizationCatalogSchemaVersion {
		return fmt.Errorf("unsupported authorization catalog schema version %q", catalog.GetSchemaVersion())
	}
	if catalog.GetOwner().GetModule() == "" || catalog.GetOwner().GetService() == "" {
		return fmt.Errorf("authorization catalog owner is incomplete")
	}
	if len(catalog.GetMethods()) == 0 {
		return fmt.Errorf("authorization catalog contains no methods")
	}

	previousProcedure := ""
	for _, method := range catalog.GetMethods() {
		if method == nil || method.GetProcedure() == "" || method.GetFullService() == "" || method.GetMethod() == "" ||
			method.GetInputType() == "" || method.GetOutputType() == "" || method.GetSourceProto() == "" {
			return fmt.Errorf("authorization method identity is incomplete")
		}
		if previousProcedure != "" && method.GetProcedure() <= previousProcedure {
			return fmt.Errorf("authorization methods are not strictly sorted at %q", method.GetProcedure())
		}
		previousProcedure = method.GetProcedure()
		if method.GetProcedure() != "/"+method.GetFullService()+"/"+method.GetMethod() {
			return fmt.Errorf("procedure %q does not match its service and method identity", method.GetProcedure())
		}
		if method.GetOwner().GetModule() != catalog.GetOwner().GetModule() ||
			method.GetOwner().GetService() != catalog.GetOwner().GetService() {
			return fmt.Errorf("procedure %q owner does not match catalog owner", method.GetProcedure())
		}
		if !business.RPCPolicyTier(method.GetPolicyTier()).Valid() {
			return fmt.Errorf("procedure %q has invalid policy tier %q", method.GetProcedure(), method.GetPolicyTier())
		}
		if err := validateAuthorizationMethodPolicy(method.GetPolicy()); err != nil {
			return fmt.Errorf("procedure %q has invalid policy: %w", method.GetProcedure(), err)
		}
		if !sha256Pattern.MatchString(method.GetPolicySha256()) || method.GetPolicySha256() != policySHA256(method.GetPolicy()) {
			return fmt.Errorf("procedure %q policy hash does not match policy", method.GetProcedure())
		}
		expectedFailureMode := rateLimitBackendFailureMode(method.GetPolicy())
		if method.GetRateLimitBackendFailureMode() != expectedFailureMode {
			return fmt.Errorf("procedure %q has invalid rate-limit backend failure mode", method.GetProcedure())
		}
	}
	return nil
}

func validateAuthorizationMethodPolicy(policy *policyv1.MethodPolicy) error {
	if policy == nil {
		return fmt.Errorf("policy is missing")
	}
	if policy.GetExposure() == policyv1.Exposure_EXPOSURE_UNSPECIFIED ||
		policy.GetTenant() == policyv1.TenantRequirement_TENANT_REQUIREMENT_UNSPECIFIED ||
		policy.GetMfa() == policyv1.MFARequirement_MFA_REQUIREMENT_UNSPECIFIED ||
		policy.GetIdempotency() == policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_UNSPECIFIED ||
		policy.GetRateLimit() == policyv1.RateLimitClass_RATE_LIMIT_CLASS_UNSPECIFIED ||
		policy.GetRequestSensitivity() == policyv1.Sensitivity_SENSITIVITY_UNSPECIFIED ||
		policy.GetResponseSensitivity() == policyv1.Sensitivity_SENSITIVITY_UNSPECIFIED ||
		policy.GetPlatformRole() == policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_UNSPECIFIED {
		return fmt.Errorf("required policy enum is unspecified")
	}
	for _, value := range append(append([]string(nil), policy.GetPermissions()...), policy.GetScopes()...) {
		if !policyValuePattern.MatchString(value) {
			return fmt.Errorf("invalid permission or scope %q", value)
		}
	}
	for _, binding := range policy.GetResourceBindings() {
		if binding.GetRequestField() == "" || binding.GetTarget() == policyv1.ResourceTarget_RESOURCE_TARGET_UNSPECIFIED ||
			binding.GetLookup() == policyv1.ResourceLookup_RESOURCE_LOOKUP_UNSPECIFIED {
			return fmt.Errorf("resource binding is incomplete")
		}
	}
	if err := condition.Validate(policy.GetConditions()); err != nil {
		return fmt.Errorf("invalid attribute condition: %w", err)
	}
	audit := policy.GetAudit()
	if audit == nil || audit.GetEmission() == policyv1.AuditEmission_AUDIT_EMISSION_UNSPECIFIED {
		return fmt.Errorf("audit emission is unspecified")
	}
	if audit.GetEmission() == policyv1.AuditEmission_AUDIT_EMISSION_NONE && len(audit.GetEvents()) != 0 {
		return fmt.Errorf("audit NONE declares events")
	}
	if audit.GetEmission() != policyv1.AuditEmission_AUDIT_EMISSION_NONE && len(audit.GetEvents()) == 0 {
		return fmt.Errorf("audited policy has no event")
	}
	for _, event := range audit.GetEvents() {
		if !auditEventPattern.MatchString(event) {
			return fmt.Errorf("invalid audit event %q", event)
		}
	}
	if policy.GetExposure() == policyv1.Exposure_EXPOSURE_PUBLIC &&
		(policy.GetTenant() != policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE ||
			policy.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE ||
			policy.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE ||
			len(policy.GetPermissions()) != 0 || len(policy.GetScopes()) != 0 || len(policy.GetResourceBindings()) != 0) {
		return fmt.Errorf("public policy declares authenticated requirements")
	}
	if policy.GetAuthenticationFactorAttempt() &&
		(policy.GetExposure() != policyv1.Exposure_EXPOSURE_PUBLIC ||
			policy.GetRateLimit() != policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION ||
			policy.GetRequestSensitivity() != policyv1.Sensitivity_SENSITIVITY_SECRET) {
		return fmt.Errorf("authentication-factor attempt is not a public secret authentication request")
	}
	return nil
}

// RenderAuthorizationCatalogJSON emits stable proto-name JSON.
func RenderAuthorizationCatalogJSON(catalog *catalogv1.AuthorizationCatalog) ([]byte, error) {
	if err := ValidateAuthorizationCatalog(catalog); err != nil {
		return nil, err
	}
	document, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("marshal authorization catalog: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, document, "", "  "); err != nil {
		return nil, fmt.Errorf("format authorization catalog: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

// RenderAuthSidecarAuthorizationMetadata emits the edge-safe subset of the
// full PDP catalog plus exact descriptor REST joins used while the legacy REST
// overlay is being retired.
func RenderAuthSidecarAuthorizationMetadata(authz *catalogv1.AuthorizationCatalog, service *catalogv1.ServiceCatalog) ([]byte, error) {
	if err := ValidateAuthorizationCatalog(authz); err != nil {
		return nil, err
	}
	if err := business.ValidateServiceCatalog(service); err != nil {
		return nil, err
	}
	if len(authz.GetMethods()) != len(service.GetMethods()) {
		return nil, fmt.Errorf("authorization catalog has %d methods; service catalog has %d", len(authz.GetMethods()), len(service.GetMethods()))
	}

	var source strings.Builder
	source.WriteString(`// Code generated by cmd/authz-methods from generated/authz-methods.json. DO NOT EDIT.

package main

type generatedAuthorizationMetadata struct {
	exposure                    edgeExposure
	rateLimitClass              edgeRateLimitClass
	rateLimitBackendFailClosed  bool
	authenticationFactorAttempt bool
	policySHA256                string
}

var generatedAuthorizationByProcedure = map[string]generatedAuthorizationMetadata{
`)
	for index, method := range authz.GetMethods() {
		serviceMethod := service.GetMethods()[index]
		if method.GetProcedure() != serviceMethod.GetProcedure() || !proto.Equal(method.GetPolicy(), serviceMethod.GetPolicy()) {
			return nil, fmt.Errorf("authorization and service catalogs disagree at %q", method.GetProcedure())
		}
		exposure, err := authSidecarExposureConstant(method.GetPolicy().GetExposure())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method.GetProcedure(), err)
		}
		rateLimit, err := authSidecarRateLimitConstant(method.GetPolicy().GetRateLimit())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method.GetProcedure(), err)
		}
		fmt.Fprintf(&source, "\t%q: {exposure: %s, rateLimitClass: %s, rateLimitBackendFailClosed: %t, authenticationFactorAttempt: %t, policySHA256: %q},\n",
			method.GetProcedure(),
			exposure,
			rateLimit,
			method.GetRateLimitBackendFailureMode() == catalogv1.RateLimitBackendFailureMode_RATE_LIMIT_BACKEND_FAILURE_MODE_FAIL_CLOSED,
			method.GetPolicy().GetAuthenticationFactorAttempt(),
			method.GetPolicySha256(),
		)
	}
	source.WriteString("}\n\nvar generatedRESTProcedureByRoute = map[string]string{\n")
	for _, method := range service.GetMethods() {
		for _, binding := range method.GetHttpBindings() {
			fmt.Fprintf(&source, "\t%q: %q,\n", strings.ToUpper(binding.GetMethod())+" "+binding.GetPath(), method.GetProcedure())
		}
	}
	source.WriteString("}\n")
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format auth-sidecar authorization metadata: %w", err)
	}
	return formatted, nil
}

func authSidecarExposureConstant(value policyv1.Exposure) (string, error) {
	switch value {
	case policyv1.Exposure_EXPOSURE_UNSPECIFIED:
		return "edgeExposureUnspecified", nil
	case policyv1.Exposure_EXPOSURE_PUBLIC:
		return "edgeExposurePublic", nil
	case policyv1.Exposure_EXPOSURE_AUTHENTICATED:
		return "edgeExposureAuthenticated", nil
	case policyv1.Exposure_EXPOSURE_INTERNAL:
		return "edgeExposureInternal", nil
	default:
		return "", fmt.Errorf("unsupported edge exposure %s", value)
	}
}

func authSidecarRateLimitConstant(value policyv1.RateLimitClass) (string, error) {
	switch value {
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_UNSPECIFIED:
		return "edgeRateLimitClassUnspecified", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_PUBLIC:
		return "edgeRateLimitClassPublic", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION:
		return "edgeRateLimitClassAuthentication", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_READ:
		return "edgeRateLimitClassStandardRead", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_WRITE:
		return "edgeRateLimitClassStandardWrite", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_SENSITIVE:
		return "edgeRateLimitClassSensitive", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_WEBHOOK:
		return "edgeRateLimitClassWebhook", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_INTERNAL:
		return "edgeRateLimitClassInternal", nil
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_MFA:
		return "edgeRateLimitClassMFA", nil
	default:
		return "", fmt.Errorf("unsupported edge rate-limit class %s", value)
	}
}
