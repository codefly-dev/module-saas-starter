package business_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// TestGetOrgEntitlements_SingleTransaction — pins the N+1 fix.
// GetOrgEntitlements brackets plan + overrides + per-feature usage
// in ONE WithOrgTx; resolveUsage no longer opens its own. Pre-fix
// behavior was 1 (outer) + N (per-feature) = many txs for a plan
// with several features. This test asserts the call is constant in
// the number of features.
//
// Async audit emits can fire between snapshots (RegisterUser /
// CreateOrganization in the seeded mustUserAndOrg both emit and
// the AsyncAuditEmitter writes via WithOrgTx in a goroutine).
// We sleep briefly before the measured call to let those drain,
// then assert the FRESH GetOrgEntitlements bumps by exactly 1.
func TestGetOrgEntitlements_SingleTransaction(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgID := mustUserAndOrg(t, ctx,
		"alice-ent@rls-test.com", "alice-ent-rls", "Acme Ent A")

	// First call also serves as warm-up + drain trigger.
	_, err := testService.GetOrgEntitlements(ctx, orgID)
	require.NoError(t, err)
	// Let any pending async audit emits drain.
	time.Sleep(150 * time.Millisecond)

	// Now measure: a second call should bump WithOrgTx by exactly 1
	// (no async emits queued, no per-feature transactions).
	before := infra.OrgTxCount()
	_, err = testService.GetOrgEntitlements(ctx, orgID)
	require.NoError(t, err)
	delta := infra.OrgTxCount() - before

	require.Equal(t, int64(1), delta,
		"GetOrgEntitlements must run in exactly 1 WithOrgTx (got %d). "+
			"If this test fails, resolveUsage probably opened its own tx again — see entitlements.go", delta)
}
