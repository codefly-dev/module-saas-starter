//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// The audit relay's quarantine (migration 34): who may touch it, and that moving
// a row into it is one transaction.

func enqueueN(t *testing.T, orgID string, n int) []business.AuditEntry {
	t.Helper()
	entries := make([]business.AuditEntry, n)
	for i := range entries {
		entries[i] = queuedEntry(orgID, business.EventSessionRevoked)
		entry := entries[i]
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			return testStore.EnqueueAuditEvent(ctx, entry)
		}))
	}
	return entries
}

func TestAuditQueueMovesASetAsideRowToTheQuarantineWholeAndKeepsTheRest(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)

	entries := enqueueN(t, orgID, 5)
	before, err := queue.Snapshot(testCtx)
	require.NoError(t, err)

	var seqs []int64
	removed, err := queue.Drain(testCtx, 10, func(_ context.Context, events []business.QueuedAuditEvent) (business.AuditDeliveryOutcome, error) {
		require.Len(t, events, 5)
		for _, event := range events {
			seqs = append(seqs, event.Seq)
		}
		return business.AuditDeliveryOutcome{
			Delivered:   []int64{seqs[0], seqs[1], seqs[3]},
			Quarantined: []business.QuarantinedAuditEvent{{Seq: seqs[2], Reason: "row rejected: invalid value"}},
			Pending:     errors.New("the fifth row could not be written"),
		}, nil
	})
	require.ErrorContains(t, err, "fifth row", "what stays queued is reported with what was removed")
	require.Equal(t, business.AuditDrainResult{Delivered: 3, Quarantined: 1}, removed)

	remaining := drainAllWithoutDeleting(t, queue)
	require.Len(t, remaining, 1, "the row the relay could not write stays queued")
	require.Equal(t, entries[4].ID, remaining[0].Entry.ID)

	var (
		quarantinedSeq           int64
		eventType, resource, why string
		resourceID, actorID, org *string
		payload                  string
		createdAt, quarantinedAt time.Time
		enqueuedAt               time.Time
	)
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT seq, event_type, resource, resource_id, actor_id::text, org_id::text, payload::text, created_at, enqueued_at, quarantined_at, error
		FROM audit_event_quarantine WHERE id = $1`, entries[2].ID).
		Scan(&quarantinedSeq, &eventType, &resource, &resourceID, &actorID, &org, &payload, &createdAt, &enqueuedAt, &quarantinedAt, &why))
	require.Equal(t, seqs[2], quarantinedSeq, "the queue's sequence number is kept")
	require.Equal(t, string(entries[2].EventType), eventType)
	require.Equal(t, entries[2].Resource, resource)
	require.Equal(t, entries[2].ResourceID, *resourceID)
	require.Equal(t, entries[2].ActorID, *actorID)
	require.Equal(t, orgID, *org)
	require.True(t, entries[2].CreatedAt.Equal(createdAt), "the event's own time is kept")
	require.JSONEq(t, `{"ratio": 1.5, "label": "<b>"}`, payload, "and its payload, whole")
	require.Equal(t, "row rejected: invalid value", why)
	require.WithinDuration(t, time.Now(), quarantinedAt, time.Minute)
	require.False(t, enqueuedAt.After(quarantinedAt))

	after, err := queue.Snapshot(testCtx)
	require.NoError(t, err)
	require.Equal(t, before.Quarantined+1, after.Quarantined, "the snapshot counts the quarantine")
	require.EqualValues(t, 1, after.Depth)

	var inQueue int
	require.NoError(t, pool.QueryRow(testCtx, `SELECT count(*) FROM audit_event_queue WHERE id = $1`, entries[2].ID).Scan(&inQueue))
	require.Zero(t, inQueue, "a row is in the queue or the quarantine, never both")
}

// drainAllWithoutDeleting reads what a drain would be handed and leaves it.
func drainAllWithoutDeleting(t *testing.T, queue *infra.PostgresAuditQueue) []business.QueuedAuditEvent {
	t.Helper()
	var got []business.QueuedAuditEvent
	_, err := queue.Drain(testCtx, 1000, func(_ context.Context, events []business.QueuedAuditEvent) (business.AuditDeliveryOutcome, error) {
		got = events
		return business.AuditDeliveryOutcome{}, nil
	})
	require.NoError(t, err)
	return got
}

func TestAuditQueueQuarantineMoveIsOneTransaction(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	entries := enqueueN(t, orgID, 2)

	// A quarantine row already holds the sequence number: the move cannot insert.
	var firstSeq int64
	require.NoError(t, pool.QueryRow(testCtx, `SELECT min(seq) FROM audit_event_queue`).Scan(&firstSeq))
	_, err = pool.Exec(testCtx, `
		INSERT INTO audit_event_quarantine (seq, xact_id, id, event_type, schema_version, actor_type, resource, created_at, enqueued_at, error)
		VALUES ($1, pg_current_xact_id(), $2, 'saas.session.revoked', 1, 'user', 'session', now(), now(), 'earlier')`,
		firstSeq, business.NewIDString())
	require.NoError(t, err)

	_, err = queue.Drain(testCtx, 10, func(_ context.Context, events []business.QueuedAuditEvent) (business.AuditDeliveryOutcome, error) {
		return business.AuditDeliveryOutcome{
			Delivered:   []int64{events[1].Seq},
			Quarantined: []business.QuarantinedAuditEvent{{Seq: events[0].Seq, Reason: "refused"}},
		}, nil
	})
	require.Error(t, err)

	remaining := drainAllWithoutDeleting(t, queue)
	require.Len(t, remaining, 2, "a move that fails takes the delivered row's delete back with it")
	require.Equal(t, entries[0].ID, remaining[0].Entry.ID)
	require.Equal(t, entries[1].ID, remaining[1].Entry.ID)
}

func TestAuditQueueRefusesAnOutcomeThatNamesARowItDidNotRead(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	enqueueN(t, orgID, 2)

	for name, outcome := range map[string]func([]business.QueuedAuditEvent) business.AuditDeliveryOutcome{
		"a row that is not there": func([]business.QueuedAuditEvent) business.AuditDeliveryOutcome {
			return business.AuditDeliveryOutcome{Delivered: []int64{1 << 50}}
		},
		"a row delivered and set aside": func(events []business.QueuedAuditEvent) business.AuditDeliveryOutcome {
			return business.AuditDeliveryOutcome{
				Delivered:   []int64{events[0].Seq},
				Quarantined: []business.QuarantinedAuditEvent{{Seq: events[0].Seq, Reason: "twice"}},
			}
		},
	} {
		_, err := queue.Drain(testCtx, 10, func(_ context.Context, events []business.QueuedAuditEvent) (business.AuditDeliveryOutcome, error) {
			return outcome(events), nil
		})
		require.Error(t, err, name)
	}
	require.Len(t, drainAllWithoutDeleting(t, queue), 2)
}

func TestAuditQuarantineIsReadableAndWritableOnlyByTheRelaysRole(t *testing.T) {
	pool := auditRelayPool(t)
	orgID := seedOrg(t, seedUser(t))

	_, err := pool.Exec(testCtx, `DELETE FROM audit_event_quarantine`)
	require.Error(t, err, "the relay cannot remove a quarantined row")
	_, err = pool.Exec(testCtx, `UPDATE audit_event_quarantine SET error = 'edited'`)
	require.Error(t, err, "nor change one")

	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var n int
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_event_quarantine`).Scan(&n)
	})
	require.Error(t, err, "a tenant cannot read the quarantine")

	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var n int
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_event_quarantine`).Scan(&n)
	})
	require.Error(t, err, "nor can the control plane")
	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `
			INSERT INTO audit_event_quarantine (seq, xact_id, id, event_type, schema_version, actor_type, resource, created_at, enqueued_at, error)
			VALUES (1, pg_current_xact_id(), $1, 'saas.session.revoked', 1, 'user', 'session', now(), now(), 'x')`, business.NewIDString())
		return err
	})
	require.Error(t, err, "or write to it")
}
