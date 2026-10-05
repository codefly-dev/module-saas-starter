package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
)

// Events left in the audit queue by a warehouse sink that was switched back are
// delivered only when a warehouse sink returns. Startup says so, with the count,
// and does not refuse to start: that would take login down for a side table.

type queueSource struct {
	snapshot business.AuditQueueSnapshot
	err      error
}

func (q queueSource) Snapshot(context.Context) (business.AuditQueueSnapshot, error) {
	return q.snapshot, q.err
}

type logCapture struct {
	mu   sync.Mutex
	logs []*wool.Log
}

func (c *logCapture) Process(log *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, log)
}

func (c *logCapture) at(level wool.Loglevel) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, log := range c.logs {
		if log.Level == level {
			out = append(out, log.String())
		}
	}
	return out
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	previous := wool.GlobalLogLevel()
	wool.SetGlobalLogLevel(wool.TRACE)
	capture := &logCapture{}
	wool.SetFallbackLogger(capture)
	t.Cleanup(func() {
		wool.SetFallbackLogger(nil)
		wool.SetGlobalLogLevel(previous)
	})
	return capture
}

func TestStartupReportsQueuedAuditEventsNoSinkWillDeliver(t *testing.T) {
	oldest := time.Now().Add(-time.Hour)
	queued := queueSource{snapshot: business.AuditQueueSnapshot{Depth: 42, OldestEnqueuedAt: &oldest}}

	logs := captureLogs(t)
	warnStrandedAuditQueue(context.Background(), business.AuditSinkPostgres, queued)
	errs := logs.at(wool.ERROR)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "42", "the count of stranded events")
	require.Contains(t, errs[0], "warehouse AUDIT_SINK", "and what brings them back")

	logs = captureLogs(t)
	warnStrandedAuditQueue(context.Background(), business.AuditSinkBigQuery, queued)
	require.Empty(t, logs.at(wool.ERROR), "under a warehouse sink the relay drains the queue")

	logs = captureLogs(t)
	warnStrandedAuditQueue(context.Background(), business.AuditSinkPostgres, queueSource{})
	require.Empty(t, logs.at(wool.ERROR), "an empty queue is not reported")

	logs = captureLogs(t)
	warnStrandedAuditQueue(context.Background(), business.AuditSinkBoth, queueSource{err: errors.New("database unavailable")})
	require.Empty(t, logs.at(wool.ERROR), "an unreadable queue is a warning, not a claim about stranded events")
	require.Len(t, logs.at(wool.WARN), 1)
}
