//go:build !pure

package business_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// signedAccessClaims returns the iat and exp the token itself carries.
func signedAccessClaims(t *testing.T, token string) (issuedAt, expiresAt time.Time) {
	t.Helper()
	var claims struct {
		IssuedAt  int64 `json:"iat"`
		ExpiresAt int64 `json:"exp"`
	}
	require.NoError(t, json.Unmarshal([]byte(decodeAccessPayload(t, token)), &claims))
	require.NotZero(t, claims.IssuedAt)
	require.NotZero(t, claims.ExpiresAt)
	return time.Unix(claims.IssuedAt, 0), time.Unix(claims.ExpiresAt, 0)
}

// requireExpiresInMatchesToken asserts both halves of the contract: the minter
// signed the configured lifetime, and expires_in reports what is left of it
// without ever claiming more time than the token actually has.
func requireExpiresInMatchesToken(t *testing.T, token string, expiresIn int64, want time.Duration) {
	t.Helper()
	issuedAt, expiresAt := signedAccessClaims(t, token)
	require.Equal(t, want, expiresAt.Sub(issuedAt), "the minter must sign the configured lifetime")
	require.LessOrEqual(t, expiresIn, int64(want.Seconds()),
		"expires_in must never exceed the lifetime the token was granted")
	require.Greater(t, expiresIn, int64(want.Seconds())-30,
		"expires_in must report the token's remaining lifetime")
}

// minterSwapped guards the window in which the shared service is running on a
// non-default minter. testService is process-wide, so two tests holding
// different TTL policies at once would assert against each other's tokens.
var minterSwapped atomic.Bool

// useMinterConfig repoints the service at a minter with a non-default TTL
// policy for the duration of one test, and restores the default afterwards.
//
// It fails rather than swapping if another test already holds the minter: these
// tests mutate shared state, so calling t.Parallel() in one of them would
// otherwise turn a hard invariant into an intermittent wrong-TTL assertion.
func useMinterConfig(t *testing.T, cfg ed25519minter.Config) {
	t.Helper()
	if !minterSwapped.CompareAndSwap(false, true) {
		t.Fatal("another test already holds the shared minter: these tests mutate testService and must not run in parallel")
	}
	testService.SetJWTMinter(newTestMinter(cfg))
	t.Cleanup(func() {
		testService.SetJWTMinter(newTestMinter(ed25519minter.Config{}))
		minterSwapped.Store(false)
	})
}

// seedMagicLink stores a magic link for emailAddr and returns its plaintext
// token, standing in for the delivered email.
func seedMagicLink(t *testing.T, emailAddr string) string {
	t.Helper()
	plaintext := "magic-link-expiry-" + business.NewIDString()
	sum := sha256.Sum256([]byte(plaintext))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.CreateMagicLink(ctx, &business.MagicLink{
			ID:        business.NewIDString(),
			Email:     emailAddr,
			TokenHash: hex.EncodeToString(sum[:]),
			ExpiresAt: time.Now().Add(time.Hour),
		})
	}))
	return plaintext
}

// TestExpiresInMatchesSignedTTLAcrossIssuingEndpoints pins every token-issuing
// response to the exp the minter signed, under a TTL policy that differs from
// the package defaults in both directions — so a response built from a fixed
// duration cannot pass.
func TestExpiresInMatchesSignedTTLAcrossIssuingEndpoints(t *testing.T) {
	clearData(t)

	const accessTTL = 7 * time.Minute
	useMinterConfig(t, ed25519minter.Config{AccessTokenTTL: accessTTL})

	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "expires-in@test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "expires-in", ProviderEmail: "expires-in@test.com",
			EmailVerified: true,
		},
	})
	require.NoError(t, err)

	login, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "expires-in",
		ProviderEmail: "expires-in@test.com",
		EmailVerified: true,
	})
	require.NoError(t, err)
	requireExpiresInMatchesToken(t, login.AccessToken, login.ExpiresIn, accessTTL)

	refreshed, err := testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{
		RefreshToken: login.RefreshToken,
	})
	require.NoError(t, err)
	requireExpiresInMatchesToken(t, refreshed.AccessToken, refreshed.ExpiresIn, accessTTL)

	session, err := testService.JWTMinter().VerifyAccess(refreshed.AccessToken)
	require.NoError(t, err)
	target, err := testService.CreateOrganization(testCtx, registered.User.Uuid, &gen.CreateOrganizationRequest{
		Name: "Expires In Organization",
		Slug: "expires-in-organization",
	})
	require.NoError(t, err)
	switched, err := testService.SwitchOrganization(testCtx, registered.User.Uuid, session.SessionID,
		&gen.SwitchOrganizationRequest{OrganizationId: target.Organization.Id})
	require.NoError(t, err)
	requireExpiresInMatchesToken(t, switched.AccessToken, switched.ExpiresIn, accessTTL)

	magic, err := testService.VerifyMagicLink(testCtx, seedMagicLink(t, "magic-expires-in@test.com"))
	require.NoError(t, err)
	requireExpiresInMatchesToken(t, magic.AccessToken, magic.ExpiresIn, accessTTL)

	_, secret := registerMFAUser(t, "mfa-expires-in", "mfa-expires-in@test.com")
	challenge, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "mfa-expires-in",
		ProviderEmail: "mfa-expires-in@test.com",
		EmailVerified: true,
	})
	require.NoError(t, err)
	require.True(t, challenge.MfaRequired)
	completed, err := testService.CompleteMFAChallenge(testCtx, challenge.MfaToken, testTOTP(t, secret, time.Now()))
	require.NoError(t, err)
	requireExpiresInMatchesToken(t, completed.AccessToken, completed.ExpiresIn, accessTTL)
}

// TestImpersonationExpiresInReportsTheCap covers the divergence the ordinary
// endpoints cannot show: the impersonation token is signed for min(access,
// impersonation) TTL, so the response must report that lower bound in one
// direction and must not over-report in the other.
func TestImpersonationExpiresInReportsTheCap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		access    time.Duration
		imperson  time.Duration
		wantSigns time.Duration
	}{
		{
			name:      "cap below a raised access ttl",
			access:    10 * time.Minute,
			imperson:  5 * time.Minute,
			wantSigns: 5 * time.Minute,
		},
		{
			// The over-reporting direction: a cap shorter than the default
			// constant once made expires_in claim more time than the token had.
			name:      "cap below the default access ttl",
			access:    3 * time.Minute,
			imperson:  time.Minute,
			wantSigns: time.Minute,
		},
		{
			name:      "cap above the access ttl leaves it uncapped",
			access:    2 * time.Minute,
			imperson:  30 * time.Minute,
			wantSigns: 2 * time.Minute,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearData(t)
			useMinterConfig(t, ed25519minter.Config{
				AccessTokenTTL:        tc.access,
				ImpersonationTokenTTL: tc.imperson,
			})

			fixture := seedImpersonationFixture(t, "expires-in")

			resp, err := testService.ImpersonateUser(testCtx, fixture.supportID,
				&gen.ImpersonateUserRequest{
					UserId: fixture.memberID,
					Reason: impersonationReason,
				})
			require.NoError(t, err)
			requireExpiresInMatchesToken(t, resp.AccessToken, resp.ExpiresIn, tc.wantSigns)
		})
	}
}
