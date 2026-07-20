package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

type migrationCipher struct{}

func (migrationCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	return "cfs1:vault-transit:" + purpose + ":" + plaintext, nil
}

func (migrationCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return envelope, nil
}

// MFA tables (mfa_devices, mfa_backup_codes) are RLS-protected by
// user_id (Phase 2G). Each test wraps direct Store calls in
// WithUserTx for the seeded test user — same pattern as the webhook
// integration tests use WithOrgTx for org-scoped tables.

func TestCreateAndListMFADevices(t *testing.T) {
	userID := seedUser(t)

	device := &business.MFADevice{
		ID:              business.NewIDString(),
		UserID:          userID,
		DeviceType:      "totp",
		Name:            "My Authenticator",
		SecretEncrypted: "encrypted-secret-data",
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		if err := testStore.CreateMFADevice(ctx, device); err != nil {
			return err
		}
		devices, err := testStore.ListMFADevices(ctx, userID)
		require.NoError(t, err)
		require.Len(t, devices, 1)
		require.Equal(t, device.ID, devices[0].ID)
		require.Equal(t, "totp", devices[0].DeviceType)
		require.Equal(t, "My Authenticator", devices[0].Name)
		require.Equal(t, "encrypted-secret-data", devices[0].SecretEncrypted)
		require.Nil(t, devices[0].VerifiedAt)
		require.Nil(t, devices[0].LastUsedAt)
		return nil
	}))
}

func TestUpdateMFADevice_SetVerified(t *testing.T) {
	userID := seedUser(t)

	device := &business.MFADevice{
		ID:              business.NewIDString(),
		UserID:          userID,
		DeviceType:      "totp",
		Name:            "Verify Me",
		SecretEncrypted: "enc",
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateMFADevice(ctx, device))

		now := time.Now()
		device.VerifiedAt = &now
		require.NoError(t, testStore.UpdateMFADevice(ctx, device))

		got, err := testStore.GetMFADevice(ctx, device.ID)
		require.NoError(t, err)
		require.NotNil(t, got.VerifiedAt)
		return nil
	}))
}

func TestDeleteMFADevice(t *testing.T) {
	userID := seedUser(t)

	device := &business.MFADevice{
		ID:              business.NewIDString(),
		UserID:          userID,
		DeviceType:      "totp",
		Name:            "Delete Me",
		SecretEncrypted: "enc",
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateMFADevice(ctx, device))
		require.NoError(t, testStore.DeleteMFADevice(ctx, device.ID))
		devices, err := testStore.ListMFADevices(ctx, userID)
		require.NoError(t, err)
		require.Empty(t, devices)

		// Deleting again should return not-found.
		err = testStore.DeleteMFADevice(ctx, device.ID)
		require.Error(t, err)
		var storeErr *business.StoreError
		require.ErrorAs(t, err, &storeErr)
		require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
		return nil
	}))
}

func TestHasVerifiedMFA(t *testing.T) {
	userID := seedUser(t)

	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		// No devices yet.
		has, err := testStore.HasVerifiedMFA(ctx, userID)
		require.NoError(t, err)
		require.False(t, has)

		// Create an unverified device.
		device := &business.MFADevice{
			ID:              business.NewIDString(),
			UserID:          userID,
			DeviceType:      "totp",
			Name:            "Unverified",
			SecretEncrypted: "enc",
		}
		require.NoError(t, testStore.CreateMFADevice(ctx, device))

		has, err = testStore.HasVerifiedMFA(ctx, userID)
		require.NoError(t, err)
		require.False(t, has, "unverified device should not count")

		// Verify the device.
		now := time.Now()
		device.VerifiedAt = &now
		require.NoError(t, testStore.UpdateMFADevice(ctx, device))

		has, err = testStore.HasVerifiedMFA(ctx, userID)
		require.NoError(t, err)
		require.True(t, has, "verified device should be detected")
		return nil
	}))
}

// ============================================================================
// MFA Backup Code tests
// ============================================================================

func TestCreateAndGetUnusedBackupCodes(t *testing.T) {
	userID := seedUser(t)

	codes := []*business.MFABackupCode{
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-aaa"},
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-bbb"},
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-ccc"},
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateBackupCodes(ctx, codes))
		unused, err := testStore.GetUnusedBackupCodes(ctx, userID)
		require.NoError(t, err)
		require.Len(t, unused, 3)
		return nil
	}))
}

func TestUseBackupCode(t *testing.T) {
	userID := seedUser(t)

	codes := []*business.MFABackupCode{
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-use-1"},
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-use-2"},
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateBackupCodes(ctx, codes))
		require.NoError(t, testStore.UseBackupCode(ctx, codes[0].ID))

		unused, err := testStore.GetUnusedBackupCodes(ctx, userID)
		require.NoError(t, err)
		require.Len(t, unused, 1)
		require.Equal(t, codes[1].ID, unused[0].ID)

		// Using the same code again should fail.
		err = testStore.UseBackupCode(ctx, codes[0].ID)
		require.Error(t, err)
		var storeErr *business.StoreError
		require.ErrorAs(t, err, &storeErr)
		require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
		return nil
	}))
}

func TestDeleteBackupCodes(t *testing.T) {
	userID := seedUser(t)

	codes := []*business.MFABackupCode{
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-del-1"},
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-del-2"},
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateBackupCodes(ctx, codes))
		require.NoError(t, testStore.DeleteBackupCodes(ctx, userID))
		unused, err := testStore.GetUnusedBackupCodes(ctx, userID)
		require.NoError(t, err)
		require.Empty(t, unused)
		return nil
	}))
}

func TestMigrateLegacyMFASecrets(t *testing.T) {
	// The migration scans globally by design; isolate it from rows created by
	// other integration cases in this package.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		_, err := tx.Exec(ctx, `DELETE FROM mfa_devices`)
		return err
	}))

	userID := seedUser(t)
	legacy := "JBSWY3DPEHPK3PXP"
	device := &business.MFADevice{
		ID: business.NewIDString(), UserID: userID, DeviceType: "totp",
		Name: "Legacy", SecretEncrypted: legacy,
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		return testStore.CreateMFADevice(ctx, device)
	}))

	migrated, err := testStore.MigrateLegacyMFASecrets(testCtx, migrationCipher{})
	require.NoError(t, err)
	require.Equal(t, 1, migrated)
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		got, err := testStore.GetMFADevice(ctx, device.ID)
		require.NoError(t, err)
		require.NotEqual(t, legacy, got.SecretEncrypted)
		require.Contains(t, got.SecretEncrypted, "cfs1:vault-transit:mfa-totp:")
		return nil
	}))

	// Restart safety: already-enveloped rows are skipped.
	migrated, err = testStore.MigrateLegacyMFASecrets(testCtx, migrationCipher{})
	require.NoError(t, err)
	require.Zero(t, migrated)
}
