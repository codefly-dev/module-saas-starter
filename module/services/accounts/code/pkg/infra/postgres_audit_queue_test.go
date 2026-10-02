//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// The transactional audit queue under a swap value (ADR 0009), against the real
// schema: who may write it, who may read and delete it, and the guarantees the
// relay's side gives — committed rows only, in order, deleted only on success.

// catalogAudit is the recorder the role catalog import requires, in the
// default (postgres) mode.
func catalogAudit(t *testing.T, opts ...business.DurableAuditEmitterOption) business.AuditRecorder {
	t.Helper()
	recorder, err := business.NewDurableAuditEmitter(testStore, testStore, opts...)
	require.NoError(t, err)
	return recorder
}

func auditRelayPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := infra.NewAuditRelayPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// emptyAuditQueue removes every queued event, as the relay would, so each test
// starts from an empty queue.
func emptyAuditQueue(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(testCtx, `DELETE FROM audit_event_queue`)
	require.NoError(t, err)
}

func queuedEntry(orgID string, eventType business.EventType) business.AuditEntry {
	return business.AuditEntry{
		ID:            business.NewIDString(),
		OrgID:         orgID,
		ActorID:       business.NewIDString(),
		ActorType:     business.ActorTypeUser,
		EventType:     eventType,
		SchemaVersion: 1,
		Resource:      "session",
		ResourceID:    "session-1",
		Payload:       map[string]any{"ratio": 1.5, "label": "<b>"},
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
}

func drainAll(t *testing.T, queue *infra.PostgresAuditQueue) []business.QueuedAuditEvent {
	t.Helper()
	var got []business.QueuedAuditEvent
	_, err := queue.Drain(testCtx, 1000, func(_ context.Context, events []business.QueuedAuditEvent) (bool, error) {
		got = events
		return true, nil
	})
	require.NoError(t, err)
	return got
}

func TestAuditQueueTenantWritesOnlyItsOwnOrganizationAndReadsNothing(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	otherOrgID := seedOrg(t, seedUser(t))

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.EnqueueAuditEvent(ctx, queuedEntry(orgID, business.EventSessionRevoked))
	}))

	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.EnqueueAuditEvent(ctx, queuedEntry(otherOrgID, business.EventSessionRevoked))
	})
	require.Error(t, err, "a tenant transaction cannot queue another organization's event")

	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var n int
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_event_queue`).Scan(&n)
	})
	require.Error(t, err, "a writer cannot read the queue back")

	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `DELETE FROM audit_event_queue`)
		return err
	})
	require.Error(t, err, "the control plane writes the queue but cannot remove a queued event")

	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	events := drainAll(t, queue)
	require.Len(t, events, 1)
	require.Equal(t, orgID, events[0].Entry.OrgID)
}

func TestAuditQueueCarriesTheEventAsAuditEventsWouldKeepIt(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))

	entry := queuedEntry(orgID, business.EventSessionRevoked)
	entry.IPAddress = "203.0.113.7"
	entry.ClientID = "example-cli"
	entry.ImpersonatedBy = business.NewIDString()
	entry.IsImpersonated = true
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.EnqueueAuditEvent(ctx, entry)
	}))
	platform := queuedEntry("", business.EventRoleCreated)
	platform.ActorID = ""
	platform.ActorType = business.ActorTypeSystem
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.EnqueueAuditEvent(ctx, platform)
	}), "a platform event is queued on the control plane")

	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	events := drainAll(t, queue)
	require.Len(t, events, 2)
	require.Less(t, events[0].Seq, events[1].Seq)

	got := events[0].Entry
	require.Equal(t, entry.ID, got.ID)
	require.Equal(t, entry.EventType, got.EventType)
	require.Equal(t, entry.ActorID, got.ActorID)
	require.Equal(t, entry.Resource, got.Resource)
	require.Equal(t, entry.ResourceID, got.ResourceID)
	require.Equal(t, entry.IPAddress, got.IPAddress)
	require.Equal(t, entry.ClientID, got.ClientID)
	require.Equal(t, entry.ImpersonatedBy, got.ImpersonatedBy)
	require.True(t, got.IsImpersonated)
	require.True(t, entry.CreatedAt.Equal(got.CreatedAt))
	details, err := business.CanonicalAuditDetails(got.Payload)
	require.NoError(t, err)
	require.Equal(t, `{"label":"<b>","ratio":1.5}`, details, "the payload reads back as the canonical details of what was written")

	require.Empty(t, events[1].Entry.OrgID)
	require.Empty(t, events[1].Entry.ActorID)
	require.Equal(t, business.ActorTypeSystem, events[1].Entry.ActorType)
}

func TestAuditQueueDeletesOnlyWhatWasDelivered(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	for range 5 {
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			return testStore.EnqueueAuditEvent(ctx, queuedEntry(orgID, business.EventSessionRevoked))
		}))
	}
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)

	_, err = queue.Drain(testCtx, 3, func(context.Context, []business.QueuedAuditEvent) (bool, error) {
		return false, errors.New("warehouse unavailable")
	})
	require.ErrorContains(t, err, "warehouse unavailable")
	n, err := queue.Drain(testCtx, 3, func(context.Context, []business.QueuedAuditEvent) (bool, error) {
		return false, nil
	})
	require.NoError(t, err)
	require.Zero(t, n, "a batch the relay is not ready to deliver is left queued")

	var first []business.QueuedAuditEvent
	n, err = queue.Drain(testCtx, 3, func(_ context.Context, events []business.QueuedAuditEvent) (bool, error) {
		first = events
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, n)

	rest := drainAll(t, queue)
	require.Len(t, rest, 2, "exactly the delivered rows were deleted")
	require.Greater(t, rest[0].Seq, first[2].Seq, "and what remains is what came after them")
}

// A row is not handed to the relay while a transaction that began before it
// is still running: when that transaction commits, its row is ordered first.
func TestAuditQueueNeverOvertakesAnEarlierTransactionStillInFlight(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)

	early := queuedEntry(orgID, business.EventSessionRevoked)
	late := queuedEntry(orgID, business.EventSessionRevoked)
	earlyQueued, releaseEarly := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var release sync.Once
	finishEarly := func() {
		release.Do(func() { close(releaseEarly) })
		wg.Wait()
	}
	// A failed assertion must still end the open transaction, or the package's
	// pool never drains and the run hangs instead of failing.
	t.Cleanup(finishEarly)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			if err := testStore.EnqueueAuditEvent(ctx, early); err != nil {
				return err
			}
			close(earlyQueued)
			<-releaseEarly
			return nil
		})
	}()
	<-earlyQueued
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.EnqueueAuditEvent(ctx, late)
	}))

	require.Empty(t, drainAll(t, queue), "the committed later row waits for the earlier transaction to finish")

	finishEarly()
	events := drainAll(t, queue)
	require.Len(t, events, 2)
	require.Equal(t, early.ID, events[0].Entry.ID, "queue order, not commit order")
	require.Equal(t, late.ID, events[1].Entry.ID)
}

func TestAuditQueueLeaseAdmitsOneRelayAtATime(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.EnqueueAuditEvent(ctx, queuedEntry(orgID, business.EventSessionRevoked))
	}))
	first, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	second, err := infra.NewPostgresAuditQueue(auditRelayPool(t))
	require.NoError(t, err)

	_, err = first.Drain(testCtx, 10, func(ctx context.Context, events []business.QueuedAuditEvent) (bool, error) {
		require.Len(t, events, 1)
		n, err := second.Drain(ctx, 10, func(context.Context, []business.QueuedAuditEvent) (bool, error) {
			t.Fatal("a second relay must not be handed rows while the first holds the lease")
			return false, nil
		})
		require.NoError(t, err)
		require.Zero(t, n)
		return true, nil
	})
	require.NoError(t, err)
}

// Under a swap value the emitter's record is a queue row and audit_events
// receives nothing; the webhook outbox is written beside it as before.
func TestQueuedRecordsLeaveAuditEventsUntouched(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	orgID := seedOrg(t, seedUser(t))
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore, business.WithQueuedRecords())
	require.NoError(t, err)

	entry := queuedEntry(orgID, business.EventSessionRevoked)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return emitter.EmitTx(ctx, entry)
	}))

	var inAuditEvents int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id = $1`, entry.ID).Scan(&inAuditEvents)
	}))
	require.Zero(t, inAuditEvents)

	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	events := drainAll(t, queue)
	require.Len(t, events, 1)
	require.Equal(t, entry.ID, events[0].Entry.ID)
}

