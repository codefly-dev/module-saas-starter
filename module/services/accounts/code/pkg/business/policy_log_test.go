//go:build pure

package business

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The policy log protocol, and every way a host could end up serving authority
// the log says was revoked.
//
// These run on a fake log and a fake local store, in-package, because what is
// under test is the ORDERING and the failure handling — which of the two writes
// happens first, and what is true after each one fails. A database would add
// nothing to that and would hide the interleavings behind a transaction.

// noopControlPlaneStore satisfies the Store interface by embedding it, so every
// method exists and only the one these tests reach is implemented.
//
// Embedding rather than writing out the whole interface: a hand-written stub of
// a large interface has to be updated whenever the interface grows, and the
// update is mechanical, so it gets done without thought. An embedded nil panics
// loudly if a test reaches a method it did not mean to — which is the behaviour
// worth having, since these tests are about ordering and not about persistence.
type noopControlPlaneStore struct{ Store }

func (noopControlPlaneStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// fakePolicyLog records what it was asked and can be made to fail or hang.
type fakePolicyLog struct {
	appends  []*PolicyLogEntry
	receipts map[string]*PolicyLogReceipt
	nextSeq  uint64

	appendErr  error
	entriesErr error
	// forceReceipt makes Append return exactly this, including nil, so a
	// malformed receipt is testable without pre-seeding the idempotency map —
	// which a first draft tried and could not express "returns nil".
	forceReceipt    *PolicyLogReceipt
	forceReceiptSet bool
	entries         []*PolicyLogRecord
	// block makes Append wait, so the bounded deadline is testable.
	block time.Duration
}

func newFakePolicyLog() *fakePolicyLog {
	return &fakePolicyLog{receipts: map[string]*PolicyLogReceipt{}}
}

func (f *fakePolicyLog) Append(ctx context.Context, entry *PolicyLogEntry) (*PolicyLogReceipt, error) {
	if f.block > 0 {
		select {
		case <-time.After(f.block):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.appendErr != nil {
		return nil, f.appendErr
	}
	if f.forceReceiptSet {
		return f.forceReceipt, nil
	}
	// Idempotent on the operation id, as the real log must be: a retry returns
	// the original receipt rather than appending again.
	if existing, ok := f.receipts[entry.OperationID]; ok {
		return existing, nil
	}
	f.nextSeq++
	receipt := &PolicyLogReceipt{
		Receipt:    "receipt-" + entry.OperationID,
		Sequence:   f.nextSeq,
		AppendedAt: time.Unix(1700000000, 0).UTC(),
	}
	f.receipts[entry.OperationID] = receipt
	f.appends = append(f.appends, entry)
	return receipt, nil
}

func (f *fakePolicyLog) Entries(_ context.Context, after uint64, _ int) ([]*PolicyLogRecord, error) {
	if f.entriesErr != nil {
		return nil, f.entriesErr
	}
	var out []*PolicyLogRecord
	for _, record := range f.entries {
		if record.Sequence > after {
			out = append(out, record)
		}
	}
	return out, nil
}

// fakePolicyLogStore is the local half, with hooks to fail the commit.
type fakePolicyLogStore struct {
	appended  map[string]*PolicyLogGap
	committed map[string]time.Time
	sequence  uint64
	reachedAt *time.Time

	commitErr error
	appendErr error
}

func newFakePolicyLogStore() *fakePolicyLogStore {
	return &fakePolicyLogStore{
		appended:  map[string]*PolicyLogGap{},
		committed: map[string]time.Time{},
	}
}

func (f *fakePolicyLogStore) RecordPolicyLogAppend(
	_ context.Context, receipt *PolicyLogReceipt, entry *PolicyLogEntry,
) (bool, error) {
	if f.appendErr != nil {
		return false, f.appendErr
	}
	if _, ok := f.appended[entry.OperationID]; ok {
		return false, nil
	}
	f.appended[entry.OperationID] = &PolicyLogGap{
		OperationID: entry.OperationID,
		Sequence:    receipt.Sequence,
		SubjectKind: entry.SubjectKind,
		SubjectID:   entry.SubjectID,
		AppendedAt:  receipt.AppendedAt,
	}
	return true, nil
}

func (f *fakePolicyLogStore) CommitPolicyLogOperation(_ context.Context, id string, at time.Time) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	if _, ok := f.appended[id]; !ok {
		return errors.New("no append recorded for " + id)
	}
	if _, already := f.committed[id]; !already {
		f.committed[id] = at
	}
	return nil
}

func (f *fakePolicyLogStore) UncommittedPolicyLogOperations(_ context.Context) ([]*PolicyLogGap, error) {
	gaps := []*PolicyLogGap{}
	for id, gap := range f.appended {
		if _, done := f.committed[id]; !done {
			gaps = append(gaps, gap)
		}
	}
	return gaps, nil
}

func (f *fakePolicyLogStore) PolicyLogCursorState(_ context.Context) (uint64, *time.Time, error) {
	return f.sequence, f.reachedAt, nil
}

func (f *fakePolicyLogStore) AdvancePolicyLogCursor(_ context.Context, sequence uint64, at time.Time) error {
	if sequence > f.sequence {
		f.sequence = sequence
	}
	stamp := at
	f.reachedAt = &stamp
	return nil
}

// policyLogService wires a service with both fakes and a frozen clock.
//
// A frozen clock, not a real one: the staleness window is a duration, and a test
// that waited for real time would either be slow or would assert on a window it
// had not actually crossed.
func policyLogService(t *testing.T, log PolicyLog, store PolicyLogStore, now time.Time) *Service {
	t.Helper()
	service := &Service{store: noopControlPlaneStore{}}
	service.SetPolicyLog(log, store)
	service.policyClock = func() time.Time { return now }
	return service
}

func narrowing(id string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: id,
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogPrincipal,
		SubjectID:   "principal-1",
		Actor:       "admin-1",
	}
}

// ---------------------------------------------------------------------------
// The ordering: append BEFORE anything narrows
// ---------------------------------------------------------------------------

// The happy path appends, records, applies and commits — and the APPLY must not
// have run before the append landed.
func TestNarrowingAppendsBeforeItApplies(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	now := time.Unix(1700000100, 0).UTC()
	service := policyLogService(t, log, store, now)
	// A serving host has reached the log. Without this the MayServe assertion
	// below would pass for the WRONG reason — staleness rather than the gap —
	// and a first draft of this test failed here, which is the implementation
	// refusing to let the test lie about why it was happy.
	store.reachedAt = &now

	applied := false
	require.NoError(t, service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error {
			// By the time the narrowing runs, the log must already have
			// witnessed it: otherwise a crash here leaves authority reduced
			// with nothing to say so.
			require.Len(t, log.appends, 1, "the append must precede the narrowing")
			require.Contains(t, store.appended, "op-1", "the receipt must be recorded before the narrowing")
			applied = true
			return nil
		}))
	require.True(t, applied)
	require.Contains(t, store.committed, "op-1")

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.True(t, mayServe)
	require.Empty(t, state.Gaps)
}

