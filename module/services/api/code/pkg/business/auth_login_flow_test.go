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
	// Isolate from other tests — otherwise a previous run's
	// logintest@example.com registration makes RegisterUser below fail
	// with "user already exists with this identity".
	clearData(t)

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
	authResp, err := testService.Authenticate(ctx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "login-test-001",
		ProviderEmail: "logintest@example.com",
		EmailVerified: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, authResp.AccessToken, "access token should be returned")
	require.NotEmpty(t, authResp.RefreshToken, "refresh token should be returned")
	require.NotZero(t, authResp.ExpiresIn, "expires_in should be set")

	// 3. Refresh token
	refreshResp, err := testService.RefreshToken(ctx, &gen.RefreshTokenRequest{RefreshToken: authResp.RefreshToken})
	require.NoError(t, err)
	require.NotEmpty(t, refreshResp.AccessToken, "new access token on refresh")
	require.NotEmpty(t, refreshResp.RefreshToken, "new refresh token on refresh")

	// New tokens should be different (rotation)
	require.NotEqual(t, authResp.RefreshToken, refreshResp.RefreshToken,
		"refresh token should be rotated")

	// 4. Old refresh token should be invalid (replay protection).
	// When a refresh token is replayed, the defensive response is to
	// invalidate the ENTIRE token family — reuse signals probable token
	// theft, so we revoke both the replayed token AND its successor to
	// log any attacker out of the compromised chain.
	_, err = testService.RefreshToken(ctx, &gen.RefreshTokenRequest{RefreshToken: authResp.RefreshToken})
	require.Error(t, err, "old refresh token should be rejected")

	// 5. After the replay was detected, the successor token (B) is ALSO
	// now revoked — this is intentional family-wide revocation. A fresh
	// Authenticate would be needed to get a new valid chain.
	_, err = testService.RefreshToken(ctx, &gen.RefreshTokenRequest{RefreshToken: refreshResp.RefreshToken})
	require.Error(t, err, "replayed family should be fully revoked")
}

// TestLoginWithAutoRegister verifies that Authenticate transparently
// creates a new user on first sign-in. This is the production OAuth
// pattern: the first time a user signs in via Google/WorkOS, we treat
// the provider-verified identity as a trusted signup.
//
// Previously this test expected Authenticate to reject unknown provider
// IDs outright — that was the wrong test shape. IDP strictness is
// enforced BEFORE Authenticate is called (by the token validator); once
// we get here, the identity is already trusted and auto-register is the
// intended flow.
func TestLoginWithAutoRegister(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}
	clearData(t)

	ctx := testCtx

	// A never-seen-before provider_id should auto-register.
	authResp, err := testService.Authenticate(ctx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "brand-new-user",
		ProviderEmail: "new@example.com",
		EmailVerified: true,
	})
	require.NoError(t, err, "auto-register on first sign-in should succeed")
	require.NotEmpty(t, authResp.AccessToken)
	require.NotNil(t, authResp.User)
	require.NotEmpty(t, authResp.User.Uuid)
	require.Equal(t, "new@example.com", authResp.User.PrimaryEmail,
		"Authenticate should return the auto-registered user's email")
}

// TestLoginReturnsUserDetails ensures Authenticate populates the full
// user record (email, status, profile) in the response — not just the
// bare UUID the implementation used to return. Frontend and test harness
// callers need the email to render the authed user in the UI without an
// extra GetUser round trip.
func TestLoginReturnsUserDetails(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}
	clearData(t)

	ctx := testCtx

	// First register via the explicit RegisterUser path so we have a
	// known-good user record to authenticate against.
	_, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "known@example.com",
		Profile:      map[string]string{"name": "Known User"},
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "known-user-001",
			ProviderEmail: "known@example.com",
			EmailVerified: true,
		},
	})
	require.NoError(t, err)

	authResp, err := testService.Authenticate(ctx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "known-user-001",
		ProviderEmail: "known@example.com",
		EmailVerified: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, authResp.AccessToken)
	require.NotNil(t, authResp.User)
	require.Equal(t, "known@example.com", authResp.User.PrimaryEmail)
	require.Equal(t, gen.UserStatus_USER_STATUS_ACTIVE, authResp.User.Status)
}
