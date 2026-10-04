//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// Two reconcile passes meeting on a binding that has NEVER been delivered.
//
// `GetSolutionHostBindingForUpdate` takes `FOR UPDATE`, and its own comment says
// that is what serializes two replicas applying the same generation. It is not,
// on the first delivery: there is no row, so `FOR UPDATE` locks nothing. Both
// passes read nil, both build a record carrying no applied state, and
// `SaveSolutionHostBinding` writes every column on every save — so whichever
// pass commits second sets `applied_*` to the NULLs it is carrying and erases
// what the other one applied.
//
// Nothing refuses that. `solution_host_bindings_applied_whole` is satisfied by
// seven NULLs, because an absent applied group is legal. The visible result is a
// binding that is serving and reads as never applied: every reader of applied
// state sees no presence and the reconciler re-applies a generation it had
// already admitted.
//
// So this drives the two passes against each other through the store methods the
// reconcile pass itself uses, on the real schema:
//
//  1. pass A opens a control-plane transaction and takes the lock on a binding
//     with no row;
//  2. pass B does the same, and must BLOCK — it cannot be allowed to read nil
//     while A holds the binding;
//  3. A writes desired-only state and commits;
//  4. B unblocks, and must now SEE A's row rather than nil, so the record it
//     saves is A's record plus its own change — not a fresh one that erases it.
//
// Step 2 is the assertion that fails without the advisory lock: B returns
// immediately with nil.
func TestSolutionHostBindingFirstDeliverySerializesTwoPasses(t *testing.T) {
	binding := uniqueAlias("acme.first-insert")
	now := time.Now().UTC()

	// Pass A: hold the binding, do not commit.
	held := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			record, err := testStore.GetSolutionHostBindingForUpdate(ctx, binding)
			if err != nil {
				return err
			}
			if record != nil {
				t.Errorf("fixture: binding %q already has a record", binding)
			}
			close(held)
			<-release
			return testStore.SaveSolutionHostBinding(ctx, &business.SolutionHostBindingRecord{
				BindingID:      binding,
				HostCoordinate: "acme.example/host",
				HostComponent:  "accounts",
				Desired: &business.SolutionHostBindingGeneration{
					Generation: 1,
					Digest:     fixtureDigest(binding + ":1"),
					Document:   "{}",
					At:         now,
				},
				UpdatedAt: now,
			})
		})
	}()
	<-held

	// Pass B: the same binding, concurrently. It must not get past the lock.
	type read struct {
		record *business.SolutionHostBindingRecord
		err    error
	}
	second := make(chan read, 1)
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelSecond()
	go func() {
		var out read
		out.err = testStore.WithControlPlane(secondCtx, func(ctx context.Context) error {
			record, err := testStore.GetSolutionHostBindingForUpdate(ctx, binding)
			out.record = record
			return err
		})
		second <- out
	}()

	// Either B waits on a lock — in which case it cannot return at all while A's
	// transaction is open — or it takes no lock and returns as soon as the read
	// finds nothing. Three seconds separates those with a wide margin.
	select {
	case out := <-second:
		require.Failf(t, "the second pass did not wait for the first",
			"it read the binding while another pass held it (record=%v, err=%v), so both passes build a record with no applied state and the second erases the first", out.record, out.err)
	case <-time.After(3 * time.Second):
	}

	close(release)
	require.NoError(t, <-finished, "the first pass must commit its desired state")

	out := <-second
	require.NoError(t, out.err)
	require.NotNil(t, out.record,
		"after the first pass committed, the second must read ITS row: reading nil here is the erasure, because the record this pass then saves carries no applied state and overwrites every applied column")
	require.NotNil(t, out.record.Desired)
	require.Equal(t, uint64(1), out.record.Desired.Generation)
}