// An append that FAILS narrows nothing. Safe, because authority was not reduced
// and nobody was told it was.
func TestNarrowingIsRefusedWhenTheAppendFails(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	log.appendErr = errors.New("warehouse unavailable")
	service := policyLogService(t, log, store, time.Unix(1700000100, 0).UTC())

	applied := false
	err := service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error {
			applied = true
			return nil
		})
	require.ErrorIs(t, err, ErrPolicyLogUnreachable)
	require.False(t, applied, "nothing may narrow when the log did not witness it")
	require.Empty(t, store.appended)
}

// A log that returns NO usable receipt has witnessed nothing, whatever it
// returned. Storing an empty receipt would make "witnessed" and "attempted"
// indistinguishable afterwards.
func TestNarrowingIsRefusedWhenTheLogReturnsNoReceipt(t *testing.T) {
	for name, receipt := range map[string]*PolicyLogReceipt{
		"nothing at all": nil,
		"no token":       {Sequence: 1},
		"no sequence":    {Receipt: "r"},
	} {
		t.Run(name, func(t *testing.T) {
			log, store := newFakePolicyLog(), newFakePolicyLogStore()
			log.forceReceipt, log.forceReceiptSet = receipt, true
			service := policyLogService(t, log, store, time.Unix(1700000100, 0).UTC())

			applied := false
			err := service.WithPolicyLoggedNarrowing(
				context.Background(), narrowing("op-1"), func(context.Context) error {
					applied = true
					return nil
				})
			require.ErrorIs(t, err, ErrPolicyLogUnreachable)
			require.False(t, applied, "a narrowing the log did not witness must not apply")
			require.Empty(t, store.appended, "and no receipt may be recorded for it")
		})
	}
}

// ---------------------------------------------------------------------------
// The asymmetry: append-ok / commit-fail must FAIL CLOSED
// ---------------------------------------------------------------------------

// The central case. The log says the narrowing happened; this host did not apply
// it; so this host MUST STOP SERVING until the gap is closed.
//
// Serving the wider authority it still holds locally would be serving authority
// that was revoked — and the log, which is the record, has already said so.
func TestAppendedButUncommittedNarrowingStopsServing(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	store.commitErr = errors.New("database went away mid-transaction")
	now := time.Unix(1700000100, 0).UTC()
	service := policyLogService(t, log, store, now)
	// The log has been reached recently, so staleness is NOT what refuses here.
	store.reachedAt = &now

	err := service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error { return nil })
	require.Error(t, err)

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.False(t, mayServe, "a host with an unapplied logged narrowing must not serve")
	require.Len(t, state.Gaps, 1)
	require.Equal(t, "op-1", state.Gaps[0].OperationID)
	require.False(t, state.Unreachable, "the refusal must be the GAP, not staleness")
}

