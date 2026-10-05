package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A queued row the store refuses for good must not hold every other tenant's
// events behind it (ADR 0009). The relay isolates it by splitting a refused
// batch, and sets it aside only on proof that the store itself was reachable.

// rejects makes a store write fail while the batch holds any of ids, the way a
// warehouse refuses a row it cannot accept.
func rejects(ids ...string) func(AuditBatch) error {
	return func(batch AuditBatch) error {
		for _, record := range batch.Records {
			if slices.Contains(ids, record.Entry.ID) {
				return fmt.Errorf("row %s rejected: invalid value", record.Entry.ID)
			}
		}
		return nil
	}
}

func queuedIDs(rows []QueuedAuditEvent) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.Entry.ID
	}
	return ids
}

func TestAuditRelaySetsAsideARowTheStoreRefusesAndDeliversTheRest(t *testing.T) {
	f := newRelayFixture(t, 10, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 10)...)
	poison := ids[3]
	f.warehouse.fail = rejects(poison)

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err, "a refused row that is set aside is not a failed delivery")
	require.Equal(t, 9, delivered)
	require.Empty(t, f.queue.remaining(), "the rest of the batch is delivered, not held behind the refused row")
	require.Len(t, f.warehouse.byID, 9)
	require.NotContains(t, f.warehouse.byID, poison)

	set := f.queue.quarantined()
	require.Equal(t, []string{poison}, queuedIDs(set))

	require.Len(t, f.archive.order, 1, "the archive holds the batch once, whole")
	require.ElementsMatch(t, ids, f.archive.eventIDs(), "including the row the store refused")
}

func TestAuditRelayNeverSetsAsideAnythingDuringAnOutage(t *testing.T) {
	f := newRelayFixture(t, 16, time.Second, nil)
	f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 16)...)
	writes := 0
	f.warehouse.fail = func(AuditBatch) error {
		writes++
		return errors.New("warehouse unavailable")
	}

	delivered, err := f.relay.DrainOnce(context.Background())
	require.ErrorContains(t, err, "warehouse unavailable")
	require.Zero(t, delivered)
	require.Len(t, f.queue.remaining(), 16, "everything stays queued")
	require.Empty(t, f.queue.quarantined(), "a failure that no other row's success contradicts proves nothing about the row")
	require.LessOrEqual(t, writes, 16, "and the search for a refused row gives up instead of writing every subrange")
}

func TestAuditRelayDoesNotSetAsideRowsThatFailAfterTheStoreWentDown(t *testing.T) {
	f := newRelayFixture(t, 8, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 8)...)
	// The whole batch fails, the first half is accepted, then the store is gone.
	calls := 0
	f.warehouse.fail = func(AuditBatch) error {
		calls++
		if calls == 2 {
			return nil
		}
		return errors.New("warehouse unavailable")
	}

	delivered, err := f.relay.DrainOnce(context.Background())
	require.Error(t, err)
	require.Equal(t, 4, delivered, "what the store accepted is delivered")
	require.Empty(t, f.queue.quarantined(), "rows that failed after the last accepted write were not refused for their content")
	require.Equal(t, ids[4:], queuedIDs(f.queue.remaining()))
}

func TestAuditRelayRetriesATransientRefusalBeforeSettingAnythingAside(t *testing.T) {
	f := newRelayFixture(t, 4, time.Second, nil)
	f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	// The whole batch, then the first half and the first row alone fail; every
	// other write is accepted, the witness and the first row's second try
	// included.
	script := []bool{false, false, true, false, true}
	calls := 0
	f.warehouse.fail = func(AuditBatch) error {
		calls++
		if calls <= len(script) && !script[calls-1] {
			return errors.New("blip")
		}
		return nil
	}

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, delivered)
	require.Empty(t, f.queue.remaining())
	require.Empty(t, f.queue.quarantined(), "a row the store accepts on a second try was never refused")
	require.Len(t, f.warehouse.byID, 4)
}

func TestAuditRelayRefusalOfEveryRowIsIndistinguishableFromAnOutage(t *testing.T) {
	f := newRelayFixture(t, 4, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	f.warehouse.fail = rejects(ids...)

	_, err := f.relay.DrainOnce(context.Background())
	require.Error(t, err)
	require.Len(t, f.queue.remaining(), 4)
	require.Empty(t, f.queue.quarantined(), "with no row accepted there is no evidence the store was reachable")
}

// An archive treats a precondition failure on an object name as "this batch,
// already written". A name must therefore never be written again with
// different rows.
func TestAuditRelayNeverWritesDifferentRowsUnderAnArchiveName(t *testing.T) {
	f := newRelayFixture(t, 8, time.Second, nil)
	archive := &namedArchive{}
	f.relay.archive = archive
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 8)...)
	f.warehouse.fail = rejects(ids[5])

	_, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, f.queue.quarantined(), 1)
	require.Equal(t, 1, archive.writes(), "splitting the store write does not re-archive the halves")

	// An archive write whose acknowledgement was lost, then more rows queued:
	// the grown batch is a different set of rows and takes a name of its own.
	g := newRelayFixture(t, 4, time.Second, nil)
	lossy := &namedArchive{loseFirstAck: true}
	g.relay.archive = lossy
	g.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 2)...)
	_, err = g.relay.DrainOnce(context.Background())
	require.Error(t, err)
	g.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 2)...)
	_, err = g.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Empty(t, lossy.conflicts(), "a name is never reused for different rows")
	require.Empty(t, g.queue.remaining())
}

