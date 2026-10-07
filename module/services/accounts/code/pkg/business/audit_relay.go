package business

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	// Quarantined are the rows the relay set aside because they can never be
	// delivered: the store reported a permanent refusal of the row
	// (PermanentRowRejection), or its details cannot be serialized. The queue
	// moves them, whole, into the quarantine; it never deletes them.
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
	// auditRelayMaxAppendWrites bounds the store writes one batch spends setting
	// aside the rows the store reports it refused for good: each write that
	// reports refusals removes at least one row, and a store that reports every
	// refused row of a write needs a few. A batch that still holds rows after
	// that stays queued and continues on the next pass.
	auditRelayMaxAppendWrites = 64
	// auditRelayMaxReasonLength bounds, in bytes, the error text kept with a
	// quarantined row.
	auditRelayMaxReasonLength = 1000
)

// The class a quarantined row's reason starts with, as "<class>: <cause>", so a
// responder can tell what the quarantine holds without reading the cause.
const (
	// QuarantineWarehouseRejectedArchived is a row the store refused for good. It
	// was written to the archive, whole, before the store was tried: the archive
	// holds a copy of it, and the quarantine holds another.
	QuarantineWarehouseRejectedArchived = "warehouse_rejected_archived"
	// QuarantineUnserializableNotArchived is a row whose details cannot be
	// serialized. The archive records each event with the SHA-256 of its
	// canonical details, which such a row does not have, so it was never
	// archived: the quarantine holds the only copy, payload included.
	QuarantineUnserializableNotArchived = "unserializable_not_archived"
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
// different rows. Only the store write is repeated without the rows the store
// refused for good; it keys each record by event id and has no use for a
// batch's name.
//
// A row the store refuses for good must not hold the rest of the queue behind
// it, and a store that is merely failing must not cost a row its place. The
// relay sets a row aside — moves it whole to the quarantine, never deletes it —
// only when the store itself reports that it refused that row for the row's own
// content (a PermanentRowRejection naming the event). Every other failure is
// retried with backoff and sets nothing aside: a transport error, a server
// error, a throttle, a quota, a timeout, a failed quorum, and any error the
// store did not classify. The relay never infers a refusal from what else a
// store accepted, or from how often a row failed. When a write reports
// refusals, the relay sets those rows aside and writes the rest again, so a
// batch of one is judged exactly as a batch of five thousand.
//
// One refusal is not taken at its word: a write of two or more rows that the
// store refuses in every row, for one and the same reason, is not a set of bad
// rows but a store that cannot take them (a table changed under the relay).
// Setting them all aside would quarantine the whole stream and leave reads with
// gaps nobody was told of, so nothing is set aside: the rows stay queued, the
// delivery fails with the reason, and the relay retries with backoff while the
// queue's depth and age grow until the relay-lag alert fires
// (RefusedForOneReason). A refusal that singles out some of the rows of a write
// is still a refusal of those rows.
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
// unregistered type. A row whose own details cannot be serialized is set aside
// with its own error: serializing is a function of the row alone, so the same
// row fails the same way every time, whatever else is queued with it. It cannot
// be archived either, since the archive's line for an event carries the hash of
// its canonical details; the quarantine holds its only copy, and its reason says
// so (QuarantineUnserializableNotArchived).
func (r *AuditRelay) compose(ctx context.Context, rows []*queuedRow) error {
	resolver := NewAuditEventResolver(r.types)
	for _, row := range rows {
		state := row.state
		if state.refused != "" || state.composed {
			continue
		}
		resolved, err := resolver.Resolve(ctx, row.event.Entry.EventType)
		if err != nil {
			return err
		}
		record, err := NewAuditRecord(row.event.Entry, resolved.RetentionClass())
		if err != nil {
			r.setAside(ctx, row, QuarantineUnserializableNotArchived, err)
			continue
		}
		state.record, state.composed = record, true
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

// awaitingAppend is the archived rows the store has neither acknowledged nor
// refused for good.
func awaitingAppend(rows []*queuedRow) []*queuedRow {
	var todo []*queuedRow
	for _, row := range rows {
		if row.state.archive != nil && !row.state.appended && row.state.refused == "" {
			todo = append(todo, row)
		}
	}
	return todo
}

// appendRows writes the archived rows the store has not acknowledged, whole.
// When the store reports rows it refused for good, those are set aside and the
// rest is written again; when it fails any other way — or refuses every row of
// the write for one reason, which is the store's and not the rows' — nothing is
// set aside and the rows stay queued for the next attempt. Rows an earlier write
// of this call may have stored are written again: the store keys a record by
// event id.
func (r *AuditRelay) appendRows(ctx context.Context, rows []*queuedRow) error {
	var failure error
	for writes := 0; writes < auditRelayMaxAppendWrites; writes++ {
		todo := awaitingAppend(rows)
		if len(todo) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("audit relay: append %d rows of batch %s: %w", len(todo), todo[0].state.archive.id, err)
		}
		first := todo[0].state.archive
		batch := AuditBatch{ID: first.id, DeploymentID: r.deploymentID, ComposedAt: first.composedAt, Records: make([]AuditRecord, len(todo))}
		for i, row := range todo {
			batch.Records[i] = row.state.record
		}
		err := r.store.AppendAuditBatch(ctx, batch)
		if err == nil {
			for _, row := range todo {
				row.state.appended = true
			}
			return nil
		}
		failure = fmt.Errorf("audit relay: append %d rows of batch %s: %w", len(todo), first.id, err)
		if reason, whole := RefusedForOneReason(err, eventIDsOf(todo)); whole {
			return r.storeRefusesEveryRow(ctx, todo, first.id, reason)
		}
		if !r.setAsideRefused(ctx, todo, err) {
			return failure
		}
	}
	if len(awaitingAppend(rows)) == 0 {
		return nil
	}
	return failure
}

// storeRefusesEveryRow is the failure of a write the store refused in every row
// for one reason. It sets nothing aside: the reason is the store's, so the rows
// are as deliverable as they were, and quarantining them would only move a
// stream the store cannot take into a table nobody reads. The rows stay queued,
// the error says why, and the loop's failure log, its backoff and the growing
// depth and age of the queue are the signals.
func (r *AuditRelay) storeRefusesEveryRow(ctx context.Context, todo []*queuedRow, batchID, reason string) error {
	reason = AuditQuarantineReason(reason)
	wool.Get(ctx).In("audit.relay").Error("audit store refused every row of a write for one reason: treated as a store failure, nothing set aside; the rows stay queued and are retried",
		wool.Field("rows", strconv.Itoa(len(todo))), wool.Field("batch_id", batchID), wool.Field("reason", reason))
	return fmt.Errorf("audit relay: append %d rows of batch %s: the store refused every row for one reason, so the rows are kept queued and not set aside: %s", len(todo), batchID, reason)
}

// eventIDsOf is the event id of each of rows.
func eventIDsOf(rows []*queuedRow) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.event.Entry.ID
	}
	return ids
}

