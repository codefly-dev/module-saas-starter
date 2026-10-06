package business

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/codefly-dev/core/wool"
	"go.opentelemetry.io/otel/metric"
)

// AuditQueueSnapshot is the payload-free state of the transactional queue.
type AuditQueueSnapshot struct {
	Depth            int64
	OldestEnqueuedAt *time.Time
	// Quarantined is how many rows the relay has set aside, and which nothing
	// deletes: it only grows until an operator deals with them.
	Quarantined int64
}

// AuditQueueMetricsSource reads the queue without taking the relay lease.
type AuditQueueMetricsSource interface {
	Snapshot(context.Context) (AuditQueueSnapshot, error)
}

// AuditRelayMonitor reports queue depth, the age of the oldest row and the size
// of the quarantine. A failed relay leaves its rows queued, so the age keeps
// rising until delivery succeeds; zero is reported for an empty queue.
//
// Each series is reported only while it is known. When the latest read of the
// queue failed, or the latest successful one is more than twice the interval
// old, depth, age and quarantine are absent rather than frozen at their last
// value: a gauge that kept "0" through a dead database or a dead monitor would
// keep the lag alert green while the relay was down. The failures are counted.
type AuditRelayMonitor struct {
	source         AuditQueueMetricsSource
	interval       time.Duration
	now            func() time.Time
	snapshotErrors metric.Int64Counter
	depth          metric.Int64ObservableGauge
	age            metric.Float64ObservableGauge
	quarantined    metric.Int64ObservableGauge
	registration   metric.Registration

	mu       sync.Mutex
	observed *observedQueue // nil until the first read, and after a failed one
	cancel   context.CancelFunc
	done     chan struct{}
}

// observedQueue is the latest successful read of the queue and when it was made.
type observedQueue struct {
	snapshot AuditQueueSnapshot
	at       time.Time
}

func NewAuditRelayMonitor(source AuditQueueMetricsSource, meter metric.Meter, interval time.Duration) (*AuditRelayMonitor, error) {
	if source == nil || meter == nil {
		return nil, errors.New("audit relay metrics: source and meter are required")
	}
	if interval <= 0 {
		return nil, errors.New("audit relay metrics: interval must be positive")
	}
	m := &AuditRelayMonitor{source: source, interval: interval, now: time.Now}
	var err error
	if m.depth, err = meter.Int64ObservableGauge("saas.audit_queue.depth",
		metric.WithDescription("Queued audit events awaiting archive and warehouse delivery. Absent while the queue cannot be read.")); err != nil {
		return nil, err
	}
	if m.age, err = meter.Float64ObservableGauge("saas.audit_queue.oldest_age", metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest queued audit event. Absent while the queue cannot be read.")); err != nil {
		return nil, err
	}
	if m.quarantined, err = meter.Int64ObservableGauge("saas.audit_queue.quarantined",
		metric.WithDescription("Audit events the relay set aside because the store refused them for good or their details cannot be serialized. Nothing deletes them. Absent while the queue cannot be read.")); err != nil {
		return nil, err
	}
	if m.snapshotErrors, err = meter.Int64Counter("saas.audit_queue.snapshot_errors",
		metric.WithDescription("Reads of the audit queue's depth and age that failed.")); err != nil {
		return nil, err
	}
	if m.registration, err = meter.RegisterCallback(m.observe, m.depth, m.age, m.quarantined); err != nil {
		return nil, err
	}
	return m, nil
}

// observe reports the latest reading while it is current.
func (m *AuditRelayMonitor) observe(_ context.Context, observer metric.Observer) error {
	m.mu.Lock()
	observed := m.observed
	m.mu.Unlock()
	if observed == nil {
		return nil
	}
	now := m.now()
	if now.Sub(observed.at) > 2*m.interval {
		return nil
	}
	age := 0.0
	if observed.snapshot.OldestEnqueuedAt != nil {
		age = max(0, now.Sub(*observed.snapshot.OldestEnqueuedAt).Seconds())
	}
	observer.ObserveInt64(m.depth, observed.snapshot.Depth)
	observer.ObserveFloat64(m.age, age)
	observer.ObserveInt64(m.quarantined, observed.snapshot.Quarantined)
	return nil
}

func (m *AuditRelayMonitor) RunOnce(ctx context.Context) error {
	snapshot, err := m.source.Snapshot(ctx)
	if err != nil {
		err = fmt.Errorf("audit relay metrics: read queue: %w", err)
	} else if snapshot.Depth < 0 || snapshot.Quarantined < 0 || (snapshot.Depth == 0 && snapshot.OldestEnqueuedAt != nil) ||
		(snapshot.Depth > 0 && snapshot.OldestEnqueuedAt == nil) {
		err = errors.New("audit relay metrics: inconsistent queue snapshot")
	}
	m.mu.Lock()
	if err != nil {
		m.observed = nil
	} else {
		m.observed = &observedQueue{snapshot: snapshot, at: m.now()}
	}
	m.mu.Unlock()
	if err != nil {
		m.snapshotErrors.Add(ctx, 1)
	}
	return err
}

// StrandedAuditQueue reports how many events sit in the audit queue of a
// deployment whose sink does not drain it. Under postgres or both nothing
// writes the queue and nothing reads it, so a row there was queued while a
// warehouse sink was configured and is delivered only when a warehouse sink is
// restored: the sink was switched back with events still waiting. Under a
// warehouse sink the relay drains the queue and nothing is stranded.
func StrandedAuditQueue(ctx context.Context, mode AuditSinkMode, source AuditQueueMetricsSource) (int64, error) {
	if mode.Swaps() {
		return 0, nil
	}
	snapshot, err := source.Snapshot(ctx)
	if err != nil {
		return 0, fmt.Errorf("audit relay metrics: read queue: %w", err)
	}
	return snapshot.Depth, nil
}

func (m *AuditRelayMonitor) Start(parent context.Context) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()

	go func() {
		defer close(done)
		run := func() {
			attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := m.RunOnce(attempt); err != nil && !errors.Is(err, context.Canceled) {
				wool.Get(ctx).In("audit.relay.metrics").Warn("queue observation failed", wool.ErrField(err))
			}
		}
		run()
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func (m *AuditRelayMonitor) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel == nil {
		return m.registration.Unregister()
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return m.registration.Unregister()
}
