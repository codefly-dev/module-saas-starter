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

// The relay's contract (ADR 0009): batches of a configured size, a partial batch
// delivered once its oldest event has waited the configured time, each batch
// written to the archive and the store before its queue rows are deleted, and
// every organization's events kept in queue order. These run it against
// in-memory fakes of the three sides.

// memQueue is a fake AuditQueue with the Postgres queue's delete-on-success
// semantics.
type memQueue struct {
	mu     sync.Mutex
	rows   []QueuedAuditEvent
	leased bool // another relay holds the lease
	// crashes makes the next n successful deliveries fail before their delete
	// commits, the way a crash or a lost connection between the writes and the
	// delete would.
	crashes int
	drains  int
}

func (q *memQueue) Drain(ctx context.Context, limit int, deliver func(context.Context, []QueuedAuditEvent) (bool, error)) (int, error) {
	q.mu.Lock()
	q.drains++
	if q.leased {
		q.mu.Unlock()
		return 0, nil
	}
	events := slices.Clone(q.rows[:min(limit, len(q.rows))])
	q.mu.Unlock()

	delivered, err := deliver(ctx, events)
	if err != nil || !delivered {
		return 0, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.crashes > 0 {
		q.crashes--
		return 0, errors.New("connection lost before the delete committed")
	}
	q.rows = q.rows[len(events):]
	return len(events), nil
}

func (q *memQueue) remaining() []QueuedAuditEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.rows)
}

// memWarehouse is a fake store of record that keeps one record per event id,
// as BigQuery's insert-id deduplication does for a prompt redelivery.
type memWarehouse struct {
	mu       sync.Mutex
	byID     map[string]AuditRecord
	appended []AuditRecord // every record in append order, duplicates included
	fail     func(batch AuditBatch) error
}

func (w *memWarehouse) AppendAuditBatch(_ context.Context, batch AuditBatch) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		if err := w.fail(batch); err != nil {
			return err
		}
	}
	if w.byID == nil {
		w.byID = map[string]AuditRecord{}
	}
	for _, record := range batch.Records {
		w.appended = append(w.appended, record)
		w.byID[record.Entry.ID] = record
	}
	return nil
}

// memArchive is a fake archive: one object per write, never overwritten.
type memArchive struct {
	mu      sync.Mutex
	objects map[string]AuditBatch
	order   []string
	fail    func(batch AuditBatch) error
}

func (a *memArchive) WriteAuditBatch(_ context.Context, batch AuditBatch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		if err := a.fail(batch); err != nil {
			return err
		}
	}
	if a.objects == nil {
		a.objects = map[string]AuditBatch{}
	}
	if _, exists := a.objects[batch.ID]; exists {
		return fmt.Errorf("object %s already exists", batch.ID)
	}
	a.objects[batch.ID] = batch
	a.order = append(a.order, batch.ID)
	return nil
}

// eventIDs is every event id across the archive, duplicates included.
func (a *memArchive) eventIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ids []string
	for _, name := range a.order {
		for _, record := range a.objects[name].Records {
			ids = append(ids, record.Entry.ID)
		}
	}
	return ids
}

// relayFixture wires a relay over the fakes with a controllable clock.
type relayFixture struct {
	queue     *memQueue
	warehouse *memWarehouse
	archive   *memArchive
	now       time.Time
	relay     *AuditRelay
	batches   int
}

