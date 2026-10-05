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
	// deletes.
	Quarantined int64
}

// AuditQueueMetricsSource reads the queue without taking the relay lease.
type AuditQueueMetricsSource interface {
	Snapshot(context.Context) (AuditQueueSnapshot, error)
}

// AuditRelayMonitor records queue depth and the age of the oldest row. A
// failed relay leaves its rows queued, so the age keeps rising until delivery
// succeeds; zero is recorded for an empty queue.
type AuditRelayMonitor struct {
	source   AuditQueueMetricsSource
	depth    metric.Int64Gauge
	age      metric.Float64Gauge
	interval time.Duration
	now      func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewAuditRelayMonitor(source AuditQueueMetricsSource, meter metric.Meter, interval time.Duration) (*AuditRelayMonitor, error) {
	if source == nil || meter == nil {
		return nil, errors.New("audit relay metrics: source and meter are required")
	}
	if interval <= 0 {
		return nil, errors.New("audit relay metrics: interval must be positive")
	}
	depth, err := meter.Int64Gauge("saas.audit_queue.depth", metric.WithDescription("Queued audit events awaiting archive and warehouse delivery."))
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64Gauge("saas.audit_queue.oldest_age", metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest queued audit event."))
	if err != nil {
		return nil, err
	}
	return &AuditRelayMonitor{source: source, depth: depth, age: age, interval: interval, now: time.Now}, nil
}

func (m *AuditRelayMonitor) RunOnce(ctx context.Context) error {
	snapshot, err := m.source.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("audit relay metrics: read queue: %w", err)
	}
	if snapshot.Depth < 0 || (snapshot.Depth == 0 && snapshot.OldestEnqueuedAt != nil) ||
		(snapshot.Depth > 0 && snapshot.OldestEnqueuedAt == nil) {
		return errors.New("audit relay metrics: inconsistent queue snapshot")
	}
	age := 0.0
	if snapshot.OldestEnqueuedAt != nil {
		age = m.now().Sub(*snapshot.OldestEnqueuedAt).Seconds()
		if age < 0 {
			age = 0
		}
	}
	m.depth.Record(ctx, snapshot.Depth)
	m.age.Record(ctx, age)
	return nil
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
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
