package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/core/wool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type OperationsMonitor struct {
	source   Operations
	interval time.Duration
	depth    metric.Int64Gauge
	age      metric.Float64Gauge

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewOperationsMonitor(
	source Operations,
	meter metric.Meter,
	interval time.Duration,
) (*OperationsMonitor, error) {
	if source == nil {
		return nil, errors.New("jobs: operations metric source is required")
	}
	if meter == nil {
		return nil, errors.New("jobs: operations metric meter is required")
	}
	if interval <= 0 {
		return nil, errors.New("jobs: operations metric interval must be positive")
	}
	depth, err := meter.Int64Gauge(
		"saas.jobs.depth",
		metric.WithDescription("Durable jobs by queue and state."),
	)
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64Gauge(
		"saas.jobs.oldest_ready_age",
		metric.WithDescription("Age of the oldest ready job in each queue."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	return &OperationsMonitor{
		source: source, interval: interval, depth: depth, age: age,
	}, nil
}

func (m *OperationsMonitor) RunOnce(ctx context.Context) error {
	response, err := m.source.GetJobOperations(
		ctx,
		&jobsv1.GetJobOperationsRequest{},
	)
	if err != nil {
		return fmt.Errorf("jobs: observe durable queue state: %w", err)
	}
	if response.GetObservedAt() == nil || !response.GetObservedAt().IsValid() {
		return errors.New("jobs: operations snapshot has no valid observation time")
	}
	observedAt := response.GetObservedAt().AsTime()
	for _, queue := range response.GetQueues() {
		if err := m.recordQueue(ctx, queue, observedAt); err != nil {
			return err
		}
	}
	return nil
}

func (m *OperationsMonitor) recordQueue(
	ctx context.Context,
	queue *jobsv1.JobQueueSnapshot,
	observedAt time.Time,
) error {
	if queue == nil || queue.GetQueue() == "" {
		return errors.New("jobs: operations snapshot contains an invalid queue")
	}
	values := []struct {
		state string
		value uint64
	}{
		{state: "pending", value: queue.GetPending()},
		{state: "processing", value: queue.GetProcessing()},
		{state: "retrying", value: queue.GetRetrying()},
		{state: "dead_letter", value: queue.GetDeadLetter()},
		{state: "ready", value: queue.GetReady()},
		{state: "scheduled", value: queue.GetScheduled()},
		{state: "expired_lease", value: queue.GetExpiredLeases()},
	}
	for _, item := range values {
		if item.value > math.MaxInt64 {
			return fmt.Errorf(
				"jobs: queue %q %s depth exceeds int64",
				queue.GetQueue(),
				item.state,
			)
		}
		m.depth.Record(ctx, int64(item.value), metric.WithAttributes(
			attribute.String("queue", queue.GetQueue()),
			attribute.String("state", item.state),
		))
	}
	age := 0.0
	if queue.GetOldestReadyAt() != nil && queue.GetOldestReadyAt().IsValid() {
		age = observedAt.Sub(queue.GetOldestReadyAt().AsTime()).Seconds()
		if age < 0 {
			age = 0
		}
	}
	m.age.Record(ctx, age, metric.WithAttributes(
		attribute.String("queue", queue.GetQueue()),
	))
	return nil
}

func (m *OperationsMonitor) Start(parent context.Context) {
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
			if err := m.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				wool.Get(ctx).In("jobs.operations_metrics").Warn(
					"queue observation failed",
					wool.ErrField(err),
				)
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

func (m *OperationsMonitor) Shutdown(ctx context.Context) error {
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
