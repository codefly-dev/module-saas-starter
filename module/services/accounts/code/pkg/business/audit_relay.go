package business

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

// QueuedAuditEvent is one row of the transactional audit queue.
type QueuedAuditEvent struct {
	// Seq is the queue's insertion order.
	Seq   int64
	Entry AuditEntry
	// EnqueuedAt is when the transaction that queued the event began.
	EnqueuedAt time.Time
}

// QuarantinedAuditEvent is a queued row the relay set aside, with why.
type QuarantinedAuditEvent struct {
	Seq int64
	// Reason is the error the store gave when it refused the row.
	Reason string
}

// AuditDeliveryOutcome is what the relay decided about the rows one Drain
// handed it. The queue applies it in the transaction that read them.
type AuditDeliveryOutcome struct {
	// Delivered are the rows the archive and the store both acknowledged. The
	// queue deletes them.
	Delivered []int64
	// Quarantined are the rows the relay set aside because the store refused
	// them while it was accepting others. The queue moves them, whole, into the
	// quarantine; it never deletes them.
	Quarantined []QuarantinedAuditEvent
	// Pending is why any other row stays queued, nil when none does.
	Pending error
}

// AuditDrainResult is how many rows one Drain removed from the queue.
type AuditDrainResult struct {
	Delivered   int
	Quarantined int
}

// Removed is every row the drain took out of the queue.
func (r AuditDrainResult) Removed() int { return r.Delivered + r.Quarantined }

// AuditQueue is the relay's side of the transactional queue.
type AuditQueue interface {
	// Drain hands deliver the oldest queued events whose writing transactions
	// have finished — at most limit, in queue order — while holding a lease no
	// other relay holds at the same time. It applies the outcome deliver
	// returns in the same transaction: the delivered rows are deleted, the
	// quarantined rows are moved to the quarantine, every other row stays. When
	// deliver returns an error it changes nothing. When the outcome leaves rows
	// queued, Drain returns what it removed together with the outcome's Pending
	// error. A relay that finds the lease held elsewhere returns zero and no
	// error without calling deliver.
	//
	// The queue holds back a row while a transaction that began before the row's
	// own is still running, so a late commit is not left behind a batch that was
	// already handed over. That gate is transaction visibility, not sequence
	// order: a row whose transaction took its transaction id earlier but its
	// sequence number later can be handed over ahead of a lower sequence number
	// still to commit. Nothing downstream depends on delivery order: both stores
	// key a record by event id and every read orders by event time.
	Drain(ctx context.Context, limit int, deliver func(ctx context.Context, events []QueuedAuditEvent) (AuditDeliveryOutcome, error)) (AuditDrainResult, error)
}

// Relay defaults. A batch of 500 is BigQuery's recommended streaming request
// size; five seconds bounds how long a quiet deployment's events wait.
const (
	DefaultAuditRelayBatchSize = 500
	MaxAuditRelayBatchSize     = 5000
	DefaultAuditRelayMaxWait   = 5 * time.Second
	// MaxAuditRelayMaxWait is the longest a partial batch may be configured to
	// wait. The relay-lag alert fires at five minutes, so a wait that long would
	// page on a healthy relay; a minute leaves room under it.
	MaxAuditRelayMaxWait = time.Minute
	// auditRelayBatchTimeout bounds one batch's delivery, which holds a queue
	// transaction open across the archive and warehouse writes. Every batch of
	// a pass has its own.
	auditRelayBatchTimeout = 2 * time.Minute
	auditRelayMaxBackoff   = time.Minute
	// AuditRelayBookkeepingTimeout bounds deleting acknowledged rows once their
	// writes are done. It runs on a context of its own so a delivery that used
	// its whole batch budget still gets to record what it delivered.
	AuditRelayBookkeepingTimeout = 30 * time.Second
	// auditRelayMaxSplitWrites bounds the store writes one batch spends
	// isolating the rows a refused batch write holds.
	auditRelayMaxSplitWrites = 64
	// auditRelayProbeWrites is how many store writes in a row may fail, with none
	// accepted, before the relay concludes that the store is unreachable rather
	// than refusing rows, and stops searching.
	auditRelayProbeWrites = 16
	// auditRelayMaxReasonLength bounds the error text kept with a quarantined row.
	auditRelayMaxReasonLength = 1000
)

