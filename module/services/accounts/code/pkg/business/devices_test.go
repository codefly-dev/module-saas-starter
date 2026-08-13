package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Linked-device claim + entitlement-check tests.
//
// The seeded default plan (fixture 'free') carries neither paired_devices nor
// the checked feature keys, so entitlements resolve to 0 (disabled) unless a
// per-org override grants them — which is exactly the production fail-closed
// behavior. Tests grant capacity through entitlement_overrides, exercising
// the same override-aware resolution path production uses. ("relay_access" is
// the lazybox product's entitlement key; the check API takes the key from the
// request, so the test key doubles as contract documentation.)

const testEntitlementKey = "relay_access"

func registerDeviceOrg(t *testing.T, tag string) (userID, orgID string) {
	t.Helper()
	resp, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: tag + "@devices.test",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: tag + "-devices", ProviderEmail: tag + "@devices.test",
		},
	})
	require.NoError(t, err)
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider: "email", ProviderId: tag + "-devices",
	})
	require.NoError(t, err)
	return resp.User.Uuid, resolved.OrgId
}

func overrideEntitlement(t *testing.T, orgID, feature string, limit int64) {
	t.Helper()
	value := limit
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateEntitlementOverride(ctx, &business.EntitlementOverride{
			ID: business.NewIDString(), OrgID: orgID, Feature: feature,
			LimitValue: &value, Reason: "device test grant",
		})
	}))
}

func TestDeviceClaimRoundTrip(t *testing.T) {
	clearData(t)
	aliceID, orgID := registerDeviceOrg(t, "claim-roundtrip")
	overrideEntitlement(t, orgID, business.EntitlementPairedDevices, 1)

	// Mint a claim code.
	created, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Len(t, created.Code, 8)
	require.True(t, created.ExpiresAt.AsTime().After(time.Now()))

	// Redeem it from the device (no session).
	claimed, err := testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code:            created.Code,
		DevicePublicKey: "ed25519-device-key-roundtrip",
		Name:            "living-room",
	})
	require.NoError(t, err)
	require.Equal(t, orgID, claimed.Device.OrgId)
	require.Equal(t, "ed25519-device-key-roundtrip", claimed.Device.DevicePublicKey)
	require.Equal(t, "living-room", claimed.Device.Name)
	require.Nil(t, claimed.Device.RevokedAt)

	// The device appears in the org's list.
	list, err := testService.ListDevices(testCtx, &gen.ListDevicesRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Len(t, list.Devices, 1)
	require.Equal(t, claimed.Device.Id, list.Devices[0].Id)

	// Codes are single-use.
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code:            created.Code,
		DevicePublicKey: "ed25519-device-key-second",
	})
	require.ErrorIs(t, err, business.ErrDeviceClaimCodeInvalid)

	// Garbage codes are invalid, not errors of another shape.
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code:            "NOTACODE",
		DevicePublicKey: "ed25519-device-key-third",
	})
	require.ErrorIs(t, err, business.ErrDeviceClaimCodeInvalid)
}

func TestDeviceClaimOverLimitRefused(t *testing.T) {
	clearData(t)
	aliceID, orgID := registerDeviceOrg(t, "claim-limit")
	overrideEntitlement(t, orgID, business.EntitlementPairedDevices, 1)

	first, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: first.Code, DevicePublicKey: "limit-device-key-1",
	})
	require.NoError(t, err)

	// paired_devices = 1 → the second claim is refused with the quota error.
	second, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: second.Code, DevicePublicKey: "limit-device-key-2",
	})
	require.ErrorIs(t, err, business.ErrEntitlementQuotaExceeded)
	var quotaErr *business.EntitlementQuotaError
	require.ErrorAs(t, err, &quotaErr)
	require.Equal(t, business.EntitlementPairedDevices, quotaErr.Feature)
	require.EqualValues(t, 1, quotaErr.Limit)

	// Revoking the first device frees the slot (count is non-revoked devices).
	list, err := testService.ListDevices(testCtx, &gen.ListDevicesRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Len(t, list.Devices, 1)
	_, err = testService.RevokeDevice(testCtx, aliceID, &gen.RevokeDeviceRequest{
		OrgId: orgID, DeviceId: list.Devices[0].Id,
	})
	require.NoError(t, err)

	third, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: third.Code, DevicePublicKey: "limit-device-key-3",
	})
	require.NoError(t, err)
}