func newRelayFixture(t *testing.T, batchSize int, maxWait time.Duration, types DeclaredAuditEventTypeReader) *relayFixture {
	t.Helper()
	f := &relayFixture{
		queue:     &memQueue{},
		warehouse: &memWarehouse{},
		archive:   &memArchive{},
		now:       time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	f.relay = f.newRelay(t, batchSize, maxWait, types)
	return f
}

// newRelay builds a relay over the fixture's queue and stores: a second call
// is the same deployment after a restart, which remembers nothing.
func (f *relayFixture) newRelay(t *testing.T, batchSize int, maxWait time.Duration, types DeclaredAuditEventTypeReader) *AuditRelay {
	t.Helper()
	relay, err := NewAuditRelay(AuditRelayConfig{
		Queue:        f.queue,
		Store:        f.warehouse,
		Archive:      f.archive,
		Types:        types,
		DeploymentID: "deployment-1",
		BatchSize:    batchSize,
		MaxWait:      maxWait,
		Now:          func() time.Time { return f.now },
		NewBatchID: func() string {
			f.batches++
			return fmt.Sprintf("batch-%d", f.batches)
		},
	})
	require.NoError(t, err)
	return relay
}

// enqueue adds events for orgs in turn ("" is a platform event), queued age ago.
func (f *relayFixture) enqueue(age time.Duration, eventType EventType, orgs ...string) []string {
	f.queue.mu.Lock()
	defer f.queue.mu.Unlock()
	var ids []string
	for _, org := range orgs {
		seq := int64(len(f.queue.rows) + 1)
		if n := len(f.queue.rows); n > 0 {
			seq = f.queue.rows[n-1].Seq + 1
		}
		id := NewIDString()
		ids = append(ids, id)
		f.queue.rows = append(f.queue.rows, QueuedAuditEvent{
			Seq: seq,
			Entry: AuditEntry{
				ID:            id,
				OrgID:         org,
				ActorID:       NewIDString(),
				ActorType:     ActorTypeUser,
				EventType:     eventType,
				SchemaVersion: 1,
				Resource:      "session",
				ResourceID:    fmt.Sprintf("r-%d", seq),
				Payload:       map[string]any{"seq": seq},
				CreatedAt:     f.now.Add(-age),
			},
			EnqueuedAt: f.now.Add(-age),
		})
	}
	return ids
}

func repeatOrg(org string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = org
	}
	return out
}

func TestAuditRelayDeliversFullBatchesThenTheDuePartialOne(t *testing.T) {
	f := newRelayFixture(t, 500, 5*time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 1200)...)

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1200, delivered)
	require.Empty(t, f.queue.remaining())

	require.Len(t, f.archive.order, 3, "one archive object per batch")
	var sizes []int
	for _, name := range f.archive.order {
		sizes = append(sizes, len(f.archive.objects[name].Records))
	}
	require.Equal(t, []int{500, 500, 200}, sizes)
	require.Len(t, f.warehouse.byID, 1200)
	for i, record := range f.warehouse.appended {
		require.Equal(t, ids[i], record.Entry.ID, "batches append in queue order")
	}
}

func TestAuditRelayHoldsAPartialBatchUntilMaxWait(t *testing.T) {
	f := newRelayFixture(t, 500, 5*time.Second, nil)
	f.enqueue(time.Second, EventSessionRevoked, repeatOrg(teeOrgID, 10)...)

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, delivered, "a partial batch younger than max wait waits for more events")
	require.Len(t, f.queue.remaining(), 10)
	require.Empty(t, f.archive.order)
	require.Empty(t, f.warehouse.appended)

	f.now = f.now.Add(4 * time.Second) // the oldest event has now waited 5s
	delivered, err = f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 10, delivered)
	require.Empty(t, f.queue.remaining())
}

func TestAuditRelayKeepsEachOrganizationsOrder(t *testing.T) {
	const orgA, orgB = "00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"
	f := newRelayFixture(t, 4, time.Second, nil)
	// Interleaved organizations and platform events, as concurrent requests queue them.
	f.enqueue(time.Minute, EventSessionRevoked, orgA, orgB, "", orgA, orgA, orgB, "", orgB, orgA, "")

	want := map[string][]int64{}
	for _, row := range f.queue.remaining() {
		want[row.Entry.OrgID] = append(want[row.Entry.OrgID], row.Seq)
	}

	_, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)

	gotStore := map[string][]int64{}
	for _, record := range f.warehouse.appended {
		gotStore[record.Entry.OrgID] = append(gotStore[record.Entry.OrgID], record.Entry.Payload["seq"].(int64))
	}
	require.Equal(t, want, gotStore, "each organization's events, and the platform's, reach the store in queue order")

	gotArchive := map[string][]int64{}
	for _, name := range f.archive.order {
		for _, record := range f.archive.objects[name].Records {
			gotArchive[record.Entry.OrgID] = append(gotArchive[record.Entry.OrgID], record.Entry.Payload["seq"].(int64))
		}
	}
	require.Equal(t, want, gotArchive, "and the archive in the same order")
}

