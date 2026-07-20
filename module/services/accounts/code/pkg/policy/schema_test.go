package policy_test

import (
	"testing"

	policyv1 "accounts/pkg/gen/saas/policy/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestMethodPolicyExtensionContract(t *testing.T) {
	extension, err := protoregistry.GlobalTypes.FindExtensionByName("saas.policy.v1.method_policy")
	require.NoError(t, err)
	require.Equal(t, protoreflect.FieldNumber(51000), extension.TypeDescriptor().Number())
	require.Equal(t, protoreflect.MessageKind, extension.TypeDescriptor().Kind())
	require.Equal(t, protoreflect.FullName("google.protobuf.MethodOptions"), extension.TypeDescriptor().ContainingMessage().FullName())
	require.Equal(t, protoreflect.FullName("saas.policy.v1.MethodPolicy"), extension.TypeDescriptor().Message().FullName())
	require.Equal(t, policyv1.E_MethodPolicy.TypeDescriptor().FullName(), extension.TypeDescriptor().FullName())
}

func TestMethodPolicyExtensionRoundTrip(t *testing.T) {
	options := &descriptorpb.MethodOptions{}
	want := &policyv1.MethodPolicy{
		Exposure:    policyv1.Exposure_EXPOSURE_AUTHENTICATED,
		Tenant:      policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN,
		Permissions: []string{"webhooks:write"},
		Scopes:      []string{"webhooks:write"},
		Mfa:         policyv1.MFARequirement_MFA_REQUIREMENT_RECENT_STEP_UP,
		Audit: &policyv1.AuditPolicy{
			Events:   []string{"webhook.secret_rotated"},
			Emission: policyv1.AuditEmission_AUDIT_EMISSION_SUCCESS,
		},
		Idempotency:         policyv1.IdempotencyRequirement_IDEMPOTENCY_REQUIREMENT_REQUIRED,
		RateLimit:           policyv1.RateLimitClass_RATE_LIMIT_CLASS_SENSITIVE,
		RequestSensitivity:  policyv1.Sensitivity_SENSITIVITY_SECRET,
		ResponseSensitivity: policyv1.Sensitivity_SENSITIVITY_SECRET,
		PlatformRole:        policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
	}

	proto.SetExtension(options, policyv1.E_MethodPolicy, want)
	require.True(t, proto.HasExtension(options, policyv1.E_MethodPolicy))
	got, ok := proto.GetExtension(options, policyv1.E_MethodPolicy).(*policyv1.MethodPolicy)
	require.True(t, ok)
	require.True(t, proto.Equal(want, got))
}
