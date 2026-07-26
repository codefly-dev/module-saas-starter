package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// TestWithOrgTx_RejectsEmptyOrgID — fail loud rather than silently
// matching an "empty" tenant under RLS. A bug elsewhere that
// forgets to thread orgID through must surface here, not pass and
// then return zero rows in production.
//
// Runs without a DB: the guard fires before any pool operation, so
// a nil-pool store is enough to prove the contract.
func TestWithOrgTx_RejectsEmptyOrgID(t *testing.T) {
	s := &infra.PostgresStore{}
	err := s.WithOrgTx(context.Background(), "", func(_ context.Context) error {
		return nil
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "orgID is required")
}

// TestControlPlaneCounters_TracksCallSites — smoke for the Phase 4
// audit trail. Each WithControlPlane call from a unique source position
// gets its own counter; multiple invocations from the same line
// increment that one counter. The test asserts both shapes.
//
// Runs against the real DB so we exercise the WithControlPlane body too.
func TestControlPlaneCounters_TracksCallSites(t *testing.T) {
	before := infra.ControlPlaneCounters()
	beforeSum := int64(0)
	for _, v := range before {
		beforeSum += v
	}

	// Two calls from the same source line — should bump ONE site
	// by 2.
	for i := 0; i < 2; i++ {
		require.NoError(t, testStore.WithControlPlane(context.Background(), func(_ context.Context) error {
			return nil
		}))
	}

	// One call from a different line — bumps a different site.
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(_ context.Context) error {
		return nil
	}))

	after := infra.ControlPlaneCounters()
	afterSum := int64(0)
	for _, v := range after {
		afterSum += v
	}
	require.Equal(t, beforeSum+3, afterSum,
		"ControlPlaneCounters should grow by exactly 3 across the 3 invocations")

	// Exactly two call-site keys should change (the two lines above). Compare
	// deltas rather than requiring new map entries so `go test -count=N`
	// remains a valid repeatability check in the same test process.
	changedSites := 0
	deltas := map[int64]int{}
	for site, count := range after {
		delta := count - before[site]
		if delta == 0 {
			continue
		}
		changedSites++
		deltas[delta]++
		require.Contains(t, site, "tenant_tx_test.go", "site should reference this test file")
	}
	require.Equal(t, 2, changedSites, "expected exactly 2 changed call sites")
	require.Equal(t, 1, deltas[int64(2)], "one call site should be invoked twice")
	require.Equal(t, 1, deltas[int64(1)], "one call site should be invoked once")
}