func TestAuditRelayDeletesOnlyAfterBothWritesAcknowledge(t *testing.T) {
	t.Run("archive refuses", func(t *testing.T) {
		f := newRelayFixture(t, 3, time.Second, nil)
		f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 3)...)
		f.archive.fail = func(AuditBatch) error { return errors.New("archive unavailable") }

		delivered, err := f.relay.DrainOnce(context.Background())
		require.ErrorContains(t, err, "archive unavailable")
		require.Zero(t, delivered)
		require.Len(t, f.queue.remaining(), 3, "nothing is deleted before the archive acknowledges")
		require.Empty(t, f.warehouse.appended, "the store is not written ahead of the archive")
	})
	t.Run("store refuses", func(t *testing.T) {
		f := newRelayFixture(t, 3, time.Second, nil)
		f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 3)...)
		f.warehouse.fail = func(AuditBatch) error { return errors.New("warehouse unavailable") }

		delivered, err := f.relay.DrainOnce(context.Background())
		require.ErrorContains(t, err, "warehouse unavailable")
		require.Zero(t, delivered)
		require.Len(t, f.queue.remaining(), 3, "nothing is deleted before the store acknowledges")
		require.Len(t, f.archive.order, 1)

		f.warehouse.fail = nil
		delivered, err = f.relay.DrainOnce(context.Background())
		require.NoError(t, err)
		require.Equal(t, 3, delivered)
		require.Empty(t, f.queue.remaining(), "both acknowledged: the rows are deleted")
	})
}

func TestAuditRelayCrashBetweenWriteAndDeleteLosesAndDuplicatesNothingInTheStore(t *testing.T) {
	f := newRelayFixture(t, 5, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 5)...)
	f.queue.crashes = 1

	_, err := f.relay.DrainOnce(context.Background())
	require.Error(t, err)
	require.Len(t, f.queue.remaining(), 5, "a crash before the delete commits leaves every event queued: nothing is lost")
	require.Len(t, f.warehouse.byID, 5, "the store already holds the batch")

	// The process died with the delete: the relay that drains next is a new one.
	restarted := f.newRelay(t, 5, time.Second, nil)
	delivered, err := restarted.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, delivered)
	require.Empty(t, f.queue.remaining())

	require.Len(t, f.warehouse.appended, 10, "the batch was appended twice")
	require.Len(t, f.warehouse.byID, 5, "and the store keeps one record per event id")
	for _, record := range f.warehouse.appended {
		require.Contains(t, ids, record.Entry.ID)
	}

	require.Len(t, f.archive.order, 2, "a redelivered batch is a new archive object, never a rewrite")
	archived := f.archive.eventIDs()
	require.Len(t, archived, 10, "so the archive holds the events twice")
	unique := slices.Compact(slices.Sorted(slices.Values(archived)))
	require.ElementsMatch(t, ids, unique, "and deduplicating by event id recovers each event exactly once")
	first, second := f.archive.objects[f.archive.order[0]], f.archive.objects[f.archive.order[1]]
	for i := range first.Records {
		require.Equal(t, first.Records[i].DetailsSHA256, second.Records[i].DetailsSHA256,
			"duplicates carry the same hash, so they are provably the same event")
	}
}

