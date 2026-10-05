package business

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Switching AUDIT_SINK back to postgres leaves queued events that no relay
// drains. Startup must say so — with the count — instead of refusing to start
// (which would take login down) or saying nothing.
func TestStrandedAuditQueueCountsRowsNoSinkWillDrain(t *testing.T) {
	oldest := time.Now().Add(-time.Hour)
	queued := &auditQueueSnapshotFake{snapshot: AuditQueueSnapshot{Depth: 42, OldestEnqueuedAt: &oldest}}

	for _, mode := range []AuditSinkMode{AuditSinkPostgres, AuditSinkBoth} {
		stranded, err := StrandedAuditQueue(context.Background(), mode, queued)
		require.NoError(t, err)
		require.Equal(t, int64(42), stranded, "under %s nothing drains the queue", mode)
	}
	for _, mode := range []AuditSinkMode{AuditSinkBigQuery, AuditSinkClickHouse} {
		stranded, err := StrandedAuditQueue(context.Background(), mode, queued)
		require.NoError(t, err)
		require.Zero(t, stranded, "under %s the relay drains it", mode)
	}

	stranded, err := StrandedAuditQueue(context.Background(), AuditSinkPostgres, &auditQueueSnapshotFake{})
	require.NoError(t, err)
	require.Zero(t, stranded, "an empty queue strands nothing")

	_, err = StrandedAuditQueue(context.Background(), AuditSinkPostgres, &auditQueueSnapshotFake{err: errors.New("database unavailable")})
	require.ErrorContains(t, err, "database unavailable", "an unreadable queue is reported, not taken for an empty one")
}
