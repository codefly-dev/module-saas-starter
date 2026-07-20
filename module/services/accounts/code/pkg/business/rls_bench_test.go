package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
)

// Benchmarks for the RLS-bracketing helpers. The point is not to
// chase a number but to give operators a baseline + a regression
// alarm: if a future change makes WithOrgTx 10× slower, this catches
// it.
//
// Reference numbers (M1 Pro, single-node Docker postgres, single
// goroutine) at time of writing:
//
//   BenchmarkRLS_NoOpQuery_NoWrap   ~850µs/op   bare pool QueryRow("SELECT 1")
//   BenchmarkRLS_NoOpQuery_OrgTx   ~1680µs/op   + Begin + SET LOCAL app.current_org_id + Commit
//   BenchmarkRLS_NoOpQuery_UserTx  ~1510µs/op   + Begin + SET LOCAL app.current_user_id + Commit
//   BenchmarkRLS_NoOpQuery_ControlPlane ~700µs/op + Begin + SET LOCAL ROLE + Commit
//   (* ControlPlane also runs recordControlPlane which emits a wool.Debug;
//      with DEBUG-level logging the per-op number includes that.)
//
// Read: the wrap adds ~600-800µs over a bare query. For a typical
// request that does 2-3 queries inside a single tx, this amortizes
// to ~200-400µs per query — the "RLS tax". Hot-path endpoints that
// fire many small queries should batch into one WithOrgTx (see
// GetOrgEntitlements + entitlements_tx_test.go for the pattern).
//
// If these numbers diverge significantly (>1.5×), investigate:
//   - pool exhaustion (too many concurrent transactions)
//   - new SET LOCAL statements added to the helpers
//   - Postgres server tuning (shared_buffers, work_mem)
//   - Docker disk slowness (most common in dev)

func BenchmarkRLS_NoOpQuery_ControlPlane(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		err := testStore.WithControlPlane(context.Background(), func(_ context.Context) error {
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRLS_NoOpQuery_OrgTx(b *testing.B) {
	const orgID = "11111111-1111-1111-1111-111111111111"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		err := testStore.WithOrgTx(context.Background(), orgID, func(_ context.Context) error {
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRLS_NoOpQuery_UserTx(b *testing.B) {
	const userID = "22222222-2222-2222-2222-222222222222"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		err := testStore.WithUserTx(context.Background(), userID, func(_ context.Context) error {
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRLS_NoOpQuery_NoWrap — the un-wrapped baseline. Each
// iteration does a single SELECT 1 against the pool. Subtract this
// from the wrapped numbers for the marginal cost of WithOrgTx /
// WithControlPlane / WithUserTx.
func BenchmarkRLS_NoOpQuery_NoWrap(b *testing.B) {
	pool := testStore.Pool()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var n int
		if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil {
			b.Fatal(err)
		}
	}
}

// Sanity: var to keep the business import alive in this file (other
// _test files import it; this one indirectly through testStore).
var _ = business.ServiceVersion
