package business_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func testTOTP(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	require.NoError(t, err)
	counter := uint64(at.Unix()) / 30
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, raw)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

func registerMFAUser(t *testing.T, subject, email string) (userID, secret string) {
	t.Helper()
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    subject,
			ProviderEmail: email,
			EmailVerified: true,
		},
	})
	require.NoError(t, err)
	secret, _, err = testService.SetupTOTP(testCtx, registered.User.Uuid)
	require.NoError(t, err)
	var devices []*business.MFADevice
	require.NoError(t, testStore.WithUserTx(testCtx, registered.User.Uuid, func(ctx context.Context) error {
		var err error
		devices, err = testStore.ListMFADevices(ctx, registered.User.Uuid)
		return err
	}))
	require.Len(t, devices, 1)
	require.NotEqual(t, secret, devices[0].SecretEncrypted)
	require.False(t, strings.Contains(devices[0].SecretEncrypted, secret), "database value must not contain the TOTP seed")
	code := testTOTP(t, secret, time.Now())
	require.NoError(t, testService.VerifyTOTP(testCtx, registered.User.Uuid, code))
	return registered.User.Uuid, secret
}

func TestMFALoginIssuesNoSessionBeforeChallengeAndConsumesOnce(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}
	clearData(t)

	userID, secretA := registerMFAUser(t, "mfa-login-a", "mfa-a@example.com")
	_, secretB := registerMFAUser(t, "mfa-login-b", "mfa-b@example.com")

	primary, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "mfa-login-a",
		ProviderEmail: "mfa-a@example.com",
		DeviceInfo:    "Firefox on Linux",
	})
	require.NoError(t, err)
	require.True(t, primary.MfaRequired)
	require.NotEmpty(t, primary.MfaToken)
	require.Empty(t, primary.AccessToken, "primary auth must not mint access before MFA")
	require.Empty(t, primary.RefreshToken, "primary auth must not mint refresh before MFA")
	var sessions []*business.Session
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		var err error
		sessions, err = testStore.ListActiveSessions(ctx, userID, 10)
		return err
	}))
	require.Empty(t, sessions, "primary auth must not persist a refreshable session before MFA")

	// A code derived from another user's authenticator must not complete A's
	// transaction. Avoid the vanishingly small numeric-code collision across
	// A's accepted +/- one-step window by choosing a nearby B time step.
	acceptedA := map[string]bool{}
	for _, delta := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		acceptedA[testTOTP(t, secretA, time.Now().Add(delta))] = true
	}
	var crossUserCode string
	for step := 0; step < 10; step++ {
		candidate := testTOTP(t, secretB, time.Now().Add(time.Duration(step)*30*time.Second))
		if !acceptedA[candidate] {
			crossUserCode = candidate
			break
		}
	}
	require.NotEmpty(t, crossUserCode)
	_, err = testService.CompleteMFAChallenge(testCtx, primary.MfaToken, crossUserCode)
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected)

	// Two replicas racing the same transaction may both validate the bearer,
	// but the database row lock permits exactly one session-issuing commit.
	codeA := testTOTP(t, secretA, time.Now())
	type result struct {
		resp *gen.CompleteMFAChallengeResponse
		err  error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := testService.CompleteMFAChallenge(context.Background(), primary.MfaToken, codeA)
			results <- result{resp: resp, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var winner *gen.CompleteMFAChallengeResponse
	var rejected int
	for got := range results {
		if got.err == nil {
			require.Nil(t, winner, "only one concurrent completion may succeed")
			winner = got.resp
			continue
		}
		require.ErrorIs(t, got.err, business.ErrMFAChallengeRejected)
		rejected++
	}
	require.NotNil(t, winner)
	require.Equal(t, 1, rejected)
	require.NotEmpty(t, winner.AccessToken)
	require.NotEmpty(t, winner.RefreshToken)
	require.Equal(t, userID, winner.User.Uuid)

	identity, err := testService.JWTMinter().VerifyAccess(winner.AccessToken)
	require.NoError(t, err)
	require.True(t, identity.MFASatisfied)
	require.Equal(t, auth.AssuranceLevelAAL2, identity.AssuranceLevel)
	require.Equal(t, []string{auth.AuthenticationMethodFixture, auth.AuthenticationMethodOTP}, identity.AuthenticationMethods)
	require.False(t, identity.AuthenticatedAt.IsZero())
	require.True(t, identity.Assurance().HasRecentMFA(time.Now(), auth.DefaultRecentStepUpMaxAge))
	originalAuthTime := identity.AuthenticatedAt
	originalMFATime := identity.MFAVerifiedAt

	// Assurance survives refresh rotation; otherwise every refreshed login
	// would unexpectedly lose access to MFA-gated operations.
	rotated, err := testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{RefreshToken: winner.RefreshToken})
	require.NoError(t, err)
	identity, err = testService.JWTMinter().VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.True(t, identity.MFASatisfied)
	require.Equal(t, auth.AssuranceLevelAAL2, identity.AssuranceLevel)
	require.Equal(t, []string{auth.AuthenticationMethodFixture, auth.AuthenticationMethodOTP}, identity.AuthenticationMethods)
	require.Equal(t, originalAuthTime.Unix(), identity.AuthenticatedAt.Unix(), "refresh must preserve auth_time")
	require.Equal(t, originalMFATime.Unix(), identity.MFAVerifiedAt.Unix(), "refresh must not renew step-up freshness")

	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		var listErr error
		sessions, listErr = testStore.ListActiveSessions(ctx, userID, 10)
		return listErr
	}))
	require.Len(t, sessions, 1)
	require.Equal(t, map[string]string{"description": "Firefox on Linux"}, sessions[0].DeviceInfo)
	require.True(t, sessions[0].IdleExpiresAt.After(sessions[0].LastActiveAt))
	require.False(t, sessions[0].IdleExpiresAt.After(sessions[0].ExpiresAt))

	_, err = testService.CompleteMFAChallenge(testCtx, primary.MfaToken, codeA)
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected)

	// The management identifier represents the complete device family, not the
	// latest rotated database row. Revoking it must invalidate the live token.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.RevokeSession(ctx, sessions[0].FamilyID, "integration_test")
	}))
	_, err = testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{RefreshToken: rotated.RefreshToken})
	require.Error(t, err)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var listErr error
		sessions, listErr = testStore.ListActiveSessions(ctx, userID, 10)
		return listErr
	}))
	require.Empty(t, sessions)
}

