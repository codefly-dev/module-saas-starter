package main

import (
	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"context"
	"fmt"
	"os"
)

// configuredAuditSink reads AUDIT_SINK and the settings its mode requires
// (auditsink.Load).
func configuredAuditSink() (auditsink.Config, error) {
	return auditsink.Load(os.Getenv)
}

// newAuditSwap builds a swap value (ADR 0009): its store of record and archive
// (auditsink.Open), and the relay that drains the queue into them on the
// relay's own pool. The service reads the returned store; the returned close
// releases every client.
func newAuditSwap(ctx context.Context, types business.DeclaredAuditEventTypeReader, swap *auditsink.Swap) (*business.AuditRelay, business.AuditStore, func(), error) {
	opened, err := auditsink.Open(ctx, swap)
	if err != nil {
		return nil, nil, nil, err
	}
	closers := []func(){opened.Close}
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (*business.AuditRelay, business.AuditStore, func(), error) {
		closeAll()
		return nil, nil, nil, err
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
	return relay, opened.Store, closeAll, nil
}
