package infra_test

import (
	"context"
	"errors"
	"sync"
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

// The exactly-once guarantee has to hold when consumers race, not just in
// sequence: the insert-once path is the whole security property. Fire many
// concurrent claims of one context id and require exactly one to win.
func TestWorkContextReplay_ConcurrentConsumeAdmitsExactlyOne(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	contextID := "ctx-" + business.NewIDString()
	expiresAt := time.Now().Add(time.Hour)

	const racers = 8
	var wg sync.WaitGroup
	results := make(chan error, racers)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			results <- testStore.ConsumeSingleUseWorkContext(testCtx, orgID, contextID, expiresAt)
		}()
	}
	wg.Wait()
	close(results)

	var wins, replays int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, business.ErrWorkContextAlreadyConsumed):
			replays++
		default:
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	require.Equal(t, 1, wins, "exactly one concurrent claim may win")
	require.Equal(t, racers-1, replays, "every other racer must fail closed as a replay")
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
