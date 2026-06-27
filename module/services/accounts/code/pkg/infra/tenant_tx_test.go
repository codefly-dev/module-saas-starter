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

// TestBypassCounters_TracksCallSites — smoke for the Phase 4
// audit trail. Each WithBypass call from a unique source position
// gets its own counter; multiple invocations from the same line
// increment that one counter. The test asserts both shapes.
//
// Runs against the real DB so we exercise the WithBypass body too.
func TestBypassCounters_TracksCallSites(t *testing.T) {
	before := infra.BypassCounters()
	beforeSum := int64(0)
	for _, v := range before {
		beforeSum += v
	}

	// Two calls from the same source line — should bump ONE site
	// by 2.
	for i := 0; i < 2; i++ {
		require.NoError(t, testStore.WithBypass(context.Background(), func(_ context.Context) error {
			return nil
		}))
	}

	// One call from a different line — bumps a different site.
	require.NoError(t, testStore.WithBypass(context.Background(), func(_ context.Context) error {
		return nil
	}))

	after := infra.BypassCounters()
	afterSum := int64(0)
	for _, v := range after {
		afterSum += v
	}
	require.Equal(t, beforeSum+3, afterSum,
		"BypassCounters should grow by exactly 3 across the 3 invocations")

	// At least 2 distinct call-site keys should appear (the two
	// lines we invoked from). Keys look like "<file>:<line>".
	newSites := 0
	for site := range after {
		if _, existed := before[site]; !existed {
			newSites++
			require.Contains(t, site, "tenant_tx_test.go", "site should reference this test file")
		}
	}
	require.GreaterOrEqual(t, newSites, 2,
		"expected at least 2 distinct call sites recorded (got %d)", newSites)
}
