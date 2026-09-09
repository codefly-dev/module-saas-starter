package infra

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/codefly-dev/core/wool"
)

// eventRelayPollInterval is the idle cadence: how long the relay waits after a
// fully-drained tick before scanning domain_events again. A publish that runs
// its own inline drain (the no-caller-transaction path) delivers immediately;
// this loop is the catch-up path for events written inside a producer
// transaction, which become visible only after that transaction commits.
const eventRelayPollInterval = 250 * time.Millisecond

// EventRelayDrainer is the relay's single dependency: drain every currently
// unpublished domain event and report how many were relayed. PostgresEventTransport
// satisfies it via RelayOnce.
type EventRelayDrainer interface {
	RelayOnce(ctx context.Context) (int, error)
}

// EventRelayWorker is the transactional-outbox pump for domain events. On a tick
// it drains domain_events: for every unpublished event it enqueues one inbox job
// per matching non-revoked subscription and marks the event published. It owns no
// queue of its own — the reserved events.relay name marks the workload — and holds
// no lease; concurrency safety comes from the FOR UPDATE SKIP LOCKED scan inside
// RelayOnce, so running more than one relay is safe and never double-delivers.
type EventRelayWorker struct {
	drainer  EventRelayDrainer
	interval time.Duration

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewEventRelayWorker builds a relay pump over the given transport. A
// non-positive interval falls back to the default idle cadence.
func NewEventRelayWorker(drainer EventRelayDrainer, interval time.Duration) *EventRelayWorker {
	if interval <= 0 {
		interval = eventRelayPollInterval
	}
	return &EventRelayWorker{drainer: drainer, interval: interval}
}

// Start launches the relay loop. It drains once immediately so a backlog left by
// a previous process is cleared without waiting a full interval, then ticks.
// Start is idempotent; a second call while running is a no-op.
func (w *EventRelayWorker) Start(parent context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go func() {
		defer close(done)
		drain := func() {
			// A tick drains until empty; a failure is logged and retried next tick,
			// because unpublished rows stay unpublished when a batch rolls back.
			if _, err := w.drainer.RelayOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				wool.Get(ctx).In("events.relay").Warn("relay iteration failed", wool.ErrField(err))
			}
		}
		drain()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				drain()
			}
		}
	}()
}

// Shutdown stops the loop and waits for the in-flight drain to finish, or until
// the caller's deadline. Events not yet relayed stay unpublished and are picked
// up when the relay next starts.
func (w *EventRelayWorker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	cancel, done := w.cancel, w.done
	w.mu.Unlock()

	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