// Retrying the head batch in the same process repeats only the write that has
// not been acknowledged: a warehouse outage does not fill the locked archive
// with copies of one batch, and a lost delete is retried as a delete.
func TestAuditRelayRetriesOnlyTheUnacknowledgedWrite(t *testing.T) {
	t.Run("warehouse outage", func(t *testing.T) {
		f := newRelayFixture(t, 3, time.Second, nil)
		f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 3)...)
		outage := 4
		f.warehouse.fail = func(AuditBatch) error {
			if outage > 0 {
				outage--
				return errors.New("warehouse unavailable")
			}
			return nil
		}
		for range 4 {
			_, err := f.relay.DrainOnce(context.Background())
			require.Error(t, err)
		}
		delivered, err := f.relay.DrainOnce(context.Background())
		require.NoError(t, err)
		require.Equal(t, 3, delivered)
		require.Len(t, f.archive.order, 1, "five attempts, one archive object")
		require.Len(t, f.warehouse.appended, 3)
		require.Equal(t, f.archive.order[0], "batch-1", "the retried batch keeps its identity")
	})
	t.Run("lost delete", func(t *testing.T) {
		f := newRelayFixture(t, 3, time.Second, nil)
		f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 3)...)
		f.queue.crashes = 1
		_, err := f.relay.DrainOnce(context.Background())
		require.Error(t, err)

		delivered, err := f.relay.DrainOnce(context.Background())
		require.NoError(t, err)
		require.Equal(t, 3, delivered)
		require.Len(t, f.archive.order, 1)
		require.Len(t, f.warehouse.appended, 3, "neither side is written twice")
	})
	t.Run("a grown batch is a new batch", func(t *testing.T) {
		f := newRelayFixture(t, 4, time.Second, nil)
		f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 2)...)
		f.warehouse.fail = func(AuditBatch) error { return errors.New("warehouse unavailable") }
		_, err := f.relay.DrainOnce(context.Background())
		require.Error(t, err)

		f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 2)...)
		f.warehouse.fail = nil
		delivered, err := f.relay.DrainOnce(context.Background())
		require.NoError(t, err)
		require.Equal(t, 4, delivered)
		require.Len(t, f.archive.order, 2, "different rows are archived as their own object")
		require.Len(t, f.archive.objects[f.archive.order[1]].Records, 4)
	})
}

func TestAuditRelayStopsAtTheFirstFailedBatch(t *testing.T) {
	f := newRelayFixture(t, 2, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6)...)
	appends := 0
	f.warehouse.fail = func(AuditBatch) error {
		appends++
		if appends == 2 {
			return errors.New("warehouse unavailable")
		}
		return nil
	}

	delivered, err := f.relay.DrainOnce(context.Background())
	require.Error(t, err)
	require.Equal(t, 2, delivered)
	var remaining []string
	for _, row := range f.queue.remaining() {
		remaining = append(remaining, row.Entry.ID)
	}
	require.Equal(t, ids[2:], remaining, "the failed batch and everything behind it stay queued, in order")
}

func TestAuditRelayLeaseHeldElsewhereDeliversNothing(t *testing.T) {
	f := newRelayFixture(t, 2, time.Second, nil)
	f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	f.queue.leased = true

	delivered, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, delivered)
	require.Len(t, f.queue.remaining(), 4)
	require.Empty(t, f.archive.order)
}

// declaredTypes serves one declared type, or an outage.
type declaredTypes struct {
	declared *DeclaredAuditEventType
	err      error
}

func (d declaredTypes) GetDeclaredAuditEventType(_ context.Context, t EventType) (*DeclaredAuditEventType, error) {
	if d.err != nil {
		return nil, d.err
	}
	if d.declared != nil && d.declared.Type == t {
		return d.declared, nil
	}
	return nil, nil
}

