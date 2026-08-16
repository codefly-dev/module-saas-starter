package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedAuthorizationMetadataJoinsRESTAndConnectRoutes(t *testing.T) {
	registration := &RouteEntry{Method: "POST", Path: "/v1/users", Protected: false}
	require.NoError(t, applyGeneratedAuthorizationMetadata(registration, generatedAuthorizationByProcedure))
	require.Equal(t, "/saas.accounts.v1.UserService/RegisterUser", registration.Procedure)
	require.False(t, registration.Protected)
	require.Equal(t, edgeRateLimitClassAuthentication, registration.RateLimitClass)
	require.NotEmpty(t, registration.PolicySHA256)

	mfaCompletion := &RouteEntry{Procedure: "/saas.accounts.v1.AuthService/CompleteMFAChallenge"}
	require.NoError(t, applyGeneratedAuthorizationMetadata(mfaCompletion, generatedAuthorizationByProcedure))
	require.False(t, mfaCompletion.Protected)
	require.True(t, mfaCompletion.RateLimitBackendFailClosed)
	require.True(t, mfaCompletion.AuthenticationFactorAttempt)

	unknownExtension := &RouteEntry{Method: "POST", Path: "/v1/auth/magic-link", Protected: false}
	require.NoError(t, applyGeneratedAuthorizationMetadata(unknownExtension, generatedAuthorizationByProcedure))
	require.Empty(t, unknownExtension.Procedure)
	require.Equal(t, edgeRateLimitClassUnspecified, unknownExtension.RateLimitClass)
}

func TestGeneratedAuthorizationMetadataRejectsPolicyDrift(t *testing.T) {
	wrongOverlay := &RouteEntry{Method: "POST", Path: "/v1/users", Protected: true}
	require.ErrorContains(t, applyGeneratedAuthorizationMetadata(wrongOverlay, generatedAuthorizationByProcedure), "disagrees with descriptor exposure")

	internal := &RouteEntry{Procedure: "/saas.accounts.v1.APIKeyService/ValidateAPIKey"}
	require.ErrorContains(t, applyGeneratedAuthorizationMetadata(internal, generatedAuthorizationByProcedure), "cannot be exposed")

	unknownProcedure := &RouteEntry{Procedure: "/saas.accounts.v1.UserService/Unknown"}
	require.ErrorContains(t, applyGeneratedAuthorizationMetadata(unknownProcedure, generatedAuthorizationByProcedure), "metadata is missing")
}
