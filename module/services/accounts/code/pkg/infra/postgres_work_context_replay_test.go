package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// =====================================================================
// work_context_replay integration tests (issue #420)
// =====================================================================
//
// Real Postgres only. The replay store is RLS-protected (org-scoped); consume
// self-scopes via WithOrgTx, the cross-tenant purge runs under the control
// plane, exactly as RunRetention drives it.

func TestWorkContextReplay_FirstConsumeWinsReplayFailsClosed(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	contextID := "ctx-" + business.NewIDString()
	expiresAt := time.Now().Add(time.Hour)

	require.NoError(t, testStore.ConsumeSingleUseWorkContext(testCtx, orgID, contextID, expiresAt))

	err := testStore.ConsumeSingleUseWorkContext(testCtx, orgID, contextID, expiresAt)
	require.ErrorIs(t, err, business.ErrWorkContextAlreadyConsumed,
		"a single-use context must be redeemable exactly once")

	require.NoError(t,
		testStore.ConsumeSingleUseWorkContext(testCtx, orgID, "ctx-"+business.NewIDString(), expiresAt),
		"a distinct context id is unaffected")
}

func TestWorkContextReplay_ContextIDsAreOrgIsolated(t *testing.T) {
	ownerA := seedUser(t)
	orgA := seedOrg(t, ownerA)
	ownerB := seedUser(t)
	orgB := seedOrg(t, ownerB)
	contextID := "ctx-" + business.NewIDString()
	expiresAt := time.Now().Add(time.Hour)

	require.NoError(t, testStore.ConsumeSingleUseWorkContext(testCtx, orgA, contextID, expiresAt))
	require.NoError(t, testStore.ConsumeSingleUseWorkContext(testCtx, orgB, contextID, expiresAt),
		"the replay marker is scoped to its own org")
}

func TestWorkContextReplay_PurgeReclaimsExpiredOnly(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	expired := "ctx-" + business.NewIDString()
	live := "ctx-" + business.NewIDString()

	require.NoError(t, testStore.ConsumeSingleUseWorkContext(testCtx, orgID, expired, time.Now().Add(-time.Hour)))
	require.NoError(t, testStore.ConsumeSingleUseWorkContext(testCtx, orgID, live, time.Now().Add(time.Hour)))

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		count, err := testStore.PurgeExpiredWorkContextReplays(ctx, time.Now())
		require.NoError(t, err)
		require.GreaterOrEqual(t, count, int64(1))
		return nil
	}))

	require.NoError(t,
		testStore.ConsumeSingleUseWorkContext(testCtx, orgID, expired, time.Now().Add(time.Hour)),
		"an expired marker is reclaimed and the id becomes claimable again")
	require.ErrorIs(t,
		testStore.ConsumeSingleUseWorkContext(testCtx, orgID, live, time.Now().Add(time.Hour)),
		business.ErrWorkContextAlreadyConsumed,
		"a marker for a still-live capability is retained")
}