// AuditRelayConfig wires a relay.
type AuditRelayConfig struct {
	Queue   AuditQueue
	Store   AuditStoreWriter
	Archive AuditArchive
	// Types serves the solution-declared half of the registry; nil resolves the
	// code catalog alone.
	Types DeclaredAuditEventTypeReader
	// DeploymentID stamps every record with the deployment it was written in.
	DeploymentID string
	// BatchSize is the most events one delivery carries.
	BatchSize int
	// MaxWait is how long a partial batch may wait for more events before it is
	// delivered anyway, measured from its oldest event.
	MaxWait time.Duration

	// Now and NewBatchID are seams for tests; nil uses the clock and a UUID.
	Now        func() time.Time
	NewBatchID func() string
}

// AuditRelay drains the transactional audit queue into the store of record and
// the archive (ADR 0009). Delivery is at-least-once: each batch is written to
// the archive as one object, then appended to the store, and its queue rows are
// deleted only after both writes were acknowledged. A crash anywhere before
// that delete leaves the rows queued and the batch is delivered again, so both
// the store and the archive may hold an event twice; every read of either
// returns each event once by event id.
//
// The archive is written first so that a failure between the two writes
// repeats the archive write, not the store's. The archive object is written
// whole and once: an archive treats a precondition failure on an object name as
// "this batch, already written", so a name must never be written again with
// different rows. Only the store write is split when the store refuses a batch;
// it keys each record by event id and has no use for a batch's name.
//
// A row the store refuses must not hold the rest of the queue behind it. When a
// batch write fails the relay splits the batch and retries the halves, down to
// single rows. A single row is set aside — moved whole to the quarantine,
// never deleted — only when the store refused it alone, accepted another row
// afterwards, and refused it again on a second try: during an outage every
// write fails and nothing is set aside.
//
// While it runs, the relay remembers what each queued row has had acknowledged
// and what it decided about it, so retrying a batch during an outage of one
// side does not write it to the other side again: a warehouse outage does not
// fill the locked archive with copies of one batch, and a lost delete is
// retried as a delete. Only a restart forgets, which is the at-least-once case
// both sides are built to absorb.
type AuditRelay struct {
	queue        AuditQueue
	store        AuditStoreWriter
	archive      AuditArchive
	types        DeclaredAuditEventTypeReader
	deploymentID string
	batchSize    int
	maxWait      time.Duration
	batchTimeout time.Duration
	now          func() time.Time
	newBatchID   func() string

	// drainMu serializes drains, which share rows and archiving.
	drainMu sync.Mutex
	// rows is what the relay knows of each queued row it has handled, by
	// sequence number. A row leaves it when the queue no longer returns it.
	rows map[int64]*rowState
	// archiving is the archive write that may be in flight or unacknowledged.
	archiving archiveAttempt

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewAuditRelay validates cfg and builds a relay.
func NewAuditRelay(cfg AuditRelayConfig) (*AuditRelay, error) {
	if cfg.Queue == nil || cfg.Store == nil || cfg.Archive == nil {
		return nil, errors.New("audit relay: queue, store and archive are required")
	}
	if strings.TrimSpace(cfg.DeploymentID) == "" {
		return nil, errors.New("audit relay: deployment id is required")
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > MaxAuditRelayBatchSize {
		return nil, fmt.Errorf("audit relay: batch size must be between 1 and %d", MaxAuditRelayBatchSize)
	}
	if cfg.MaxWait <= 0 {
		return nil, errors.New("audit relay: max wait must be positive")
	}
	relay := &AuditRelay{
		queue:        cfg.Queue,
		store:        cfg.Store,
		archive:      cfg.Archive,
		types:        cfg.Types,
		deploymentID: cfg.DeploymentID,
		batchSize:    cfg.BatchSize,
		maxWait:      cfg.MaxWait,
		batchTimeout: auditRelayBatchTimeout,
		now:          cfg.Now,
		newBatchID:   cfg.NewBatchID,
		rows:         map[int64]*rowState{},
	}
	if relay.now == nil {
		relay.now = time.Now
	}
	if relay.newBatchID == nil {
		relay.newBatchID = func() string { return uuid.NewString() }
	}
	return relay, nil
}

// DrainOnce delivers every batch that is due — each full batch, then a partial
// one whose oldest event has waited MaxWait — and returns how many events it
// delivered. Each batch has a deadline of its own. It stops at the first batch
// that leaves rows queued, which stay queued, in order, for the next attempt;
// the events it delivered before that, and in that batch, are counted.
func (r *AuditRelay) DrainOnce(ctx context.Context) (int, error) {
	pass, err := r.drain(ctx, nil)
	return pass.Delivered, err
}

// drain is DrainOnce with a way to end the pass between batches: stopped is
// asked before each batch, and a pass it ends is not a failure.
func (r *AuditRelay) drain(ctx context.Context, stopped func() bool) (AuditDrainResult, error) {
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	var pass AuditDrainResult
	for {
		if stopped != nil && stopped() {
			return pass, nil
		}
		batch, cancel := context.WithTimeout(ctx, r.batchTimeout)
		result, err := r.queue.Drain(batch, r.batchSize, r.deliver)
		cancel()
		pass.Delivered += result.Delivered
		pass.Quarantined += result.Quarantined
		if err != nil {
			return pass, err
		}
		// Fewer than a full batch means the queue held no more that was due:
		// nothing, a partial batch still waiting, a partial batch now delivered,
		// or a lease held by another relay.
		if result.Removed() < r.batchSize {
			return pass, nil
		}
	}
}

// rowState is what the relay remembers about one queued row.
type rowState struct {
	record   AuditRecord
	composed bool
	// archive is the archive object that holds the row, once one acknowledged.
	archive  *archiveRef
	appended bool
	// refused is why the row was set aside; empty until it is.
	refused string
}

// archiveRef names the archive object a row was acknowledged in.
type archiveRef struct {
	id         string
	composedAt time.Time
}

// archiveAttempt is the batch the relay last tried to archive and had not
// acknowledged, so a retry of exactly the same rows keeps its name and any
// other set of rows takes a new one.
type archiveAttempt struct {
	key   string
	batch AuditBatch
}

// queuedRow pairs a row the queue handed over with what the relay remembers.
type queuedRow struct {
	event QueuedAuditEvent
	state *rowState
}

// rowsKey identifies a run of queued rows by their sequence numbers.
func rowsKey(rows []*queuedRow) string {
	var key strings.Builder
	for _, row := range rows {
		key.WriteString(strconv.FormatInt(row.event.Seq, 36))
		key.WriteByte(',')
	}
	return key.String()
}

// rowsOf returns the relay's record of each event, forgetting every row the
// queue did not hand over: a row not returned is one already removed.
func (r *AuditRelay) rowsOf(events []QueuedAuditEvent) []*queuedRow {
	current := make(map[int64]*rowState, len(events))
	rows := make([]*queuedRow, len(events))
	for i, event := range events {
		state := r.rows[event.Seq]
		if state == nil {
			state = &rowState{}
		}
		current[event.Seq] = state
		rows[i] = &queuedRow{event: event, state: state}
	}
	r.rows = current
	return rows
}

// deliver is the relay's half of one Drain: decide whether the events are due,
// compose them, write them to the archive and then the store — each only for
// the rows that have not already had it acknowledged — and report which rows
// were delivered, which were set aside, and why any other stays queued.
func (r *AuditRelay) deliver(ctx context.Context, events []QueuedAuditEvent) (AuditDeliveryOutcome, error) {
	rows := r.rowsOf(events)
	if len(events) == 0 {
		return AuditDeliveryOutcome{}, nil
	}
	if len(events) < r.batchSize && r.now().Sub(oldestEnqueued(events)) < r.maxWait {
		return AuditDeliveryOutcome{}, nil
	}
	if err := r.compose(ctx, rows); err != nil {
		return settle(rows, err), nil
	}
	if err := r.archiveRows(ctx, rows); err != nil {
		return settle(rows, err), nil
	}
	return settle(rows, r.appendRows(ctx, rows)), nil
}

// settle reports what is decided about rows: every row acknowledged by both
// sides is delivered, every row judged undeliverable is set aside, and the
// rest stay queued for the reason in pending.
func settle(rows []*queuedRow, pending error) AuditDeliveryOutcome {
	var outcome AuditDeliveryOutcome
	for _, row := range rows {
		switch {
		case row.state.appended:
			outcome.Delivered = append(outcome.Delivered, row.event.Seq)
		case row.state.refused != "":
			outcome.Quarantined = append(outcome.Quarantined, QuarantinedAuditEvent{Seq: row.event.Seq, Reason: row.state.refused})
		}
	}
	if len(outcome.Delivered)+len(outcome.Quarantined) < len(rows) {
		outcome.Pending = pending
		if outcome.Pending == nil {
			outcome.Pending = errors.New("audit relay: rows remain queued")
		}
	}
	return outcome
}

func oldestEnqueued(events []QueuedAuditEvent) time.Time {
	oldest := events[0].EnqueuedAt
	for _, event := range events[1:] {
		if event.EnqueuedAt.Before(oldest) {
			oldest = event.EnqueuedAt
		}
	}
	return oldest
}

// compose classifies and hashes each row it has not composed. A registry that
// cannot be read is an error, retried with the batch — never mistaken for an
// unregistered type. A row whose own details cannot be serialized is set aside,
// but only when another row composed: with none composing, nothing shows that
// the rows, not the relay, are at fault.
func (r *AuditRelay) compose(ctx context.Context, rows []*queuedRow) error {
	resolver := NewAuditEventResolver(r.types)
	composed := false
	var unreadable []*queuedRow
	var first error
	for _, row := range rows {
		state := row.state
		if state.refused != "" {
			continue
		}
		if state.composed {
			composed = true
			continue
		}
		resolved, err := resolver.Resolve(ctx, row.event.Entry.EventType)
		if err != nil {
			return err
		}
		record, err := NewAuditRecord(row.event.Entry, resolved.RetentionClass())
		if err != nil {
			if first == nil {
				first = err
			}
			unreadable = append(unreadable, row)
			continue
		}
		state.record, state.composed = record, true
		composed = true
	}
	if len(unreadable) == 0 {
		return nil
	}
	if !composed {
		return first
	}
	for _, row := range unreadable {
		r.setAside(ctx, row, first)
	}
	return nil
}

// archiveRows writes the composed rows the archive has not acknowledged as one
// object, whole. The same rows retried keep the batch's name; any other set
// takes a new one.
func (r *AuditRelay) archiveRows(ctx context.Context, rows []*queuedRow) error {
	var todo []*queuedRow
	for _, row := range rows {
		if row.state.composed && row.state.archive == nil && row.state.refused == "" {
			todo = append(todo, row)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	if key := rowsKey(todo); r.archiving.key != key {
		batch := AuditBatch{
			ID:           r.newBatchID(),
			DeploymentID: r.deploymentID,
			ComposedAt:   r.now().UTC(),
			Records:      make([]AuditRecord, len(todo)),
		}
		for i, row := range todo {
			batch.Records[i] = row.state.record
		}
		r.archiving = archiveAttempt{key: key, batch: batch}
	}
	batch := r.archiving.batch
	if err := r.archive.WriteAuditBatch(ctx, batch); err != nil {
		return fmt.Errorf("audit relay: archive batch %s: %w", batch.ID, err)
	}
	ref := &archiveRef{id: batch.ID, composedAt: batch.ComposedAt}
	for _, row := range todo {
		row.state.archive = ref
	}
	r.archiving = archiveAttempt{}
	return nil
}

// appendRows writes the archived rows the store has not acknowledged. The
// whole run goes first; when the store refuses it, the run is split and the
// halves retried, so a row the store cannot take does not hold the rest back.
func (r *AuditRelay) appendRows(ctx context.Context, rows []*queuedRow) error {
	var todo []*queuedRow
	for _, row := range rows {
		if row.state.archive != nil && !row.state.appended && row.state.refused == "" {
			todo = append(todo, row)
		}
	}
	if len(todo) == 0 {
		return nil
	}

	writes := 0
	write := func(run []*queuedRow) error {
		writes++
		first := run[0].state.archive
		batch := AuditBatch{ID: first.id, DeploymentID: r.deploymentID, ComposedAt: first.composedAt, Records: make([]AuditRecord, len(run))}
		for i, row := range run {
			batch.Records[i] = row.state.record
		}
		if err := r.store.AppendAuditBatch(ctx, batch); err != nil {
			return err
		}
		for _, row := range run {
			row.state.appended = true
		}
		return nil
	}

	err := write(todo)
	if err == nil {
		return nil
	}
	failure := fmt.Errorf("audit relay: append %d rows of batch %s: %w", len(todo), todo[0].state.archive.id, err)
	if len(todo) == 1 {
		return failure
	}

	var alone []*queuedRow
	accepted := 0
	// Search breadth-first, so the first accepted run turns up within a write or
	// two when only one row is bad, and an outage ends after a bounded number of
	// writes instead of after one per row.
	failed := [][]*queuedRow{todo}
search:
	for len(failed) > 0 {
		var next [][]*queuedRow
		for _, run := range failed {
			middle := len(run) / 2
			for _, half := range [][]*queuedRow{run[:middle], run[middle:]} {
				if ctx.Err() != nil || writes >= auditRelayMaxSplitWrites || (accepted == 0 && writes >= auditRelayProbeWrites) {
					break search
				}
				switch err := write(half); {
				case err == nil:
					accepted++
				case len(half) == 1:
					alone = append(alone, half[0])
				default:
					next = append(next, half)
				}
			}
		}
		failed = next
	}

	// A row is set aside only on this chain of evidence: the store refused it
	// alone; afterwards the store took a row it had already acknowledged, again
	// (so it was reachable after the refusal, and a duplicate is something every
	// read already discards); and it refuses the row alone once more. Without a
	// row to write as that witness nothing is set aside.
	if witness := lastAppended(rows); len(alone) > 0 && witness != nil && ctx.Err() == nil {
		if write([]*queuedRow{witness}) == nil {
			for _, refused := range alone {
				if ctx.Err() != nil {
					break
				}
				if err := write([]*queuedRow{refused}); err != nil && ctx.Err() == nil {
					r.setAside(ctx, refused, err)
				}
			}
		}
	}

	unresolved := 0
	for _, row := range todo {
		if !row.state.appended && row.state.refused == "" {
			unresolved++
		}
	}
	if unresolved == 0 {
		return nil
	}
	return failure
}

// lastAppended is the last row the store has acknowledged, nil when none has.
func lastAppended(rows []*queuedRow) *queuedRow {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].state.appended {
			return rows[i]
		}
	}
	return nil
}

// setAside records the decision that row cannot be delivered. The decision is
// kept until the row leaves the queue, so a lost commit does not turn it back
// into doubt.
func (r *AuditRelay) setAside(ctx context.Context, row *queuedRow, cause error) {
	reason := cause.Error()
	if len(reason) > auditRelayMaxReasonLength {
		reason = reason[:auditRelayMaxReasonLength]
	}
	row.state.refused = reason
	wool.Get(ctx).In("audit.relay").Error("audit event set aside: the store refused it while accepting others; it stays in the quarantine and is not delivered",
		wool.Field("event_id", row.event.Entry.ID), wool.Field("event_type", string(row.event.Entry.EventType)),
		wool.Field("queue_seq", strconv.FormatInt(row.event.Seq, 10)), wool.ErrField(cause))
}

// pollInterval is how often the loop looks for due events: a fifth of MaxWait,
// so a partial batch is delivered close to its deadline, within [100ms, 1s].
func (r *AuditRelay) pollInterval() time.Duration {
	interval := r.maxWait / 5
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > time.Second {
		interval = time.Second
	}
	return interval
}

// Start runs the relay loop until Shutdown. A failed delivery is logged and
// retried with a backoff that doubles up to a minute and starts over whenever a
// pass made progress; the rows it could not deliver stay queued. Start is
// idempotent.
func (r *AuditRelay) Start(parent context.Context) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()

	go func() {
		defer close(done)
		w := wool.Get(ctx).In("audit.relay")
		var backoff time.Duration
		for {
			// A delivery in flight is not cancelled by Shutdown: cancelling it
			// between the store write and the queue delete is exactly what turns
			// one delivery into two. Each batch is bounded by its own timeout
			// instead, and Shutdown ends the pass between batches.
			pass, err := r.drain(context.WithoutCancel(ctx), func() bool { return ctx.Err() != nil })
			backoff = relayBackoffAfter(backoff, pass.Removed(), err)
			wait := r.pollInterval()
			if err != nil {
				wait = backoff
				w.Warn("audit relay delivery failed; the rows stay queued and are retried",
					wool.Field("retry_in", backoff.String()), wool.ErrField(err))
			}
			if pass.Quarantined > 0 {
				w.Error("audit relay set rows aside in the quarantine",
					wool.Field("rows", strconv.Itoa(pass.Quarantined)))
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

// relayBackoffAfter is the wait before the next pass: none after a pass that
// succeeded, one doubling further after a failure that made no progress, and
// the first step again after a failure that did — a relay moving rows is not a
// relay that is stuck, and should not be slowed as if it were.
func relayBackoffAfter(previous time.Duration, progress int, err error) time.Duration {
	switch {
	case err == nil:
		return 0
	case progress > 0:
		return nextAuditRelayBackoff(0)
	default:
		return nextAuditRelayBackoff(previous)
	}
}

func nextAuditRelayBackoff(previous time.Duration) time.Duration {
	if previous <= 0 {
		return time.Second
	}
	if next := previous * 2; next < auditRelayMaxBackoff {
		return next
	}
	return auditRelayMaxBackoff
}

// Shutdown stops the loop and waits for a delivery in flight to finish, or for
// the caller's deadline. Events not yet delivered stay queued for the next start.
func (r *AuditRelay) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	cancel, done := r.cancel, r.done
	r.mu.Unlock()

	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
