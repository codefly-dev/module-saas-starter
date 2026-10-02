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

// AuditQueue is the relay's side of the transactional queue.
type AuditQueue interface {
	// Drain hands deliver the oldest queued events whose transactions have
	// committed — at most limit, in queue order — while holding a lease no other
	// relay holds at the same time. When deliver reports true it deletes exactly
	// those rows and returns how many; when deliver reports false or an error it
	// deletes nothing. A relay that finds the lease held elsewhere returns 0 and
	// no error without calling deliver.
	//
	// The queue never reports an event before every event queued ahead of it has
	// committed or rolled back, so a batch cannot overtake an earlier event of
	// the same organization that was still in flight.
	Drain(ctx context.Context, limit int, deliver func(ctx context.Context, events []QueuedAuditEvent) (bool, error)) (int, error)
}

// Relay defaults. A batch of 500 is BigQuery's recommended streaming request
// size; five seconds bounds how long a quiet deployment's events wait.
const (
	DefaultAuditRelayBatchSize = 500
	MaxAuditRelayBatchSize     = 5000
	DefaultAuditRelayMaxWait   = 5 * time.Second
	// auditRelayAttemptTimeout bounds one delivery, which holds a queue
	// transaction open across the archive and warehouse writes.
	auditRelayAttemptTimeout = 2 * time.Minute
	auditRelayMaxBackoff     = time.Minute
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
// that delete leaves the rows queued and the batch is delivered again — the
// store keeps one record per event id, and the archive's readers deduplicate.
//
// The archive is written first so that a failure between the two writes
// leaves its duplicate in the archive, where readers already deduplicate, and
// not in the store.
//
// While it runs, the relay remembers which writes the batch at the head of the
// queue has already had acknowledged, so retrying it during an outage of one
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
	now          func() time.Time
	newBatchID   func() string

	// drainMu serializes drains, which share head.
	drainMu sync.Mutex
	head    batchProgress

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
		now:          cfg.Now,
		newBatchID:   cfg.NewBatchID,
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
// delivered. It stops at the first failure, which leaves the failed batch and
// everything behind it queued, in order, for the next attempt.
func (r *AuditRelay) DrainOnce(ctx context.Context) (int, error) {
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	delivered := 0
	for {
		n, err := r.queue.Drain(ctx, r.batchSize, r.deliver)
		delivered += n
		if err != nil {
			return delivered, err
		}
		if n > 0 {
			// Its rows are gone: nothing about the head batch is worth remembering.
			r.head = batchProgress{}
		}
		// Fewer than a full batch means the queue held no more that was due:
		// nothing, a partial batch still waiting, a partial batch now delivered,
		// or a lease held by another relay.
		if n < r.batchSize {
			return delivered, nil
		}
	}
}

// batchProgress is what the relay knows about the batch at the head of the
// queue: which queued rows it holds, and which of the two writes acknowledged it.
type batchProgress struct {
	rows     string
	batch    AuditBatch
	archived bool
	appended bool
}

// rowsKey identifies a run of queued rows by their sequence numbers.
func rowsKey(events []QueuedAuditEvent) string {
	var key strings.Builder
	for _, event := range events {
		key.WriteString(strconv.FormatInt(event.Seq, 36))
		key.WriteByte(',')
	}
	return key.String()
}

// deliver is the relay's half of one Drain: decide whether the events are due,
// compose them into a batch, and write it to the archive and then the store —
// each only if it has not already acknowledged these exact rows.
func (r *AuditRelay) deliver(ctx context.Context, events []QueuedAuditEvent) (bool, error) {
	if len(events) == 0 {
		return false, nil
	}
	if len(events) < r.batchSize && r.now().Sub(oldestEnqueued(events)) < r.maxWait {
		return false, nil
	}
	if key := rowsKey(events); r.head.rows != key {
		batch, err := r.compose(ctx, events)
		if err != nil {
			return false, err
		}
		r.head = batchProgress{rows: key, batch: batch}
	}
	batch := r.head.batch
	if !r.head.archived {
		if err := r.archive.WriteAuditBatch(ctx, batch); err != nil {
			return false, fmt.Errorf("audit relay: archive batch %s: %w", batch.ID, err)
		}
		r.head.archived = true
	}
	if !r.head.appended {
		if err := r.store.AppendAuditBatch(ctx, batch); err != nil {
			return false, fmt.Errorf("audit relay: append batch %s: %w", batch.ID, err)
		}
		r.head.appended = true
	}
	return true, nil
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

// compose classifies and hashes each event. A registry that cannot be read is
// an error, retried with the batch — never mistaken for an unregistered type.
func (r *AuditRelay) compose(ctx context.Context, events []QueuedAuditEvent) (AuditBatch, error) {
	resolver := NewAuditEventResolver(r.types)
	batch := AuditBatch{
		ID:           r.newBatchID(),
		DeploymentID: r.deploymentID,
		ComposedAt:   r.now().UTC(),
		Records:      make([]AuditRecord, 0, len(events)),
	}
	for _, event := range events {
		resolved, err := resolver.Resolve(ctx, event.Entry.EventType)
		if err != nil {
			return AuditBatch{}, err
		}
		record, err := NewAuditRecord(event.Entry, resolved.RetentionClass())
		if err != nil {
			return AuditBatch{}, err
		}
		batch.Records = append(batch.Records, record)
	}
	return batch, nil
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
// retried with a backoff that doubles up to a minute; the failed batch stays
// queued, so nothing behind it is delivered out of order. Start is idempotent.
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
			// one delivery into two. It is bounded by its own timeout instead.
			attempt, cancelAttempt := context.WithTimeout(context.WithoutCancel(ctx), auditRelayAttemptTimeout)
			_, err := r.DrainOnce(attempt)
			cancelAttempt()
			wait := r.pollInterval()
			if err != nil {
				backoff = nextAuditRelayBackoff(backoff)
				wait = backoff
				w.Warn("audit relay delivery failed; the batch stays queued and is retried",
					wool.Field("retry_in", backoff.String()), wool.ErrField(err))
			} else {
				backoff = 0
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
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
