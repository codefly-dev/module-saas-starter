package main

import (
	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/codefly-dev/core/wool"
	"go.opentelemetry.io/otel"
)

// auditQueueMonitorInterval is how often the queue's depth and age are read.
const auditQueueMonitorInterval = 10 * time.Second

// configuredAuditSink reads AUDIT_SINK and the settings its mode requires
// (auditsink.Load).
func configuredAuditSink() (auditsink.Config, error) {
	return auditsink.Load(os.Getenv)
}

// newAuditSwap builds a swap value (ADR 0009): its store of record and archive
// (auditsink.Open), and the relay that drains the queue into them on the
// relay's own pool. The service reads the returned store; the returned close
// releases every client.
func newAuditSwap(ctx context.Context, types business.DeclaredAuditEventTypeReader, swap *auditsink.Swap, metricsEnabled bool) (*business.AuditRelay, *business.AuditRelayMonitor, business.AuditStore, func(), error) {
	opened, err := auditsink.Open(ctx, swap)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	closers := []func(){opened.Close}
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (*business.AuditRelay, *business.AuditRelayMonitor, business.AuditStore, func(), error) {
		closeAll()
		return nil, nil, nil, nil, err
	}

	pool, err := infra.NewAuditRelayPool(ctx)
	if err != nil {
		return fail(fmt.Errorf("audit relay: database pool: %w", err))
	}
	closers = append(closers, pool.Close)
	queue, err := infra.NewPostgresAuditQueue(pool)
	if err != nil {
		return fail(err)
	}

	relay, err := business.NewAuditRelay(business.AuditRelayConfig{
		Queue:        queue,
		Store:        opened.Store,
		Archive:      opened.Archive,
		Types:        types,
		DeploymentID: swap.DeploymentID,
		BatchSize:    swap.RelayBatchSize,
		MaxWait:      swap.RelayMaxWait,
	})
	if err != nil {
		return fail(err)
	}
	var monitor *business.AuditRelayMonitor
	if metricsEnabled {
		monitor, err = newAuditQueueMonitor(queue)
		if err != nil {
			return fail(err)
		}
	}
	return relay, monitor, opened.Store, closeAll, nil
}

func newAuditQueueMonitor(queue *infra.PostgresAuditQueue) (*business.AuditRelayMonitor, error) {
	return business.NewAuditRelayMonitor(queue, otel.Meter("github.com/codefly-dev/module-saas-starter/accounts"), auditQueueMonitorInterval)
}

// observeAuditQueue watches the audit queue under a sink that does not drain it
// (postgres, both), so the queue is never invisible. If a deployment switched
// back from a warehouse sink with events still queued, they are neither lost nor
// delivered — they wait for a warehouse sink to be restored — and the only thing
// that tells an operator so is this: an error at startup with the count, and the
// depth, age and quarantine series for as long as the service runs.
//
// It never fails startup. The queue is the audit trail's business, not login's:
// a service that refuses to start because a side table is unreadable takes
// every user down for a problem that costs them nothing. Any trouble here is a
// warning, and the missing series is itself what the telemetry alert reads.
func observeAuditQueue(ctx context.Context, mode business.AuditSinkMode, metricsEnabled bool) (*business.AuditRelayMonitor, func()) {
	w := wool.Get(ctx).In("observeAuditQueue")
	pool, err := infra.NewAuditRelayPool(ctx)
	if err != nil {
		w.Warn("audit queue is not observed: its database pool could not be opened", wool.ErrField(err))
		return nil, func() {}
	}
	queue, err := infra.NewPostgresAuditQueue(pool)
	if err != nil {
		pool.Close()
		w.Warn("audit queue is not observed", wool.ErrField(err))
		return nil, func() {}
	}
	warnStrandedAuditQueue(ctx, mode, queue)
	if !metricsEnabled {
		pool.Close()
		return nil, func() {}
	}
	monitor, err := newAuditQueueMonitor(queue)
	if err != nil {
		pool.Close()
		w.Warn("audit queue metrics are not recorded", wool.ErrField(err))
		return nil, func() {}
	}
	return monitor, pool.Close
}

// warnStrandedAuditQueue logs an error when events are queued under a sink that
// will not deliver them.
func warnStrandedAuditQueue(ctx context.Context, mode business.AuditSinkMode, queue business.AuditQueueMetricsSource) {
	w := wool.Get(ctx).In("warnStrandedAuditQueue")
	stranded, err := business.StrandedAuditQueue(ctx, mode, queue)
	if err != nil {
		w.Warn("audit queue could not be read at startup", wool.ErrField(err))
		return
	}
	if stranded > 0 {
		w.Error("audit events are queued and AUDIT_SINK does not deliver them: they reach the warehouse only when a warehouse AUDIT_SINK (bigquery or clickhouse) is restored; until then they stay queued and new audit events are written to audit_events",
			wool.Field("sink", string(mode)), wool.Field("queued_events", fmt.Sprint(stranded)))
	}
}
