package infra_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
)

// ============================================================================
// MFA Device tests
// ============================================================================

func TestCreateAndListMFADevices(t *testing.T) {
	userID := seedUser(t)

	device := &business.MFADevice{
		ID:              business.NewIDString(),
		UserID:          userID,
		DeviceType:      "totp",
		Name:            "My Authenticator",
		SecretEncrypted: "encrypted-secret-data",
	}
	err := testStore.CreateMFADevice(testCtx, device)
	require.NoError(t, err)

	devices, err := testStore.ListMFADevices(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	require.Equal(t, device.ID, devices[0].ID)
	require.Equal(t, "totp", devices[0].DeviceType)
	require.Equal(t, "My Authenticator", devices[0].Name)
	require.Equal(t, "encrypted-secret-data", devices[0].SecretEncrypted)
	require.Nil(t, devices[0].VerifiedAt)
	require.Nil(t, devices[0].LastUsedAt)
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
	require.NoError(t, testStore.CreateMFADevice(testCtx, device))

	now := time.Now()
	device.VerifiedAt = &now
	err := testStore.UpdateMFADevice(testCtx, device)
	require.NoError(t, err)

	got, err := testStore.GetMFADevice(testCtx, device.ID)
	require.NoError(t, err)
	require.NotNil(t, got.VerifiedAt)
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
	require.NoError(t, testStore.CreateMFADevice(testCtx, device))

	err := testStore.DeleteMFADevice(testCtx, device.ID)
	require.NoError(t, err)

	devices, err := testStore.ListMFADevices(testCtx, userID)
	require.NoError(t, err)
	require.Empty(t, devices)

	// Deleting again should return not-found.
	err = testStore.DeleteMFADevice(testCtx, device.ID)
	require.Error(t, err)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
}

func TestHasVerifiedMFA(t *testing.T) {
	userID := seedUser(t)

	// No devices yet.
	has, err := testStore.HasVerifiedMFA(testCtx, userID)
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
	require.NoError(t, testStore.CreateMFADevice(testCtx, device))

	has, err = testStore.HasVerifiedMFA(testCtx, userID)
	require.NoError(t, err)
	require.False(t, has, "unverified device should not count")

	// Verify the device.
	now := time.Now()
	device.VerifiedAt = &now
	require.NoError(t, testStore.UpdateMFADevice(testCtx, device))

	has, err = testStore.HasVerifiedMFA(testCtx, userID)
	require.NoError(t, err)
	require.True(t, has, "verified device should be detected")
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
	err := testStore.CreateBackupCodes(testCtx, codes)
	require.NoError(t, err)

	unused, err := testStore.GetUnusedBackupCodes(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, unused, 3)
}

func TestUseBackupCode(t *testing.T) {
	userID := seedUser(t)

	codes := []*business.MFABackupCode{
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-use-1"},
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-use-2"},
	}
	require.NoError(t, testStore.CreateBackupCodes(testCtx, codes))

	// Use the first code.
	err := testStore.UseBackupCode(testCtx, codes[0].ID)
	require.NoError(t, err)

	unused, err := testStore.GetUnusedBackupCodes(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, unused, 1)
	require.Equal(t, codes[1].ID, unused[0].ID)

	// Using the same code again should fail.
	err = testStore.UseBackupCode(testCtx, codes[0].ID)
	require.Error(t, err)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
}

func TestDeleteBackupCodes(t *testing.T) {
	userID := seedUser(t)

	codes := []*business.MFABackupCode{
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-del-1"},
		{ID: business.NewIDString(), UserID: userID, CodeHash: "hash-del-2"},
	}
	require.NoError(t, testStore.CreateBackupCodes(testCtx, codes))

	err := testStore.DeleteBackupCodes(testCtx, userID)
	require.NoError(t, err)

	unused, err := testStore.GetUnusedBackupCodes(testCtx, userID)
	require.NoError(t, err)
	require.Empty(t, unused)
}
