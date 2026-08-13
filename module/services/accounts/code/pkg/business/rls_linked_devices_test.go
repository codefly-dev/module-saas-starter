package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_LinkedDevices_CrossTenantBlocked — proves migration 86 RLS on
// linked_devices and device_claim_codes (direct org_id policies).
// Org A's session sees only its own devices/claim codes; B's rows are
// physically invisible; un-wrapped reads fail closed to zero rows; the
// audited control-plane scope (entitlement-check lookup path) still resolves
// keys across tenants.
func TestRLS_LinkedDevices_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	registerOrg := func(tag string) (string, string) {
		resp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
			PrimaryEmail: tag + "@rls-devices.test",
			Identity: &gen.UserIdentity{
				Provider: "email", ProviderId: tag + "-rls-devices", ProviderEmail: tag + "@rls-devices.test",
			},
		})
		require.NoError(t, err)
		resolved, err := testService.ResolveIdentity(ctx, &gen.ResolveIdentityRequest{
			Provider: "email", ProviderId: tag + "-rls-devices",
		})
		require.NoError(t, err)
		return resp.User.Uuid, resolved.OrgId
	}

	aliceID, orgA := registerOrg("alice")
	_, orgB := registerOrg("bob")

	now := time.Now().UTC()
	deviceA := &business.Device{
		ID: business.NewIDString(), OrgID: orgA,
		DevicePublicKey: "rls-device-key-org-a", Name: "a-device",
		CreatedBy: aliceID, CreatedAt: now,
	}
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		return testStore.CreateDevice(ctx, deviceA)
	}))
	require.NoError(t, testStore.WithOrgTx(ctx, orgB, func(ctx context.Context) error {
		return testStore.CreateDevice(ctx, &business.Device{
			ID: business.NewIDString(), OrgID: orgB,
			DevicePublicKey: "rls-device-key-org-b", Name: "b-device", CreatedAt: now,
		})
	}))

	codeHashA := business.HashDeviceClaimCode("RLSCODEA")
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		return testStore.CreateDeviceClaimCode(ctx, &business.DeviceClaimCode{
			ID: business.NewIDString(), OrgID: orgA, CodeHash: codeHashA,
			Status: "pending", ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now,
		})
	}))

	// As A: only A's device.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		devices, err := testStore.ListDevices(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, devices, 1)
		require.Equal(t, "a-device", devices[0].Name)
		return nil
	}))

	// Cross-tenant probe: from inside A's tx, B's devices are invisible.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListDevices(ctx, orgB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's devices from A's tx")

		byKey, err := testStore.GetDeviceByPublicKey(ctx, "rls-device-key-org-b")
		require.NoError(t, err)
		require.Nil(t, byKey, "RLS must hide B's device key from A's tx")
		return nil
	}))

	// Cross-tenant probe on claim codes: B cannot see A's code hash.
	require.NoError(t, testStore.WithOrgTx(ctx, orgB, func(ctx context.Context) error {
		stolen, err := testStore.GetDeviceClaimCodeByHash(ctx, codeHashA)
		require.NoError(t, err)
		require.Nil(t, stolen, "RLS must hide A's claim code from B's tx")
		return nil
	}))

	// Un-wrapped reads: zero rows (fail-closed).
	noWrap, err := testStore.ListDevices(context.Background(), orgA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0, "un-wrapped ListDevices must return ZERO rows (RLS fail-closed)")

	noWrapDevice, err := testStore.GetDeviceByPublicKey(context.Background(), "rls-device-key-org-a")
	require.NoError(t, err)
	require.Nil(t, noWrapDevice, "un-wrapped GetDeviceByPublicKey must fail closed")

	noWrapCode, err := testStore.GetDeviceClaimCodeByHash(context.Background(), codeHashA)
	require.NoError(t, err)
	require.Nil(t, noWrapCode, "un-wrapped GetDeviceClaimCodeByHash must fail closed")

	// The audited control-plane scope (entitlement check + claim redemption
	// lookup path) resolves keys across tenants.
	require.NoError(t, testStore.As(business.System()).Within(ctx, func(ctx context.Context) error {
		device, err := testStore.GetDeviceByPublicKey(ctx, "rls-device-key-org-b")
		require.NoError(t, err)
		require.NotNil(t, device, "control-plane scope must resolve any device key")
		require.Equal(t, orgB, device.OrgID)

		code, err := testStore.GetDeviceClaimCodeByHash(ctx, codeHashA)
		require.NoError(t, err)
		require.NotNil(t, code, "control-plane scope must resolve any claim code")
		require.Equal(t, orgA, code.OrgID)
		return nil
	}))
}