// The role catalog import writes platform events outside the Service; under a
// swap value they reach the queue like every other record.
func TestImportRoleCatalogUnderTheSwapQueuesItsEvents(t *testing.T) {
	pool := auditRelayPool(t)
	emptyAuditQueue(t, pool)
	resetCatalogRoles(t)
	catalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:queued","permissions":[{"resource":"x","action":"read"}]}]}`)

	_, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{
		Audit: catalogAudit(t, business.WithQueuedRecords()),
	})
	require.NoError(t, err)
	role, found := readBuiltinRole(t, "catalog-test:queued")
	require.True(t, found)

	var inAuditEvents int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx,
			`SELECT count(*) FROM audit_events WHERE resource = 'role' AND resource_id = $1`, role.id).Scan(&inAuditEvents)
	}))
	require.Zero(t, inAuditEvents)

	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	events := drainAll(t, queue)
	require.Len(t, events, 1)
	require.Equal(t, business.EventRoleCreated, events[0].Entry.EventType)
	require.Equal(t, role.id, events[0].Entry.ResourceID)
	require.Empty(t, events[0].Entry.OrgID, "built-in roles belong to no organization")
}

func TestImportRoleCatalogRefusesToApplyUnrecorded(t *testing.T) {
	resetCatalogRoles(t)
	catalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:unrecorded","permissions":[{"resource":"x","action":"read"}]}]}`)
	_, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{})
	require.ErrorContains(t, err, "audit recorder is required")
	_, found := readBuiltinRole(t, "catalog-test:unrecorded")
	require.False(t, found)
}