// Once the gap is closed, serving resumes. Without this the first test could
// pass against a host that never serves at all.
func TestServingResumesWhenTheGapIsClosed(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	store.commitErr = errors.New("transient")
	now := time.Unix(1700000100, 0).UTC()
	service := policyLogService(t, log, store, now)
	store.reachedAt = &now

	require.Error(t, service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error { return nil }))
	mayServe, _, _ := service.MayServe(context.Background())
	require.False(t, mayServe)

	// The retry succeeds: the log returns the SAME receipt, and the commit lands.
	store.commitErr = nil
	require.NoError(t, service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error { return nil }))

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.True(t, mayServe)
	require.Empty(t, state.Gaps)
	require.Len(t, log.appends, 1, "a retry of one operation must append ONCE")
}

// A narrowing whose APPLY fails leaves the gap too, and that is deliberate: the
// log has witnessed the operation, so until this host either applies it or an
// operator resolves it, the host must not serve the authority the log says is
// gone.
func TestFailedApplyAlsoStopsServing(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	now := time.Unix(1700000100, 0).UTC()
	service := policyLogService(t, log, store, now)
	store.reachedAt = &now

	require.Error(t, service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error {
			return errors.New("the narrowing itself failed")
		}))

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.False(t, mayServe)
	require.Len(t, state.Gaps, 1)
}

// ---------------------------------------------------------------------------
// Refuse to serve while the log is unreachable
// ---------------------------------------------------------------------------

// A host that has never reached the log does not know whether its authority is
// current, so it does not serve.
func TestNeverHavingReachedTheLogStopsServing(t *testing.T) {
	service := policyLogService(t, newFakePolicyLog(), newFakePolicyLogStore(),
		time.Unix(1700000100, 0).UTC())

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.False(t, mayServe)
	require.True(t, state.Unreachable)
	require.Nil(t, state.LastReachedAt)
}

// Having reached it too long ago is the same answer. Serving what the host last
// believed is serving authority that may have been revoked since.
func TestStaleLogContactStopsServing(t *testing.T) {
	store := newFakePolicyLogStore()
	now := time.Unix(1700000100, 0).UTC()
	stale := now.Add(-PolicyLogStaleAfter - time.Second)
	store.reachedAt = &stale
	service := policyLogService(t, newFakePolicyLog(), store, now)

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.False(t, mayServe)
	require.True(t, state.Unreachable)
}

// Inside the window it serves. The window is not zero deliberately: a log round
// trip per request would make the log's availability the host's own, and a blip
// would become an outage.
func TestRecentLogContactServes(t *testing.T) {
	store := newFakePolicyLogStore()
	now := time.Unix(1700000100, 0).UTC()
	fresh := now.Add(-PolicyLogStaleAfter + time.Second)
	store.reachedAt = &fresh
	service := policyLogService(t, newFakePolicyLog(), store, now)

	mayServe, state, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.True(t, mayServe)
	require.False(t, state.Unreachable)
}

// ---------------------------------------------------------------------------
// The bounded deadline
// ---------------------------------------------------------------------------

// A log that hangs costs a refusal, not an indefinite wait. The point is lock
// accumulation: the append happens before any transaction opens, so a slow log
// cannot hold database locks — but a caller blocked forever still holds its
// request and everything above it.
func TestASlowLogRefusesRatherThanHanging(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	log.block = PolicyLogDeadline + 2*time.Second
	service := policyLogService(t, log, store, time.Unix(1700000100, 0).UTC())

	applied := false
	started := time.Now()
	err := service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error {
			applied = true
			return nil
		})
	require.ErrorIs(t, err, ErrPolicyLogUnreachable)
	require.False(t, applied, "a narrowing the log did not witness must not apply")
	require.Less(t, time.Since(started), PolicyLogDeadline+time.Second,
		"the append must be bounded by PolicyLogDeadline")
	require.Empty(t, store.appended)
}

// ---------------------------------------------------------------------------
// Reconciliation finds a gap another replica opened
// ---------------------------------------------------------------------------