func TestMFALoginLocksTransactionAfterFiveRejectedFactors(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}
	clearData(t)
	userID, secret := registerMFAUser(t, "mfa-lock", "mfa-lock@example.com")
	primary, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "email", ProviderId: "mfa-lock", ProviderEmail: "mfa-lock@example.com",
	})
	require.NoError(t, err)
	require.True(t, primary.MfaRequired)

	for attempt := 1; attempt <= 5; attempt++ {
		_, err = testService.CompleteMFAChallenge(testCtx, primary.MfaToken, "definitely-not-a-factor")
		require.ErrorIs(t, err, business.ErrMFAChallengeRejected)
	}

	// Even the correct factor is rejected after the transaction-specific
	// budget is exhausted. A fresh primary login is required.
	_, err = testService.CompleteMFAChallenge(testCtx, primary.MfaToken, testTOTP(t, secret, time.Now()))
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected)

	var failedAttempts int
	var maxAttempts int
	var lockedUntil *time.Time
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return ctx.Value("tx").(pgx.Tx).QueryRow(ctx, `
			SELECT failed_attempts, max_attempts, locked_until
			FROM mfa_login_transactions
			WHERE user_id = $1`, userID).Scan(&failedAttempts, &maxAttempts, &lockedUntil)
	}))
	require.Equal(t, 5, failedAttempts)
	require.Equal(t, 5, maxAttempts)
	require.NotNil(t, lockedUntil)

	var sessions []*business.Session
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		var listErr error
		sessions, listErr = testStore.ListActiveSessions(ctx, userID, 10)
		return listErr
	}))
	require.Empty(t, sessions)
}

func TestMFALoginTransactionExpiryRejectsBeforeIssuance(t *testing.T) {
	if testStore == nil {
		t.Skip("test infrastructure not available")
	}
	clearData(t)
	userID, _ := registerMFAUser(t, "mfa-expired", "mfa-expired@example.com")

	plaintext := "expired-mfa-login-token-with-enough-entropy-for-test"
	digest := sha256.Sum256([]byte(plaintext))
	tx := &business.MFALoginTransaction{
		ID:        business.NewIDString(),
		TokenHash: hex.EncodeToString(digest[:]),
		UserID:    userID,
		SessionID: business.NewIDString(),
		CreatedAt: time.Now().Add(-10 * time.Minute),
		ExpiresAt: time.Now().Add(-5 * time.Minute),
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		return testStore.CreateMFALoginTransaction(ctx, tx)
	}))

	issued := false
	err := testStore.ConsumeMFALoginTransaction(testCtx, tx.TokenHash, time.Now(), func(context.Context, *business.MFALoginTransaction) error {
		issued = true
		return nil
	})
	require.True(t, errors.Is(err, business.ErrMFAChallengeRejected))
	require.False(t, issued, "expired transaction must reject before session issuance")
}

func TestMFABackupCodesAreOneUseRegeneratedAndNotified(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available")
	}
	clearData(t)
	userID, _ := registerMFAUser(t, "mfa-backup", "mfa-backup@example.com")
	codes, err := testService.GenerateBackupCodes(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, codes, 10)

	login := func() *gen.AuthenticateResponse {
		resp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
			Provider: "email", ProviderId: "mfa-backup", ProviderEmail: "mfa-backup@example.com",
		})
		require.NoError(t, err)
		require.True(t, resp.MfaRequired)
		return resp
	}

	first := login()
	_, err = testService.CompleteMFAChallenge(testCtx, first.MfaToken, codes[0])
	require.NoError(t, err)
	notifications, _, err := testService.ListNotifications(testCtx, userID, 20, "")
	require.NoError(t, err)
	require.NotEmpty(t, notifications)
	require.Equal(t, "Recovery code used", notifications[0].Title)
	require.Equal(t, "security", notifications[0].Type)

	second := login()
	_, err = testService.CompleteMFAChallenge(testCtx, second.MfaToken, codes[0])
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected, "used recovery code must not replay")
	_, err = testService.CompleteMFAChallenge(testCtx, second.MfaToken, codes[1])
	require.NoError(t, err, "a failed factor attempt must not consume the login transaction")

	newCodes, err := testService.GenerateBackupCodes(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, newCodes, 10)
	third := login()
	_, err = testService.CompleteMFAChallenge(testCtx, third.MfaToken, codes[2])
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected, "regeneration must invalidate every old code")
	_, err = testService.CompleteMFAChallenge(testCtx, third.MfaToken, newCodes[0])
	require.NoError(t, err)
}
