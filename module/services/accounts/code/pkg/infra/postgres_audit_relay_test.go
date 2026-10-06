//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// The relay against the real queue and quarantine: the relay's decisions have
// to be something Postgres can store, or the transaction that carries them
// rolls back together with the rows it delivered, and does so on every pass.

// refusingWarehouse is a store that refuses, for good, the rows it was told to.
type refusingWarehouse struct {
	mu     sync.Mutex
	refuse map[string]error
	stored map[string]bool
}

func (w *refusingWarehouse) AppendAuditBatch(_ context.Context, batch business.AuditBatch) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var refused []error
	for _, record := range batch.Records {
		if cause, ok := w.refuse[record.Entry.ID]; ok {
			refused = append(refused, &business.PermanentRowRejection{EventID: record.Entry.ID, Cause: cause})
		}
	}
	if len(refused) > 0 {
		return errors.Join(refused...)
	}
	if w.stored == nil {
		w.stored = map[string]bool{}
	}
	for _, record := range batch.Records {
		w.stored[record.Entry.ID] = true
	}
	return nil
}

type discardingArchive struct{}

func (discardingArchive) WriteAuditBatch(context.Context, business.AuditBatch) error { return nil }

// A store's error text is the store's own: it can be long, hold any character,
// and hold bytes that are not text at all. The reason kept with a quarantined
// row is cut to a bound, and the cut must not leave a half character, nor may a
// NUL or an invalid byte reach a text column.
func TestAuditRelayQuarantinesARowWhoseErrorIsLongMultibyteOrNotText(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	entries := enqueueN(t, orgID, 6)

	// The two long causes start one byte apart, so whatever the text before
	// them, the 1000-byte cut falls inside a two-byte character for one of them.
	causes := map[int]string{
		1: strings.Repeat("é", 600),
		2: "x" + strings.Repeat("é", 600),
		3: "before\x00after",
		4: "bad \xff\xfe bytes",
	}
	warehouse := &refusingWarehouse{refuse: map[string]error{}}
	for i, cause := range causes {
		warehouse.refuse[entries[i].ID] = errors.New(cause)
	}
	relay, err := business.NewAuditRelay(business.AuditRelayConfig{
		Queue: queue, Store: warehouse, Archive: discardingArchive{}, DeploymentID: "deployment-1",
		BatchSize: len(entries), MaxWait: time.Second,
	})
	require.NoError(t, err)

	delivered, err := relay.DrainOnce(testCtx)
	require.NoError(t, err, "a reason Postgres cannot store must not roll back the delivery")
	require.Equal(t, 2, delivered)
	require.Empty(t, drainAllWithoutDeleting(t, queue), "every row left the queue: delivered or quarantined")
	require.Len(t, warehouse.stored, 2)

	for i := range causes {
		var why string
		require.NoError(t, pool.QueryRow(testCtx, `SELECT error FROM audit_event_quarantine WHERE id = $1`, entries[i].ID).Scan(&why),
			"row %d is in the quarantine", i)
		require.True(t, utf8.ValidString(why), "row %d: the reason is text", i)
		require.NotContains(t, why, "\x00", "row %d", i)
		require.LessOrEqual(t, len(why), 1000, "row %d: the reason stays bounded", i)
		require.Contains(t, why, entries[i].ID, "row %d: and still says which event it is about", i)
		require.True(t, strings.HasPrefix(why, business.QuarantineWarehouseRejectedArchived+": "), "row %d: and what the archive holds", i)
	}
	var why string
	require.NoError(t, pool.QueryRow(testCtx, `SELECT error FROM audit_event_quarantine WHERE id = $1`, entries[3].ID).Scan(&why))
	require.Contains(t, why, "before")
	require.Contains(t, why, "after")
}

// The queue enforces the constraint itself, whoever built the outcome.
func TestAuditQueueStoresAReasonItWouldOtherwiseRefuse(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	entries := enqueueN(t, orgID, 2)

	_, err = queue.Drain(testCtx, 10, func(_ context.Context, events []business.QueuedAuditEvent) (business.AuditDeliveryOutcome, error) {
		return business.AuditDeliveryOutcome{Quarantined: []business.QuarantinedAuditEvent{
			{Seq: events[0].Seq, Reason: "nul \x00 byte"},
			{Seq: events[1].Seq, Reason: strings.Repeat("é", 2000) + "\xff"},
		}}, nil
	})
	require.NoError(t, err)
	require.Empty(t, drainAllWithoutDeleting(t, queue))

	for _, entry := range entries {
		var why string
		require.NoError(t, pool.QueryRow(testCtx, `SELECT error FROM audit_event_quarantine WHERE id = $1`, entry.ID).Scan(&why))
		require.True(t, utf8.ValidString(why))
		require.LessOrEqual(t, len(why), 1000)
	}
}