func TestDeviceClaimDuplicateKeyRefused(t *testing.T) {
	clearData(t)
	aliceID, orgID := registerDeviceOrg(t, "claim-dup")
	overrideEntitlement(t, orgID, business.EntitlementPairedDevices, 5)

	first, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: first.Code, DevicePublicKey: "dup-device-key",
	})
	require.NoError(t, err)

	second, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: second.Code, DevicePublicKey: "dup-device-key",
	})
	require.ErrorIs(t, err, business.ErrDeviceAlreadyClaimed)
}

func TestCheckDeviceEntitlement_UnknownKey(t *testing.T) {
	clearData(t)

	resp, err := testService.CheckDeviceEntitlement(testCtx, &gen.CheckDeviceEntitlementRequest{
		DevicePublicKey: "never-claimed-key",
		EntitlementKey:  testEntitlementKey,
	})
	require.NoError(t, err, "unknown keys are a decision, not an error")
	require.False(t, resp.Active)
	require.Equal(t, business.DeviceEntitlementReasonUnknownDevice, resp.Reason)
	require.Empty(t, resp.Plan)
	require.NotNil(t, resp.CheckedAt)
}

func TestCheckDeviceEntitlement_EntitledAndInactive(t *testing.T) {
	clearData(t)
	aliceID, orgID := registerDeviceOrg(t, "check-entitled")
	overrideEntitlement(t, orgID, business.EntitlementPairedDevices, 2)

	code, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: code.Code, DevicePublicKey: "check-device-key",
	})
	require.NoError(t, err)

	// No entitlement for the requested key → subscription_inactive.
	resp, err := testService.CheckDeviceEntitlement(testCtx, &gen.CheckDeviceEntitlementRequest{
		DevicePublicKey: "check-device-key",
		EntitlementKey:  testEntitlementKey,
	})
	require.NoError(t, err)
	require.False(t, resp.Active)
	require.Equal(t, business.DeviceEntitlementReasonSubscriptionInactive, resp.Reason)
	require.NotEmpty(t, resp.Plan, "plan resolves to the org's effective plan")

	// Grant the entitlement → active flips true, same plan resolution.
	overrideEntitlement(t, orgID, testEntitlementKey, 1)
	resp, err = testService.CheckDeviceEntitlement(testCtx, &gen.CheckDeviceEntitlementRequest{
		DevicePublicKey: "check-device-key",
		EntitlementKey:  testEntitlementKey,
	})
	require.NoError(t, err)
	require.True(t, resp.Active)
	require.Empty(t, resp.Reason)
	require.NotEmpty(t, resp.Plan)
}

func TestCheckDeviceEntitlement_RevokedDevice(t *testing.T) {
	clearData(t)
	aliceID, orgID := registerDeviceOrg(t, "check-revoked")
	overrideEntitlement(t, orgID, business.EntitlementPairedDevices, 2)
	overrideEntitlement(t, orgID, testEntitlementKey, 1)

	code, err := testService.CreateDeviceClaimCode(testCtx, aliceID, &gen.CreateDeviceClaimCodeRequest{OrgId: orgID})
	require.NoError(t, err)
	claimed, err := testService.ClaimDevice(testCtx, &gen.ClaimDeviceRequest{
		Code: code.Code, DevicePublicKey: "revoked-device-key",
	})
	require.NoError(t, err)

	before, err := testService.CheckDeviceEntitlement(testCtx, &gen.CheckDeviceEntitlementRequest{
		DevicePublicKey: "revoked-device-key",
		EntitlementKey:  testEntitlementKey,
	})
	require.NoError(t, err)
	require.True(t, before.Active)

	_, err = testService.RevokeDevice(testCtx, aliceID, &gen.RevokeDeviceRequest{
		OrgId: orgID, DeviceId: claimed.Device.Id,
	})
	require.NoError(t, err)

	// Revocation flips the decision immediately.
	after, err := testService.CheckDeviceEntitlement(testCtx, &gen.CheckDeviceEntitlementRequest{
		DevicePublicKey: "revoked-device-key",
		EntitlementKey:  testEntitlementKey,
	})
	require.NoError(t, err)
	require.False(t, after.Active)
	require.Equal(t, business.DeviceEntitlementReasonDeviceRevoked, after.Reason)

	// Revoking an unknown or already-revoked device surfaces not-found.
	_, err = testService.RevokeDevice(testCtx, aliceID, &gen.RevokeDeviceRequest{
		OrgId: orgID, DeviceId: claimed.Device.Id,
	})
	require.ErrorIs(t, err, business.ErrDeviceNotFound)
}
