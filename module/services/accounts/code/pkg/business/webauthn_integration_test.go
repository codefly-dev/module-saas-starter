package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type deterministicWebAuthnEngine struct {
	credentialID []byte
}

func (e *deterministicWebAuthnEngine) BeginRegistration(_ context.Context, _ business.WebAuthnUser) ([]byte, []byte, time.Time, error) {
	return []byte(`{"challenge":"registration"}`), []byte(`{"state":"registration"}`), time.Now().Add(2 * time.Minute), nil
}

func (e *deterministicWebAuthnEngine) FinishRegistration(_ context.Context, _ business.WebAuthnUser, sessionJSON, responseJSON []byte) (*business.WebAuthnCredentialResult, error) {
	if string(sessionJSON) != `{"state":"registration"}` || string(responseJSON) != `{"credential":"registration"}` {
		return nil, errors.New("invalid registration proof")
	}
	return &business.WebAuthnCredentialResult{
		ID:             append([]byte(nil), e.credentialID...),
		CredentialJSON: []byte(`{"counter":0,"credential":"encrypted-at-rest"}`),
	}, nil
}

func (e *deterministicWebAuthnEngine) BeginLogin(_ context.Context, user business.WebAuthnUser) ([]byte, []byte, time.Time, error) {
	if len(user.CredentialJSON) == 0 {
		return nil, nil, time.Time{}, errors.New("no passkey")
	}
	return []byte(`{"challenge":"login"}`), []byte(`{"state":"login"}`), time.Now().Add(2 * time.Minute), nil
}

func (e *deterministicWebAuthnEngine) FinishLogin(_ context.Context, user business.WebAuthnUser, sessionJSON, responseJSON []byte) (*business.WebAuthnCredentialResult, error) {
	if len(user.CredentialJSON) == 0 || string(sessionJSON) != `{"state":"login"}` || string(responseJSON) != `{"credential":"assertion"}` {
		return nil, errors.New("invalid assertion")
	}
	return &business.WebAuthnCredentialResult{
		ID:             append([]byte(nil), e.credentialID...),
		CredentialJSON: []byte(`{"counter":1,"credential":"updated-after-login"}`),
	}, nil
}

func restoreProductionWebAuthnEngine(t *testing.T) {
	t.Helper()
	engine, err := infra.NewWebAuthnEngine("localhost", "SaaS Starter Test", []string{"http://localhost:21931"})
	require.NoError(t, err)
	testService.SetWebAuthnEngine(engine)
}

func registerPasskeyUser(t *testing.T, subject, email string, engine business.WebAuthnEngine) string {
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
	testService.SetWebAuthnEngine(engine)
	ceremonyToken, options, err := testService.BeginWebAuthnRegistration(testCtx, registered.User.Uuid)
	require.NoError(t, err)
	require.NotEmpty(t, ceremonyToken)
	require.JSONEq(t, `{"challenge":"registration"}`, options)
	device, err := testService.FinishWebAuthnRegistration(
		testCtx,
		registered.User.Uuid,
		ceremonyToken,
		`{"credential":"registration"}`,
		"Touch ID",
	)
	require.NoError(t, err)
	require.Equal(t, "webauthn", device.DeviceType)
	require.Equal(t, "Touch ID", device.Name)
	require.NotNil(t, device.VerifiedAt)
	return registered.User.Uuid
}

func TestWebAuthnRegistrationAndLoginAreOneUseAndAAL2(t *testing.T) {
	clearData(t)
	engine := &deterministicWebAuthnEngine{credentialID: []byte("credential-a")}
	defer restoreProductionWebAuthnEngine(t)

	userID := registerPasskeyUser(t, "passkey-user", "passkey@example.com", engine)
	devices, err := testService.ListMFADevices(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	require.Equal(t, "webauthn", devices[0].DeviceType)
	require.Empty(t, devices[0].SecretEncrypted)
	var records []*business.StoredWebAuthnCredential
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		var err error
		records, err = testStore.ListWebAuthnCredentials(ctx, userID, false)
		return err
	}))
	require.Len(t, records, 1)
	require.NotContains(t, records[0].CredentialEncrypted, "encrypted-at-rest")

	primary, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "email", ProviderId: "passkey-user", ProviderEmail: "passkey@example.com",
	})
	require.NoError(t, err)
	require.True(t, primary.MfaRequired)
	require.Empty(t, primary.AccessToken)

	ceremonyToken, options, err := testService.BeginWebAuthnMFAChallenge(testCtx, primary.MfaToken)
	require.NoError(t, err)
	require.JSONEq(t, `{"challenge":"login"}`, options)

	completed, err := testService.CompleteWebAuthnMFAChallenge(
		testCtx,
		primary.MfaToken,
		ceremonyToken,
		`{"credential":"assertion"}`,
	)
	require.NoError(t, err)
	require.NotEmpty(t, completed.AccessToken)
	require.NotEmpty(t, completed.RefreshToken)

	identity, err := testService.JWTMinter().VerifyAccess(completed.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AssuranceLevelAAL2, identity.AssuranceLevel)
	require.Equal(t, []string{auth.AuthenticationMethodFixture, auth.AuthenticationMethodWebAuthn}, identity.AuthenticationMethods)
	require.True(t, identity.Assurance().HasRecentMFA(time.Now(), auth.DefaultRecentStepUpMaxAge))

	_, err = testService.CompleteWebAuthnMFAChallenge(
		testCtx,
		primary.MfaToken,
		ceremonyToken,
		`{"credential":"assertion"}`,
	)
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected)
}