// A narrowing appended by ANOTHER replica — one this host never performed —
// becomes a gap here, and stops this host serving until it is applied.
//
// This is the requirement that a startup-only check cannot meet: the entry
// appears in the log at runtime, and every replica must honour it.
func TestReconciliationFindsAnotherReplicasAppend(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	now := time.Unix(1700000100, 0).UTC()
	service := policyLogService(t, log, store, now)
	store.reachedAt = &now

	mayServe, _, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.True(t, mayServe, "the control: nothing is outstanding yet")

	// Another replica appended a revocation this host knows nothing about.
	log.entries = []*PolicyLogRecord{{
		PolicyLogEntry: *narrowing("op-elsewhere"),
		Receipt:        "receipt-op-elsewhere",
		Sequence:       7,
		LoggedAt:       now,
	}}

	state, err := service.ReconcilePolicyLog(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Gaps, 1)
	require.Equal(t, "op-elsewhere", state.Gaps[0].OperationID)

	mayServe, _, err = service.MayServe(context.Background())
	require.NoError(t, err)
	require.False(t, mayServe, "a narrowing another replica logged must stop this host too")
	require.Equal(t, uint64(7), store.sequence, "the cursor advances past what was recorded")
}

// An unreachable log does NOT advance the cursor, so the staleness window closes
// and the host stops serving. A reconciler that advanced the cursor on failure
// would make an unreachable log look like a quiet one.
func TestReconciliationDoesNotAdvanceTheCursorWhenTheLogIsUnreachable(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	log.entriesErr = errors.New("warehouse unavailable")
	now := time.Unix(1700000100, 0).UTC()
	store.reachedAt = &now
	store.sequence = 3
	service := policyLogService(t, log, store, now)

	_, err := service.ReconcilePolicyLog(context.Background())
	require.ErrorIs(t, err, ErrPolicyLogUnreachable)
	require.Equal(t, uint64(3), store.sequence, "the cursor must not move when the log was not read")
	require.Equal(t, &now, store.reachedAt, "nor may reached_at, or staleness would never trigger")
}

// ---------------------------------------------------------------------------
// What may be logged
// ---------------------------------------------------------------------------

// An entry the log could not be replayed from must not reach the log: a bad
// entry that is accepted is worse than a refused operation, because it is a gap
// nothing can close.
func TestUnreplayableEntriesAreRefusedBeforeTheyReachTheLog(t *testing.T) {
	for name, entry := range map[string]*PolicyLogEntry{
		"no operation id": {Decision: PolicyLogNarrowed, SubjectKind: PolicyLogPrincipal, SubjectID: "p", Actor: "a"},
		"no actor":        {OperationID: "op", Decision: PolicyLogNarrowed, SubjectKind: PolicyLogPrincipal, SubjectID: "p"},
		"no subject":      {OperationID: "op", Decision: PolicyLogNarrowed, SubjectKind: PolicyLogPrincipal, Actor: "a"},
		"unknown kind":    {OperationID: "op", Decision: PolicyLogNarrowed, SubjectKind: "other", SubjectID: "p", Actor: "a"},
		"unknown decision": {OperationID: "op", Decision: "deleted", SubjectKind: PolicyLogPrincipal,
			SubjectID: "p", Actor: "a"},
	} {
		t.Run(name, func(t *testing.T) {
			log, store := newFakePolicyLog(), newFakePolicyLogStore()
			service := policyLogService(t, log, store, time.Unix(1700000100, 0).UTC())
			require.Error(t, service.WithPolicyLoggedNarrowing(
				context.Background(), entry, func(context.Context) error { return nil }))
			require.Empty(t, log.appends, "a malformed entry must not reach the log")
		})
	}
}

// An authorised REGRANT is logged too. Without it the log cannot tell "this
// authority was restored" from "it was never taken away", and those have
// different answers to whether the subject may act now.
func TestRegrantsAreLoggedAsWell(t *testing.T) {
	log, store := newFakePolicyLog(), newFakePolicyLogStore()
	now := time.Unix(1700000100, 0).UTC()
	service := policyLogService(t, log, store, now)

	entry := narrowing("op-regrant")
	entry.Decision = PolicyLogRegranted
	require.NoError(t, service.WithPolicyLoggedNarrowing(
		context.Background(), entry, func(context.Context) error { return nil }))
	require.Len(t, log.appends, 1)
	require.Equal(t, PolicyLogRegranted, log.appends[0].Decision)
}

// A host with NO log refuses to narrow, because the narrowing would be
// unwitnessed and a restore would undo it silently — while still serving, because
// nothing has been narrowed through the protocol and there is nothing
// unreconciled. The two answers look inconsistent and are not.
func TestWithoutALogNarrowingIsRefusedButServingContinues(t *testing.T) {
	service := &Service{store: noopControlPlaneStore{}}
	service.policyClock = func() time.Time { return time.Unix(1700000100, 0).UTC() }

	applied := false
	err := service.WithPolicyLoggedNarrowing(
		context.Background(), narrowing("op-1"), func(context.Context) error {
			applied = true
			return nil
		})
	require.ErrorIs(t, err, ErrPolicyLogUnreachable)
	require.False(t, applied)

	mayServe, _, err := service.MayServe(context.Background())
	require.NoError(t, err)
	require.True(t, mayServe)
}
