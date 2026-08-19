package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnectRouteDiscoveryExcludesInternalRPCs(t *testing.T) {
	entries, err := LoadConnectRoutesFromCatalog()
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	paths := make(map[string]bool, len(entries))
	protected := make(map[string]bool, len(entries))
	for _, entry := range entries {
		paths[entry.Path] = true
		protected[entry.Path] = entry.Protected
		policyPath := entry.Procedure
		policy, classified := generatedAuthorizationByProcedure[policyPath]
		require.True(t, classified, policyPath)
		require.NotEqual(t, edgeExposureInternal, policy.exposure, policyPath)
		require.Equal(t, policy.rateLimitClass, entry.RateLimitClass, policyPath)
		require.NotEmpty(t, entry.PolicySHA256, policyPath)
	}
	require.False(t, paths["/saas.accounts.v1.APIKeyService/ValidateAPIKey"])
	require.False(t, paths["/saas.accounts.v1.PermissionService/CheckPermission"])
	require.True(t, paths["/saas.accounts.v1.AuthService/CompleteMFAChallenge"])
	require.False(t, protected["/saas.accounts.v1.AuthService/CompleteMFAChallenge"], "MFA login completion uses its one-use bearer instead of an access token")
	require.True(t, paths["/saas.accounts.v1.AuthService/BeginWebAuthnMFAChallenge"])
	require.False(t, protected["/saas.accounts.v1.AuthService/BeginWebAuthnMFAChallenge"])
	require.True(t, paths["/saas.accounts.v1.AuthService/CompleteWebAuthnMFAChallenge"])
	require.False(t, protected["/saas.accounts.v1.AuthService/CompleteWebAuthnMFAChallenge"])
	require.True(t, paths["/customers.AuthService/CompleteWebAuthnMFAChallenge"])
	require.False(t, protected["/customers.AuthService/CompleteWebAuthnMFAChallenge"])
	require.True(t, paths["/saas.accounts.v1.AuthService/BeginOAuth"])
	require.False(t, protected["/saas.accounts.v1.AuthService/BeginOAuth"])
	require.True(t, paths["/saas.accounts.v1.IntrospectionService/GetServiceInfo"])
	require.False(t, protected["/saas.accounts.v1.IntrospectionService/GetServiceInfo"])
	require.True(t, paths["/saas.accounts.v1.BillingService/ListPublicPlans"])
	require.False(t, protected["/saas.accounts.v1.BillingService/ListPublicPlans"])
	require.True(t, paths["/customers.BillingService/ListPublicPlans"])
	require.False(t, protected["/customers.BillingService/ListPublicPlans"])
	require.True(t, paths["/saas.accounts.v1.UserService/RegisterUser"])
	require.False(t, protected["/saas.accounts.v1.UserService/RegisterUser"])
	require.True(t, paths["/saas.accounts.v1.WaitlistService/Join"])
	require.False(t, protected["/saas.accounts.v1.WaitlistService/Join"])
	require.True(t, paths["/saas.accounts.v1.PlatformAdminService/GetJobOperations"])
	require.True(t, protected["/saas.accounts.v1.PlatformAdminService/GetJobOperations"])
	require.True(t, paths["/saas.accounts.v1.PlatformAdminService/ReplayJob"])
	require.True(t, protected["/saas.accounts.v1.PlatformAdminService/ReplayJob"])
	require.True(t, paths["/customers.PlatformAdminService/ReplayJob"])
	require.True(t, protected["/customers.PlatformAdminService/ReplayJob"])
	require.True(t, paths["/saas.accounts.v1.WorkContextService/ExchangeAudience"])
	require.True(t, protected["/saas.accounts.v1.WorkContextService/ExchangeAudience"])
	require.True(t, paths["/customers.WorkContextService/ExchangeAudience"])
	require.True(t, protected["/customers.WorkContextService/ExchangeAudience"])
	require.True(t, paths["/saas.accounts.v1.UsageService/GetUsageHistory"])
	require.True(t, protected["/saas.accounts.v1.UsageService/GetUsageHistory"])
	require.True(t, paths["/customers.UsageService/GetUsageHistory"])
	require.True(t, protected["/customers.UsageService/GetUsageHistory"])
	require.Len(t, entries, 278)
	require.True(t, paths["/saas.accounts.v1.PlatformAdminService/UpsertFeatureFlag"])

	var legacy *RouteEntry
	for _, entry := range entries {
		if entry.Path == "/customers.UserService/GetSelf" {
			legacy = entry
			break
		}
	}
	require.NotNil(t, legacy)
	require.Equal(t, "/saas.accounts.v1.UserService/GetSelf", legacy.UpstreamPath)
	require.Equal(t, "/saas.accounts.v1.UserService/GetSelf", legacy.Procedure)
}