func TestWebAuthnCeremonyCannotCrossLoginTransactions(t *testing.T) {
	clearData(t)
	engine := &deterministicWebAuthnEngine{credentialID: []byte("credential-b")}
	defer restoreProductionWebAuthnEngine(t)
	registerPasskeyUser(t, "passkey-cross", "passkey-cross@example.com", engine)

	first, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "email", ProviderId: "passkey-cross", ProviderEmail: "passkey-cross@example.com",
	})
	require.NoError(t, err)
	second, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "email", ProviderId: "passkey-cross", ProviderEmail: "passkey-cross@example.com",
	})
	require.NoError(t, err)
	firstCeremony, _, err := testService.BeginWebAuthnMFAChallenge(testCtx, first.MfaToken)
	require.NoError(t, err)
	secondCeremony, _, err := testService.BeginWebAuthnMFAChallenge(testCtx, second.MfaToken)
	require.NoError(t, err)

	_, err = testService.CompleteWebAuthnMFAChallenge(testCtx, second.MfaToken, firstCeremony, `{"credential":"assertion"}`)
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected)
	_, err = testService.CompleteWebAuthnMFAChallenge(testCtx, second.MfaToken, secondCeremony, `{"credential":"assertion"}`)
	require.NoError(t, err, "a cross-transaction rejection must not consume the valid ceremony")
}

func TestWebAuthnCredentialIDIsGloballyUnique(t *testing.T) {
	clearData(t)
	engine := &deterministicWebAuthnEngine{credentialID: []byte("shared-credential")}
	defer restoreProductionWebAuthnEngine(t)
	registerPasskeyUser(t, "passkey-owner", "passkey-owner@example.com", engine)

	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "passkey-other@example.com",
		Identity:     &gen.UserIdentity{Provider: "email", ProviderId: "passkey-other", ProviderEmail: "passkey-other@example.com", EmailVerified: true},
	})
	require.NoError(t, err)
	token, _, err := testService.BeginWebAuthnRegistration(testCtx, registered.User.Uuid)
	require.NoError(t, err)
	_, err = testService.FinishWebAuthnRegistration(testCtx, registered.User.Uuid, token, `{"credential":"registration"}`, "Duplicate")
	require.ErrorIs(t, err, business.ErrWebAuthnCeremonyRejected)
}

func TestWebAuthnRegistrationAndLoginCeremoniesExpire(t *testing.T) {
	clearData(t)
	engine := &deterministicWebAuthnEngine{credentialID: []byte("credential-expiry")}
	defer restoreProductionWebAuthnEngine(t)

	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "passkey-expiry@example.com",
		Identity:     &gen.UserIdentity{Provider: "email", ProviderId: "passkey-expiry", ProviderEmail: "passkey-expiry@example.com", EmailVerified: true},
	})
	require.NoError(t, err)
	testService.SetWebAuthnEngine(engine)
	registrationToken, _, err := testService.BeginWebAuthnRegistration(testCtx, registered.User.Uuid)
	require.NoError(t, err)
	require.NoError(t, expireWebAuthnCeremonies(registered.User.Uuid, "registration"))
	_, err = testService.FinishWebAuthnRegistration(testCtx, registered.User.Uuid, registrationToken, `{"credential":"registration"}`, "Expired")
	require.ErrorIs(t, err, business.ErrWebAuthnCeremonyRejected)

	userID := registerPasskeyUser(t, "passkey-expiry-enrolled", "passkey-expiry-enrolled@example.com", engine)
	primary, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "email", ProviderId: "passkey-expiry-enrolled", ProviderEmail: "passkey-expiry-enrolled@example.com",
	})
	require.NoError(t, err)
	loginToken, _, err := testService.BeginWebAuthnMFAChallenge(testCtx, primary.MfaToken)
	require.NoError(t, err)
	require.NoError(t, expireWebAuthnCeremonies(userID, "login"))
	_, err = testService.CompleteWebAuthnMFAChallenge(testCtx, primary.MfaToken, loginToken, `{"credential":"assertion"}`)
	require.ErrorIs(t, err, business.ErrMFAChallengeRejected)
}

func expireWebAuthnCeremonies(userID, ceremonyType string) error {
	return testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		_, err := tx.Exec(ctx, `
			UPDATE webauthn_ceremonies
			SET expires_at = $3
			WHERE user_id = $1 AND ceremony_type = $2 AND consumed_at IS NULL`,
			userID, ceremonyType, time.Now().Add(-time.Minute),
		)
		return err
	})
}
