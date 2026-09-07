package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/events"
	"accounts/pkg/events/eventstest"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// seedSubscriptions materializes the harness's in-memory subscriptions as rows
// in event_subscriptions, the relay's real resolution source. Request traffic
// can only SELECT that table, so the seed runs under the control-plane role, the
// same authority ModuleCapabilitiesService.Subscribe uses in production. The
// harness gives every subtest a unique queue, so rows from earlier subtests stay
// in the shared table harmlessly: their deliveries land on queues nobody claims.
func seedSubscriptions(t *testing.T, subscriptions []events.Subscription) {
	t.Helper()
	if len(subscriptions) == 0 {
		return
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		for _, subscription := range subscriptions {
			if _, err := tx.Exec(ctx, `
				INSERT INTO public.event_subscriptions
					(subscriber_principal_id, type_pattern, queue, delivery)
				VALUES (gen_random_uuid(), $1, $2, $3)`,
				subscription.TypePattern, subscription.Queue, string(subscription.Delivery),
			); err != nil {
				return err
			}
		}
		return nil
	}))
}

func TestPostgresEventTransportConformance(t *testing.T) {
	const lease = 300 * time.Millisecond
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	eventstest.RunConformance(t, eventstest.Harness{
		New: func(t *testing.T, subscriptions []events.Subscription) events.Transport {
			seedSubscriptions(t, subscriptions)
			return infra.NewPostgresEventTransport(store, pool, "events-conformance-"+uuid.NewString(), lease)
		},
		LeaseDuration: lease,
	})
}

// TestPostgresEventTransportPublishJoinsCallerTransaction proves the outbox
// rule: Publish with a caller transaction writes the event-of-record inside it,
// so a rolled-back producer transaction leaves nothing durable and nothing to
// replay. Durability of the event is independent of fan-out — the relay reads
// domain_events after commit — so this is the guarantee that matters.
func TestPostgresEventTransportPublishJoinsCallerTransaction(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "events-atomic-"+uuid.NewString(), 300*time.Millisecond)

	tenant := uuid.NewString()
	event := &events.EventEnvelope{
		Id:            uuid.NewString(),
		Type:          "documents.entry.ingested",
		Source:        "urn:codefly:documents/ingest",
		Specversion:   "1.0",
		TenantId:      tenant,
		SchemaVersion: 1,
		Data:          []byte("payload"),
	}

	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	require.NoError(t, transport.Publish(testCtx, tx, event), "publish within a caller tx writes the event-of-record into it")
	require.NoError(t, tx.Rollback(testCtx), "the producer transaction rolls back")

	replayed, err := transport.Replay(testCtx, events.ReplaySelector{Type: event.GetType(), TenantID: tenant})
	require.NoError(t, err)
	require.Zero(t, replayed, "a rolled-back producer leaves no durable event-of-record behind")
}

// TestDomainEventsTenantIsolationUnderRLS pins the tenant boundary declared by
// migration 114's domain_events_tenant policy (114:77-79): request traffic reads
// only its own organization's events, and a connection with no tenant context
// reads nothing. Both are load-bearing for a regulated-finance customer — a
// cross-tenant read or a fail-open on an unset GUC would leak one tenant's event
// stream to another. The publish path is function-only and BYPASSRLS-seeded here,
// so this test isolates exactly the SELECT policy, which is the only way request
// traffic ever touches the relation.
func TestDomainEventsTenantIsolationUnderRLS(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "events-rls-"+uuid.NewString(), 300*time.Millisecond)

	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	eventA := &events.EventEnvelope{
		Id:            uuid.NewString(),
		Type:          "reference.console.viewed",
		Source:        "urn:codefly:accounts",
		Specversion:   "1.0",
		TenantId:      tenantA,
		SchemaVersion: 1,
		Data:          []byte("a"),
	}
	eventB := &events.EventEnvelope{
		Id:            uuid.NewString(),
		Type:          "reference.console.viewed",
		Source:        "urn:codefly:accounts",
		Specversion:   "1.0",
		TenantId:      tenantB,
		SchemaVersion: 1,
		Data:          []byte("b"),
	}

	// Seed one event per tenant through the outbox function under the relay
	// worker's BYPASSRLS role, the only role that may publish for an arbitrary
	// tenant. Committing makes the rows the durable event-of-record the tenant
	// read below resolves under RLS.
	seedTx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	require.NoError(t, transport.Publish(testCtx, seedTx, eventA))
	require.NoError(t, transport.Publish(testCtx, seedTx, eventB))
	require.NoError(t, seedTx.Commit(testCtx))

	seeded := []string{eventA.GetId(), eventB.GetId()}

	// Sanity: both rows are durably present. The BYPASSRLS relay role sees the
	// whole relation, so the fail-closed assertions below cannot be vacuous.
	var total int
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT count(*) FROM public.domain_events WHERE id = ANY($1::uuid[])`, seeded,
	).Scan(&total))
	require.Equal(t, 2, total, "both tenants' events must be durably seeded")

	// Tenant A's request role sees only tenant A's event, even when tenant B's
	// id is named explicitly: the policy filters on app.current_org_id, not on
	// the query predicate.
	require.NoError(t, testStore.WithOrgTx(testCtx, tenantA, func(ctx context.Context) error {
		tx := txFromCtx(t, ctx)
		rows, err := tx.Query(ctx,
			`SELECT id::text FROM public.domain_events WHERE id = ANY($1::uuid[])`, seeded)
		require.NoError(t, err)
		visible, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		require.Equal(t, []string{eventA.GetId()}, visible,
			"tenant A must see its own event and never tenant B's")
		return nil
	}))

	// A bare request connection carries no tenant context (app.current_org_id is
	// unset). The policy's USING clause is then undefined, and the fail-closed
	// contract at 114:77-79 must yield zero rows — never every row.
	var leaked int
	require.NoError(t, testStore.Pool().QueryRow(testCtx,
		`SELECT count(*) FROM public.domain_events WHERE id = ANY($1::uuid[])`, seeded,
	).Scan(&leaked))
	require.Zero(t, leaked, "an unset app.current_org_id must fail closed, not expose every tenant's events")
}