func TestAuditRelayClassifiesEachRecordByItsType(t *testing.T) {
	declared := declaredType("acme", AuditVisibilityTenant)
	f := newRelayFixture(t, 4, time.Second, declaredTypes{declared: declared})
	login := f.enqueue(time.Minute, EventAuthLogin, teeOrgID)[0]
	read := f.enqueue(time.Minute, EventDocumentRead, teeOrgID)[0]
	solution := f.enqueue(time.Minute, declared.Type, teeOrgID)[0]
	retired := f.enqueue(time.Minute, "saas.retired.event_type", teeOrgID)[0]

	_, err := f.relay.DrainOnce(context.Background())
	require.NoError(t, err)

	classOf := func(id string) AuditRetentionClass { return f.warehouse.byID[id].Retention }
	require.Equal(t, RetentionSecurity, classOf(login), "authentication is security")
	require.Equal(t, RetentionContent, classOf(read), "a content read is content")
	require.Equal(t, RetentionContent, classOf(solution), "a declared type is content")
	require.Equal(t, RetentionSecurity, classOf(retired), "a type the registry no longer knows keeps its details")

	for _, record := range f.warehouse.byID {
		want, err := CanonicalAuditDetails(record.Entry.Payload)
		require.NoError(t, err)
		require.Equal(t, want, record.Details)
		require.Equal(t, AuditDetailsSHA256(want), record.DetailsSHA256)
	}
	batch := f.archive.objects[f.archive.order[0]]
	require.Equal(t, "deployment-1", batch.DeploymentID)
	require.Equal(t, "batch-1", batch.ID)
	require.Equal(t, f.now, batch.ComposedAt)
}

func TestAuditRelayRegistryOutageFailsTheBatch(t *testing.T) {
	declared := declaredType("acme", AuditVisibilityTenant)
	f := newRelayFixture(t, 2, time.Second, declaredTypes{err: errors.New("database unavailable")})
	f.enqueue(time.Minute, declared.Type, teeOrgID, teeOrgID)

	_, err := f.relay.DrainOnce(context.Background())
	require.ErrorContains(t, err, "database unavailable")
	require.Len(t, f.queue.remaining(), 2, "a type that cannot be resolved is never guessed at")
	require.Empty(t, f.archive.order)
}

func TestAuditRelayRunsUntilShutdown(t *testing.T) {
	f := newRelayFixture(t, 3, 100*time.Millisecond, nil)
	f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 7)...)

	f.relay.Start(context.Background())
	require.Eventually(t, func() bool { return len(f.queue.remaining()) == 0 }, 5*time.Second, 10*time.Millisecond)
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.relay.Shutdown(shutdown))
	require.Len(t, f.warehouse.byID, 7)
}

func TestNewAuditRelayValidatesItsConfiguration(t *testing.T) {
	valid := AuditRelayConfig{
		Queue: &memQueue{}, Store: &memWarehouse{}, Archive: &memArchive{},
		DeploymentID: "deployment-1", BatchSize: 10, MaxWait: time.Second,
	}
	_, err := NewAuditRelay(valid)
	require.NoError(t, err)

	for name, mutate := range map[string]func(*AuditRelayConfig){
		"no queue":         func(c *AuditRelayConfig) { c.Queue = nil },
		"no store":         func(c *AuditRelayConfig) { c.Store = nil },
		"no archive":       func(c *AuditRelayConfig) { c.Archive = nil },
		"no deployment":    func(c *AuditRelayConfig) { c.DeploymentID = " " },
		"zero batch":       func(c *AuditRelayConfig) { c.BatchSize = 0 },
		"oversized batch":  func(c *AuditRelayConfig) { c.BatchSize = MaxAuditRelayBatchSize + 1 },
		"no max wait":      func(c *AuditRelayConfig) { c.MaxWait = 0 },
		"negative waiting": func(c *AuditRelayConfig) { c.MaxWait = -time.Second },
	} {
		config := valid
		mutate(&config)
		_, err := NewAuditRelay(config)
		require.Error(t, err, name)
	}
}

func TestAuditRelayBackoffDoublesToAMinute(t *testing.T) {
	var got []time.Duration
	backoff := time.Duration(0)
	for range 8 {
		backoff = nextAuditRelayBackoff(backoff)
		got = append(got, backoff)
	}
	require.Equal(t, []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, time.Minute, time.Minute,
	}, got)
}
