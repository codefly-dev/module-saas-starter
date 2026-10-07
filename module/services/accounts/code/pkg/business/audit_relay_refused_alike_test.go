package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A store that refuses every row of a write for one and the same reason is not
// reporting bad rows: a table was changed under the relay, and the rows are as
// deliverable as they were. Setting them all aside would quarantine the whole
// stream and leave reads with gaps nobody was told of, so the relay keeps them
// queued and fails the delivery, which is what backs it off and lets the queue's
// depth and age raise the relay-lag alert. A refusal that singles out some of
// the rows of a write is still a refusal of those rows. These pin both halves.

const schemaChanged = "no such field: occurred_at_utc"

// refusesAlike makes a store write fail with a refusal of each of ids it holds,
// every one for the same reason. The cause carries the row, as a store's error
// does; the reason does not.
func refusesAlike(reason string, ids ...string) func(AuditBatch) error {
	return func(batch AuditBatch) error {
		var refused []error
		for _, record := range batch.Records {
			if slices.Contains(ids, record.Entry.ID) {
				refused = append(refused, &PermanentRowRejection{
					EventID: record.Entry.ID,
					Cause:   fmt.Errorf("the row of event %s was refused: %s", record.Entry.ID, reason),
					Reason:  reason,
				})
			}
		}
		return errors.Join(refused...)
	}
}

func TestAuditRelayKeepsRowsQueuedWhenTheStoreRefusesEveryRowForOneReason(t *testing.T) {
	for _, size := range []int{2, 8, 500} {
		f := newRelayFixture(t, size, time.Second, nil)
		ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, size)...)
		f.warehouse.fail = refusesAlike(schemaChanged, ids...)

		delivered, err := f.relay.DrainOnce(context.Background())

		require.Error(t, err, "batch of %d: the delivery fails, which is what backs the relay off", size)
		require.ErrorContains(t, err, schemaChanged, "batch of %d: the reason is the one the store gave", size)
		require.ErrorContains(t, err, "not set aside")
		require.Empty(t, PermanentRowRejections(err), "batch of %d: the failure is not itself a refusal of any row", size)
		require.Zero(t, delivered)
		require.Empty(t, f.queue.quarantined(), "batch of %d: nothing is set aside", size)
		require.Len(t, f.queue.remaining(), size, "batch of %d: every row stays queued, in order", size)
		require.Equal(t, ids, queuedIDs(f.queue.remaining()))
		require.Empty(t, f.queue.reasons)
		require.Len(t, f.archive.order, 1, "batch of %d: the archive holds the batch, once", size)
	}
}

func TestAuditRelayDeliversEveryRowOnceTheStoreIsRestoredAfterRefusingEveryRowAlike(t *testing.T) {
	f := newRelayFixture(t, 6, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6)...)
	f.warehouse.fail = refusesAlike(schemaChanged, ids...)

	for pass := 0; pass < 4; pass++ {
		_, err := f.relay.DrainOnce(context.Background())
		require.Error(t, err, "pass %d", pass)
		require.Empty(t, f.queue.quarantined(), "pass %d: a refusal repeated is still the store's", pass)
		require.Len(t, f.queue.remaining(), 6, "pass %d", pass)
	}

	f.warehouse.fail = nil // the table is put back
	delivered, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, 6, delivered)
	require.Empty(t, f.queue.remaining())
	require.Empty(t, f.queue.quarantined(), "no row was ever set aside")
	require.ElementsMatch(t, ids, mapKeys(f.warehouse.byID), "every row reached the store")
	require.Len(t, f.archive.order, 1, "and the archive was written once through all of it")
}

func TestAuditRelayLoopBacksOffWhileTheStoreRefusesEveryRowAlike(t *testing.T) {
	// The loop's failure path: a delivery that fails without moving a row doubles
	// the wait, up to the cap, and a queue that keeps its rows is what the relay-lag
	// alert reads. Only the error matters here: the loop treats any failure alike.
	f := newRelayFixture(t, 4, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	f.warehouse.fail = refusesAlike(schemaChanged, ids...)

	pass, err := f.relay.drain(context.Background(), nil)

	require.Error(t, err)
	require.Zero(t, pass.Removed())
	require.Equal(t, time.Second, relayBackoffAfter(0, pass.Removed(), err))
	require.Equal(t, 2*time.Second, relayBackoffAfter(time.Second, pass.Removed(), err))
}

func TestAuditRelaySetsAsideOnlyTheRowsARefusalSingleOut(t *testing.T) {
	f := newRelayFixture(t, 6, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6)...)
	// Two of the six are refused, for the same reason; the other four are not
	// refused at all. The reason being shared does not make it the store's: it
	// does not cover the write.
	f.warehouse.fail = refusesAlike("value is out of range", ids[1], ids[4])

	delivered, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err, "a refusal that is set aside is not a failed delivery")
	require.Equal(t, 4, delivered)
	require.ElementsMatch(t, []string{ids[1], ids[4]}, queuedIDs(f.queue.quarantined()), "exactly the refused rows")
	require.Empty(t, f.queue.remaining())
	require.ElementsMatch(t, []string{ids[0], ids[2], ids[3], ids[5]}, mapKeys(f.warehouse.byID))
	for _, id := range []string{ids[1], ids[4]} {
		require.Contains(t, f.queue.reasonOf(id), "value is out of range")
	}
}

