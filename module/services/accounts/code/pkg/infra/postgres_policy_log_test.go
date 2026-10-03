//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// The policy log's LOCAL half against real PostgreSQL.
//
// The protocol's ordering and failure handling are tested on fakes, in-package,
// because those are about which write happens first. These are about the SQL the
// fakes cannot prove: that the idempotency key is enforced by the database
// rather than by a check-then-write, that a commit cannot precede an append,
// and that the cursor only moves forward.

func policyLogEntry(id, subject string) *business.PolicyLogEntry {
	return &business.PolicyLogEntry{
		OperationID: id,
		Decision:    business.PolicyLogNarrowed,
		SubjectKind: business.PolicyLogPrincipal,
		SubjectID:   subject,
		Actor:       "admin-1",
	}
}

func policyLogReceipt(id string, sequence uint64, at time.Time) *business.PolicyLogReceipt {
	return &business.PolicyLogReceipt{
		Receipt:    "receipt-" + id,
		Sequence:   sequence,
		AppendedAt: at,
	}
}

// An append then a commit: the row goes from gap to applied.
func TestPolicyLogAppendThenCommit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	operation := "op-" + business.NewIDString()
	subject := business.NewIDString()

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		inserted, err := testStore.RecordPolicyLogAppend(ctx,
			policyLogReceipt(operation, 101, now), policyLogEntry(operation, subject))
		require.NoError(t, err)
		require.True(t, inserted)

		gaps, err := testStore.UncommittedPolicyLogOperations(ctx)
		require.NoError(t, err)
		require.True(t, containsPolicyGap(gaps, operation),
			"an appended, uncommitted operation must read as a gap")

		require.NoError(t, testStore.CommitPolicyLogOperation(ctx, operation, now))

		gaps, err = testStore.UncommittedPolicyLogOperations(ctx)
		require.NoError(t, err)
		require.False(t, containsPolicyGap(gaps, operation),
			"a committed operation is no longer a gap")
		return nil
	}))
}

// The idempotency key is enforced by the DATABASE. A second append of the same
// operation writes nothing and reports not-inserted, so a retry cannot produce
// two receipts for one revocation.
func TestPolicyLogAppendIsIdempotentOnTheOperationID(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	operation := "op-" + business.NewIDString()

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		first, err := testStore.RecordPolicyLogAppend(ctx,
			policyLogReceipt(operation, 201, now), policyLogEntry(operation, business.NewIDString()))
		require.NoError(t, err)
		require.True(t, first)

		second, err := testStore.RecordPolicyLogAppend(ctx,
			policyLogReceipt(operation, 202, now), policyLogEntry(operation, business.NewIDString()))
		require.NoError(t, err)
		require.False(t, second, "a retry of one operation must not append twice")
		return nil
	}))
}

// A commit for an operation that was never appended is an ERROR, not a quiet
// no-op.
//
// It would mean a narrowing reached the commit without the log having witnessed
// it, which is the one direction the protocol forbids outright — so it must be
// loud rather than absorbed.
func TestPolicyLogCommitWithoutAnAppendIsRefused(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		err := testStore.CommitPolicyLogOperation(ctx, "op-never-appended-"+business.NewIDString(), time.Now().UTC())
		require.Error(t, err, "committing an unwitnessed narrowing must be refused")
		return nil
	}))
}

// Committing twice is a no-op rather than a rewrite: the first commit is when
// the narrowing took effect, and a retry must not move that timestamp.
func TestPolicyLogDoubleCommitDoesNotRewriteTheTimestamp(t *testing.T) {
	first := time.Now().UTC().Truncate(time.Millisecond)
	later := first.Add(time.Hour)
	operation := "op-" + business.NewIDString()

	// The write and the read-back are SEPARATE transactions, deliberately. The
	// read-back helper opens its own control-plane transaction, so performing it
	// inside the writing one would read a snapshot taken before the insert
	// committed — a first draft did exactly that and failed with "no rows",
	// which is the isolation working rather than the store misbehaving.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := testStore.RecordPolicyLogAppend(ctx,
			policyLogReceipt(operation, 301, first), policyLogEntry(operation, business.NewIDString()))
		require.NoError(t, err)
		require.NoError(t, testStore.CommitPolicyLogOperation(ctx, operation, first))
		require.NoError(t, testStore.CommitPolicyLogOperation(ctx, operation, later),
			"a second commit is a retry, not an error")
		return nil
	}))

	var committed time.Time
	require.NoError(t, testStore.ScanAsControlPlane(testCtx,
		`SELECT committed_at FROM public.policy_log_commits WHERE operation_id = $1`,
		[]any{&committed}, operation))
	require.WithinDuration(t, first, committed, time.Second,
		"the commit time is when the narrowing took effect, not when it was retried")
}

// The cursor only moves FORWARD.
//
// Two replicas reconcile independently, so a slower one finishing second must
// not rewind the cursor — which would cause entries to be re-read and recorded
// again as gaps that had already been closed, stopping both hosts serving for a
// reason neither could resolve.
func TestPolicyLogCursorOnlyMovesForward(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		require.NoError(t, testStore.AdvancePolicyLogCursor(ctx, 500, now))
		sequence, reachedAt, err := testStore.PolicyLogCursorState(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(500), sequence)
		require.NotNil(t, reachedAt)

		// A slower replica reports an older sequence.
		require.NoError(t, testStore.AdvancePolicyLogCursor(ctx, 400, now.Add(time.Second)))
		sequence, _, err = testStore.PolicyLogCursorState(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(500), sequence, "a lagging replica must not rewind the cursor")
		return nil
	}))
}

// Reaching the log is recorded even when there was nothing new to read.
//
// A host that only stamped `reached_at` when entries arrived would look stale
// during a quiet period and would stop serving for lack of news — turning an
// absence of authority changes into an outage.
func TestPolicyLogCursorRecordsContactWithNoNewEntries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		sequence, _, err := testStore.PolicyLogCursorState(ctx)
		require.NoError(t, err)

		later := now.Add(2 * time.Minute)
		require.NoError(t, testStore.AdvancePolicyLogCursor(ctx, sequence, later))
		got, reachedAt, err := testStore.PolicyLogCursorState(ctx)
		require.NoError(t, err)
		require.Equal(t, sequence, got, "no new entries means no new sequence")
		require.NotNil(t, reachedAt)
		require.WithinDuration(t, later, *reachedAt, time.Second,
			"but contact with the log is still a fact about now")
		return nil
	}))
}

func containsPolicyGap(gaps []*business.PolicyLogGap, operation string) bool {
	for _, gap := range gaps {
		if gap.OperationID == operation {
			return true
		}
	}
	return false
}