// namedArchive behaves as the object-store writers do: writing a name that
// exists succeeds without writing, because it is taken to be the same batch.
type namedArchive struct {
	mu           sync.Mutex
	objects      map[string][]string
	loseFirstAck bool
	written      int
	reused       []string
}

func (a *namedArchive) WriteAuditBatch(_ context.Context, batch AuditBatch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.objects == nil {
		a.objects = map[string][]string{}
	}
	ids := make([]string, len(batch.Records))
	for i, record := range batch.Records {
		ids[i] = record.Entry.ID
	}
	if existing, ok := a.objects[batch.ID]; ok {
		if !slices.Equal(existing, ids) {
			a.reused = append(a.reused, batch.ID)
		}
		return nil
	}
	a.objects[batch.ID] = ids
	a.written++
	if a.loseFirstAck {
		a.loseFirstAck = false
		return errors.New("acknowledgement lost")
	}
	return nil
}

func (a *namedArchive) writes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.written
}

func (a *namedArchive) conflicts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.reused)
}

// The set-aside decision is kept until the rows leave the queue, so a lost
// commit does not turn proof into doubt.
func TestAuditRelayKeepsItsVerdictWhenTheCommitIsLost(t *testing.T) {
	f := newRelayFixture(t, 6, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6)...)
	writes := 0
	refuse := rejects(ids[2])
	f.warehouse.fail = func(batch AuditBatch) error {
		writes++
		return refuse(batch)
	}
	f.queue.crashes = 1

	_, err := f.relay.DrainOnce(context.Background())
	require.Error(t, err)
	require.Len(t, f.queue.remaining(), 6, "the lost commit leaves every row queued")
	written := writes

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, delivered)
	require.Equal(t, written, writes, "nothing is written twice and nothing is re-judged")
	require.Equal(t, []string{ids[2]}, queuedIDs(f.queue.quarantined()))
	require.Empty(t, f.queue.remaining())
}

// unencodable is a payload value no store can serialize.
func unencodable() map[string]any { return map[string]any{"bad": make(chan int)} }

func TestAuditRelaySetsAsideARowItCannotComposeOnlyWhenOthersCan(t *testing.T) {
	f := newRelayFixture(t, 3, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 3)...)
	f.queue.rows[1].Entry.Payload = unencodable()

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, delivered)
	require.Equal(t, []string{ids[1]}, queuedIDs(f.queue.quarantined()))
	require.Empty(t, f.queue.remaining())

	g := newRelayFixture(t, 2, time.Second, nil)
	g.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 2)...)
	for i := range g.queue.rows {
		g.queue.rows[i].Entry.Payload = unencodable()
	}
	_, err = g.relay.DrainOnce(context.Background())
	require.Error(t, err)
	require.Len(t, g.queue.remaining(), 2, "when no row composes, nothing shows the rows rather than the relay at fault")
	require.Empty(t, g.queue.quarantined())
}

// Each batch is bounded on its own: a delivery that is working must not be cut
// off because earlier batches in the same pass used up a shared budget.
func TestAuditRelayGivesEveryBatchItsOwnDeadline(t *testing.T) {
	f := newRelayFixture(t, 2, time.Second, nil)
	f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6)...)
	f.queue.onDrain = func(ctx context.Context) {
		time.Sleep(15 * time.Millisecond)
	}

	_, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)

	deadlines := f.queue.deadlines()
	require.Len(t, deadlines, 4, "three full batches and the empty look that ends the pass")
	for i, deadline := range deadlines {
		require.NotZero(t, deadline, "batch %d has a deadline of its own", i)
	}
	for i := 1; i < len(deadlines); i++ {
		require.True(t, deadlines[i].After(deadlines[i-1]), "batch %d's deadline starts when it does", i)
	}
}

func TestAuditRelayBackoffResetsOnProgress(t *testing.T) {
	failure := errors.New("warehouse unavailable")
	require.Equal(t, time.Duration(0), relayBackoffAfter(8*time.Second, 3, nil), "success clears it")
	require.Equal(t, 16*time.Second, relayBackoffAfter(8*time.Second, 0, failure), "no progress keeps doubling")
	require.Equal(t, time.Second, relayBackoffAfter(32*time.Second, 5, failure), "a failure after progress starts over")
}