func TestAuditRelaySetsAsideRowsRefusedForDifferentReasonsEvenWhenTheyAreEveryRow(t *testing.T) {
	f := newRelayFixture(t, 4, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	f.warehouse.fail = func(batch AuditBatch) error {
		var refused []error
		for _, record := range batch.Records {
			refused = append(refused, &PermanentRowRejection{
				EventID: record.Entry.ID,
				Cause:   fmt.Errorf("value of row %s is invalid", record.Entry.ID),
				Reason:  "invalid value in " + record.Entry.ID,
			})
		}
		return errors.Join(refused...)
	}

	_, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err)
	require.ElementsMatch(t, ids, queuedIDs(f.queue.quarantined()), "rows that each have a reason of their own are rows at fault")
	require.Empty(t, f.queue.remaining())
}

func TestAuditRelayNeedsAReasonFromTheStoreBeforeItReadsRowsAsAlike(t *testing.T) {
	// A rejection that states no reason is never alike to another, so every store
	// that predates the reason keeps the behaviour it had.
	f := newRelayFixture(t, 4, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	f.warehouse.fail = func(batch AuditBatch) error {
		var refused []error
		for _, record := range batch.Records {
			refused = append(refused, &PermanentRowRejection{EventID: record.Entry.ID, Cause: errors.New(schemaChanged)})
		}
		return errors.Join(refused...)
	}

	_, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err)
	require.ElementsMatch(t, ids, queuedIDs(f.queue.quarantined()))
}

func TestAuditRelaySetsAsideALoneRowRefusedWithAReasonAndBatchesOfOne(t *testing.T) {
	// One row refused alone cannot be told from a store that refuses everything. It
	// is the row's: judged as a batch of one is judged as a batch of five thousand,
	// so a poison row can never wedge a relay whose batches hold one row.
	for _, batch := range []int{1, 10} {
		f := newRelayFixture(t, batch, time.Second, nil)
		ids := f.enqueue(time.Minute, EventSessionRevoked, teeOrgID)
		f.warehouse.fail = refusesAlike(schemaChanged, ids...)

		_, err := f.relay.DrainOnce(context.Background())

		require.NoError(t, err, "batch size %d", batch)
		require.Equal(t, ids, queuedIDs(f.queue.quarantined()), "batch size %d", batch)
		require.Empty(t, f.queue.remaining())
	}

	f := newRelayFixture(t, 1, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 3)...)
	f.warehouse.fail = refusesAlike(schemaChanged, ids[0])
	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, delivered, "the rows behind the refused first row are delivered")
	require.Equal(t, ids[:1], queuedIDs(f.queue.quarantined()))
}

func TestAuditRelayJudgesEachWriteOnItsOwn(t *testing.T) {
	// The first write singles out two rows, which are set aside; the store then
	// refuses every row of the second write for one reason. The first decision
	// stands, the second is the store's.
	f := newRelayFixture(t, 6, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6)...)
	writes := 0
	f.warehouse.fail = func(batch AuditBatch) error {
		writes++
		if writes == 1 {
			return refusesAlike("value is out of range", ids[0], ids[1])(batch)
		}
		var all []string
		for _, record := range batch.Records {
			all = append(all, record.Entry.ID)
		}
		return refusesAlike(schemaChanged, all...)(batch)
	}

	delivered, err := f.relay.DrainOnce(context.Background())

	require.Error(t, err)
	require.ErrorContains(t, err, schemaChanged)
	require.Equal(t, 2, writes)
	require.Zero(t, delivered)
	require.ElementsMatch(t, ids[:2], queuedIDs(f.queue.quarantined()), "the two the first write singled out")
	require.Equal(t, ids[2:], queuedIDs(f.queue.remaining()), "the four the second refused alike stay queued")
}

func TestRefusedForOneReason(t *testing.T) {
	refusal := func(id, reason string) *PermanentRowRejection {
		return &PermanentRowRejection{EventID: id, Cause: errors.New(id), Reason: reason}
	}
	err := errors.Join(refusal("a", "r"), refusal("b", "r"), refusal("c", "r"))

	reason, whole := RefusedForOneReason(err, []string{"a", "b", "c"})
	require.True(t, whole)
	require.Equal(t, "r", reason)

	_, whole = RefusedForOneReason(err, []string{"a", "b", "c", "d"})
	require.False(t, whole, "a row the refusal does not name is not refused")
	_, whole = RefusedForOneReason(err, []string{"a", "b"})
	require.True(t, whole, "the write is its rows, and these are all of them")
	_, whole = RefusedForOneReason(errors.Join(refusal("a", "r"), refusal("b", "other")), []string{"a", "b"})
	require.False(t, whole, "two reasons are two causes")
	_, whole = RefusedForOneReason(errors.Join(refusal("a", ""), refusal("b", "")), []string{"a", "b"})
	require.False(t, whole, "no reason, no comparison")
	_, whole = RefusedForOneReason(errors.Join(refusal("a", "r"), errors.New("429")), []string{"a"})
	require.False(t, whole, "one row is not enough to read a store from")
	_, whole = RefusedForOneReason(refusal("a", "r"), []string{"a", "a"})
	require.False(t, whole, "the same event twice is one event")
	_, whole = RefusedForOneReason(nil, []string{"a", "b"})
	require.False(t, whole)
	_, whole = RefusedForOneReason(err, nil)
	require.False(t, whole)
}
