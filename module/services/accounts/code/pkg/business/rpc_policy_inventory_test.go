package business_test

import (
	"accounts/pkg/business"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
	"testing"

	"github.com/stretchr/testify/require"
)

// Descriptor policy inventory is independent of a running service and remains
// enforced in both pure owner checks and the full hosted service suite.
func TestRPCPolicyInventoryIsCompleteAndClassified(t *testing.T) {
	policies := business.RPCPolicies()
	require.NotEmpty(t, policies)
	seen := make(map[string]struct{}, len(policies))
	streaming := make(map[string]bool)
	var internalWithoutHTTP []string
	for _, policy := range policies {
		require.Empty(t, policy.PolicyError, "%s has invalid descriptor policy", policy.FullMethod)
		require.NotNil(t, policy.MethodPolicy, "%s has no descriptor policy", policy.FullMethod)
		require.True(t, policy.Tier.Valid(), "%s has invalid tier %q", policy.FullMethod, policy.Tier)
		if policy.MethodPolicy.GetExposure() == policyv1.Exposure_EXPOSURE_INTERNAL {
			require.Empty(t, policy.HTTPMethod, "%s must not opt into REST", policy.FullMethod)
			require.Empty(t, policy.HTTPPath, "%s must not opt into REST", policy.FullMethod)
			internalWithoutHTTP = append(internalWithoutHTTP, policy.FullMethod)
		} else {
			require.Equal(t, policy.HTTPMethod == "", policy.HTTPPath == "", "%s has incomplete HTTP metadata", policy.FullMethod)
		}
		require.NotEmpty(t, policy.Description, "%s has no description", policy.FullMethod)
		_, duplicate := seen[policy.FullMethod]
		require.False(t, duplicate, "duplicate policy for %s", policy.FullMethod)
		seen[policy.FullMethod] = struct{}{}
		streaming[policy.FullMethod] = policy.Streaming
	}
	require.ElementsMatch(t, []string{
		"/saas.accounts.v1.APIKeyService/ValidateAPIKey",
		"/saas.accounts.v1.ClientRegistryService/ListRegisteredClients",
		"/saas.accounts.v1.IdentityService/ResolveIdentity",
		"/saas.accounts.v1.ModuleCapabilitiesService/ListReadableSourceCollections",
		"/saas.accounts.v1.ModuleCapabilitiesService/CheckWorkContextRecordAccess",
		"/saas.accounts.v1.ModuleCapabilitiesService/ExchangeDelegatedReadAudience",
		"/saas.accounts.v1.ModuleCapabilitiesService/ExchangeDelegatedOperationAudience",
		"/saas.accounts.v1.ModuleCapabilitiesService/PlaceRecord",
		"/saas.accounts.v1.ModuleCapabilitiesService/EnqueueJob",
		"/saas.accounts.v1.ModuleCapabilitiesService/ClaimJobs",
		"/saas.accounts.v1.ModuleCapabilitiesService/HeartbeatJob",
		"/saas.accounts.v1.ModuleCapabilitiesService/AckJob",
		"/saas.accounts.v1.ModuleCapabilitiesService/NackJob",
		"/saas.accounts.v1.ModuleCapabilitiesService/NotifyUser",
		"/saas.accounts.v1.ModuleCapabilitiesService/RequestApproval",
		"/saas.accounts.v1.ModuleCapabilitiesService/GetApproval",
		"/saas.accounts.v1.ModuleCapabilitiesService/CancelApproval",
		"/saas.accounts.v1.ModuleCapabilitiesService/EmitAuditEvent",
		"/saas.accounts.v1.ModuleCapabilitiesService/ListSubjectVisibility",
		"/saas.accounts.v1.ModuleCapabilitiesService/FetchDatasourceBlob",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleRegistration",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleWorkContext",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleOperationContext",
		"/saas.accounts.v1.ModuleCapabilitiesService/MintSolutionRegistration",
		"/saas.accounts.v1.ModuleCapabilitiesService/PublishEvent",
		"/saas.accounts.v1.ModuleCapabilitiesService/Subscribe",
		"/saas.accounts.v1.ModuleCapabilitiesService/Unsubscribe",
		"/saas.accounts.v1.ModuleCapabilitiesService/ListSubscriptions",
		"/saas.accounts.v1.ModuleCapabilitiesService/ReplayEvents",
		"/saas.accounts.v1.PermissionService/CheckAccess",
		"/saas.accounts.v1.PermissionService/CheckPermission",
		"/saas.accounts.v1.PermissionService/Decide",
		"/saas.accounts.v1.PermissionService/ListAccessibleScopes",
		"/saas.accounts.v1.PrincipalService/DisableAgentPrincipal",
		"/saas.accounts.v1.PrincipalService/EnableAgentPrincipal",
		"/saas.accounts.v1.PrincipalService/GetAgentPrincipal",
		"/saas.accounts.v1.PrincipalService/GetPrincipal",
		"/saas.accounts.v1.SolutionRegistryService/DeleteSolutionRegistration",
		"/saas.accounts.v1.SolutionRegistryService/ListSolutionRegistrations",
		"/saas.accounts.v1.SolutionRegistryService/PutSolutionRegistration",
		"/saas.accounts.v1.UsageService/ConsumeUsage",
		"/saas.accounts.v1.WorkContextService/AuthorizeEvidenceRead",
		"/saas.accounts.v1.WorkContextService/CheckAuthorizationRevision",
		"/saas.accounts.v1.WorkContextService/ConsumeSingleUse",
		"/saas.accounts.v1.WorkContextService/StartInstallationTask",
	}, internalWithoutHTTP, "the exact internal RPC inventory must remain off the REST surface")
	require.True(t, streaming["/saas.accounts.v1.DelegationService/WaitForDelegation"], "server-streaming RPC must be present and marked streaming")
	require.True(t, streaming["/saas.accounts.v1.ModuleCapabilitiesService/FetchDatasourceBlob"], "server-streaming RPC must be present and marked streaming")
}