// setAsideRefused sets aside every row of todo that err says the store refused
// for good, and reports whether it set any aside. A refusal that names an event
// that is not among todo is not evidence about any of them.
func (r *AuditRelay) setAsideRefused(ctx context.Context, todo []*queuedRow, err error) bool {
	byEvent := make(map[string][]*queuedRow, len(todo))
	for _, row := range todo {
		byEvent[row.event.Entry.ID] = append(byEvent[row.event.Entry.ID], row)
	}
	set := false
	for _, rejection := range PermanentRowRejections(err) {
		for _, row := range byEvent[rejection.EventID] {
			r.setAside(ctx, row, QuarantineWarehouseRejectedArchived, rejection)
			set = true
		}
	}
	return set
}

// AuditQuarantineReason is text as the quarantine keeps it: valid UTF-8 with no
// NUL, because that is all PostgreSQL stores in a text column and a store's
// error can hold anything; and at most 1000 bytes, cut between characters. An
// invalid sequence or a NUL becomes U+FFFD.
func AuditQuarantineReason(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	text = strings.ReplaceAll(text, "\x00", "\uFFFD")
	if len(text) <= auditRelayMaxReasonLength {
		return text
	}
	cut := auditRelayMaxReasonLength
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// setAside records the decision that row cannot be delivered, with its class
// and the error that decided it. The decision is kept until the row leaves the
// queue, so a lost commit does not turn it back into doubt.
func (r *AuditRelay) setAside(ctx context.Context, row *queuedRow, class string, cause error) {
	row.state.refused = AuditQuarantineReason(class + ": " + cause.Error())
	wool.Get(ctx).In("audit.relay").Error("audit event set aside: it cannot be delivered now or later; it stays in the quarantine",
		wool.Field("event_id", row.event.Entry.ID), wool.Field("event_type", string(row.event.Entry.EventType)),
		wool.Field("queue_seq", strconv.FormatInt(row.event.Seq, 10)), wool.Field("reason_class", class), wool.ErrField(cause))
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
