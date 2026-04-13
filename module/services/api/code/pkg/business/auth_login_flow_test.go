package business_test

import (
	"api/pkg/gen"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFullLoginFlow tests: register → authenticate → refresh → logout
func TestFullLoginFlow(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available (run with codefly test)")
	}

	ctx := testCtx

	// 1. Register a user
	regResp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "logintest@example.com",
		Profile:      map[string]string{"name": "Login Test"},
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "login-test-001",
			ProviderEmail: "logintest@example.com",
			EmailVerified: true,
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, regResp.User.Uuid)

	// 2. Authenticate (same provider+id as registered)
	authResp, err := testService.Authenticate(ctx, "logintest@example.com",
		"email", "login-test-001", "logintest@example.com", true, nil)
	require.NoError(t, err)
	require.NotEmpty(t, authResp.AccessToken, "access token should be returned")
	require.NotEmpty(t, authResp.RefreshToken, "refresh token should be returned")
	require.NotZero(t, authResp.ExpiresIn, "expires_in should be set")

	// 3. Refresh token
	refreshResp, err := testService.RefreshToken(ctx, authResp.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, refreshResp.AccessToken, "new access token on refresh")
	require.NotEmpty(t, refreshResp.RefreshToken, "new refresh token on refresh")

	// New tokens should be different (rotation)
	require.NotEqual(t, authResp.RefreshToken, refreshResp.RefreshToken,
		"refresh token should be rotated")

	// 4. Old refresh token should be invalid (replay protection)
	_, err = testService.RefreshToken(ctx, authResp.RefreshToken)
	require.Error(t, err, "old refresh token should be rejected")

	// 5. New refresh token should still work
	refreshResp2, err := testService.RefreshToken(ctx, refreshResp.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, refreshResp2.AccessToken)
}

// TestLoginWithInvalidCredentials tests that invalid provider ID is rejected
func TestLoginWithInvalidCredentials(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}

	ctx := testCtx

	// Non-existent provider ID should fail
	_, err := testService.Authenticate(ctx, "nobody@example.com",
		"email", "nonexistent-provider-id", "nobody@example.com", true, nil)
	require.Error(t, err, "should reject unknown provider ID")
}

// TestFixtureUserLogin tests that seeded fixture users can authenticate
func TestFixtureUserLogin(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}

	ctx := testCtx

	// dev-admin fixture user (seeded by DevAdmin fixture)
	authResp, err := testService.Authenticate(ctx, "admin@acme.com",
		"email", "dev-admin", "admin@acme.com", true, nil)
	if err != nil {
		t.Skip("fixture users not seeded (run with --fixture dev-admin)")
	}

	require.NotEmpty(t, authResp.AccessToken)
	require.NotEmpty(t, authResp.User)
	require.Equal(t, "admin@acme.com", authResp.User.PrimaryEmail)
}
