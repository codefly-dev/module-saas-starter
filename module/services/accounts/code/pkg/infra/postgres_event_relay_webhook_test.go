package infra_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/events"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// externalEventType is a type the composed catalog declares with external
// visibility — the audit registry projects every one of its types that way, and
// only an external type may leave the platform over a webhook.
const externalEventType = "saas.api_key.created"

// tenantOnlyEventType is declared tenant-visible, so it is deliverable to a
// module subscriber and never to an endpoint.
const tenantOnlyEventType = "scope.granted"

func newWebhookRelayTransport(t *testing.T) (*infra.PostgresEventTransport, *pgxpool.Pool) {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return infra.NewPostgresEventTransport(
		infra.NewPostgresJobStore(pool), pool, "relay-webhook-"+uuid.NewString(), time.Second,
		infra.WithWebhookRelay(infra.NewPostgresWebhookRelay(testStore)),
	), pool
}

// seedWebhookEndpoint registers an endpoint for one org and derives its
// subscription rows the way CreateSubscription does.
func seedWebhookEndpoint(t *testing.T, orgID string, active bool, eventNames ...string) string {
	t.Helper()
	sub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/" + business.NewIDString(), SecretEncrypted: "encrypted:sec",
		Events: eventNames, Active: active,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateWebhookSubscription(ctx, sub); err != nil {
			return err
		}
		return testStore.SyncWebhookEventSubscriptions(ctx, orgID, sub.ID, sub.Events)
	}))
	return sub.ID
}

func webhookDeliveries(t *testing.T, pool *pgxpool.Pool, subscriptionID string) []business.WebhookDelivery {
	t.Helper()
	rows, err := pool.Query(testCtx, `
		SELECT event_id, event_type, payload
		FROM public.webhook_deliveries
		WHERE subscription_id = $1
		ORDER BY created_at`, subscriptionID)
	require.NoError(t, err)
	defer rows.Close()
	var out []business.WebhookDelivery
	for rows.Next() {
		var d business.WebhookDelivery
		require.NoError(t, rows.Scan(&d.EventID, &d.EventType, &d.Payload))
		out = append(out, d)
	}
	require.NoError(t, rows.Err())
	return out
}

func publishExternal(t *testing.T, transport *infra.PostgresEventTransport, eventType, tenant string, data []byte) *events.EventEnvelope {
	t.Helper()
	event := &events.EventEnvelope{
		Id: uuid.NewString(), Type: eventType, Source: "saas.accounts",
		Specversion: "1.0", Datacontenttype: "application/json",
		Time: timestamppb.New(time.Now().UTC()), TenantId: tenant, PartitionKey: tenant, Data: data,
	}
	// A nil caller tx commits the event and drains the relay inline, so fan-out
	// is complete when Publish returns.
	require.NoError(t, transport.Publish(testCtx, nil, event))
	return event
}

// TestPostgresRelayDeliversExternalEventToWebhookEndpoint is the convergence
// acceptance case: an endpoint subscribed to an external type receives the event
// through the ordinary relay, as a pending delivery plus the dispatch job the
// worker already knows how to run. The persisted body is the delivery envelope
// wrapping the published data verbatim, which is what keeps the bytes an
// endpoint verifies identical to the ones the inline fan-out used to produce.
func TestPostgresRelayDeliversExternalEventToWebhookEndpoint(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	endpointID := seedWebhookEndpoint(t, orgID, true, externalEventType)

	data := []byte(`{"event_type":"saas.api_key.created","payload":{"key_id":"k1"}}`)
	event := publishExternal(t, transport, externalEventType, orgID, data)

	deliveries := webhookDeliveries(t, pool, endpointID)
	require.Len(t, deliveries, 1, "one delivery per subscribed endpoint")
	require.Equal(t, event.GetId(), deliveries[0].EventID,
		"the envelope id is the X-Webhook-Event-ID an endpoint deduplicates on")
	require.Equal(t, externalEventType, deliveries[0].EventType)

	var body struct {
		EventID   string          `json:"id"`
		EventType string          `json:"event_type"`
		Data      json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(deliveries[0].Payload), &body))
	require.Equal(t, event.GetId(), body.EventID)
	require.Equal(t, externalEventType, body.EventType)
	require.JSONEq(t, string(data), string(body.Data),
		"the published event data is the delivery body's data member, unmodified")

	var jobs int
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT count(*) FROM public.job_messages
		WHERE queue = $1 AND topic = $2 AND idempotency_key IN (
			SELECT id::text FROM public.webhook_deliveries WHERE subscription_id = $3)`,
		business.OutboundWebhookQueue, business.OutboundWebhookTopic, endpointID,
	).Scan(&jobs))
	require.Equal(t, 1, jobs, "the relay enqueues the dispatcher's own job for the delivery")
}

// TestPostgresRelayConfinesWebhookDeliveryToItsTenant is the isolation case a
// type pattern cannot express. The relay runs with RLS bypassed and resolves
// subscriptions across every tenant, so the subscription's org is the only thing
// standing between one tenant's event and another tenant's endpoint.
func TestPostgresRelayConfinesWebhookDeliveryToItsTenant(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	subscribedOrg := seedOrg(t, seedUser(t))
	otherOrg := seedOrg(t, seedUser(t))
	endpointID := seedWebhookEndpoint(t, otherOrg, true, externalEventType)

	publishExternal(t, transport, externalEventType, subscribedOrg, []byte(`{}`))

	require.Empty(t, webhookDeliveries(t, pool, endpointID),
		"an endpoint never receives another tenant's event")
}

// TestPostgresRelaySkipsIneligibleWebhookSubscriptions covers the two gates that
// stand between a matching pattern and an outbound request: the endpoint has to
// be active, and the type has to be declared external. Deactivating an endpoint
// stops delivery without rewriting a subscription row, and a type that is only
// tenant-visible never leaves the platform even when an endpoint names it.
func TestPostgresRelaySkipsIneligibleWebhookSubscriptions(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	inactive := seedWebhookEndpoint(t, orgID, false, externalEventType)
	internalTyped := seedWebhookEndpoint(t, orgID, true, tenantOnlyEventType)

	publishExternal(t, transport, externalEventType, orgID, []byte(`{}`))
	publishExternal(t, transport, tenantOnlyEventType, orgID, []byte(`{}`))

	require.Empty(t, webhookDeliveries(t, pool, inactive),
		"an inactive endpoint receives nothing")
	require.Empty(t, webhookDeliveries(t, pool, internalTyped),
		"a type that is not declared external is never delivered to an endpoint")
}

// TestSyncWebhookEventSubscriptionsDerivesRowsFromRegistration pins the derived
// state: one row per registered name, names that cannot be a domain event type
// skipped, and a re-sync that removes a name removing its row. Delivery is
// driven by these rows, so a name without one is a name that never fires.
func TestSyncWebhookEventSubscriptionsDerivesRowsFromRegistration(t *testing.T) {
	orgID := seedOrg(t, seedUser(t))
	// "legacy-name" is a valid webhook event name and cannot be a domain event
	// type, so it has never matched a registered event and gets no row.
	endpointID := seedWebhookEndpoint(t, orgID, true, externalEventType, "saas.org.created", "legacy-name")

	require.Equal(t,
		[]string{externalEventType, "saas.org.created"},
		webhookSubscriptionPatterns(t, endpointID),
		"one subscription per registered name that can be an event type")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.SyncWebhookEventSubscriptions(ctx, orgID, endpointID, []string{externalEventType})
	}))
	require.Equal(t, []string{externalEventType}, webhookSubscriptionPatterns(t, endpointID),
		"a name removed from the registration loses its subscription")
}

func webhookSubscriptionPatterns(t *testing.T, endpointID string) []string {
	t.Helper()
	var patterns []string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		rows, err := tx.Query(ctx, `
			SELECT type_pattern FROM public.event_subscriptions
			WHERE webhook_subscription_id = $1 AND revoked_at IS NULL
			ORDER BY type_pattern`, endpointID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pattern string
			if err := rows.Scan(&pattern); err != nil {
				return err
			}
			patterns = append(patterns, pattern)
		}
		return rows.Err()
	}))
	return patterns
}

// TestAuditPublishTakesNoPartitionLock pins the reason the audit envelope
// carries no partition key. publish_domain_event takes a per-partition advisory
// lock, held until the producer's transaction commits, for any event that names
// one. Keying the audit event on its organization would therefore serialize
// every audited mutation in that organization against every other one, for an
// ordering nothing consumes — an outbound webhook is dispatched in
// subscription-id order, and a module cannot subscribe the platform namespace.
//
// The assertion is on the stored partition key rather than on timing, because
// the lock does not fail a mutation, it only queues it: a timing test would be
// flaky in exactly the conditions that matter.
func TestAuditPublishTakesNoPartitionLock(t *testing.T) {
	transport := auditRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore,
		business.WithDomainEventTransport(transport))
	require.NoError(t, err)

	entryID := business.NewIDString()
	emitter.Emit(testCtx, business.AuditEntry{
		ID: entryID, OrgID: orgID, ActorID: business.NewIDString(), ActorType: "user",
		EventType: business.EventSessionRevoked, Resource: "session", ResourceID: entryID,
		CreatedAt: time.Now().UTC(),
	})

	var partitionKey string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		return tx.QueryRow(ctx,
			`SELECT partition_key FROM public.domain_events WHERE id = $1::uuid`, entryID).Scan(&partitionKey)
	}))
	require.Empty(t, partitionKey,
		"an audit-derived event must not take a per-organization advisory lock on the mutation path")
}

// TestPostgresRelaySurvivesEndpointDeletedMidFanOut is the race the subscription
// scan cannot lock away: it reads live subscriptions without a row lock, so an
// endpoint can be deleted between that read and the delivery insert. The insert
// then violates webhook_deliveries' foreign key, and a foreign-key violation
// aborts whatever subtransaction issued it — so the delivery must be written on
// a savepoint of its own, or the event's other subscribers are dragged down with
// an endpoint that no longer exists.
func TestPostgresRelaySurvivesEndpointDeletedMidFanOut(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	doomed := seedWebhookEndpoint(t, orgID, true, externalEventType)
	survivor := seedWebhookEndpoint(t, orgID, true, externalEventType)

	// Publish inside a caller transaction so the event is committed but not yet
	// relayed, then delete one endpoint before draining. The subscription row
	// cascades away with it, reproducing the state the scan-then-insert race
	// lands in.
	event := &events.EventEnvelope{
		Id: uuid.NewString(), Type: externalEventType, Source: "saas.accounts",
		Specversion: "1.0", Datacontenttype: "application/json",
		Time: timestamppb.New(time.Now().UTC()), TenantId: orgID, Data: []byte(`{}`),
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return transport.Publish(ctx, ctx.Value("tx"), event) //nolint:staticcheck // shared "tx" key
	}))
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.DeleteWebhookSubscription(ctx, doomed)
	}))

	relayed, err := transport.RelayOnce(testCtx)
	require.NoError(t, err, "a deleted endpoint must not fail the relay")
	require.GreaterOrEqual(t, relayed, 1)

	require.Len(t, webhookDeliveries(t, pool, survivor), 1,
		"the surviving endpoint still receives the event")
	var published bool
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT published_at IS NOT NULL FROM public.domain_events WHERE id = $1::uuid`,
		event.GetId()).Scan(&published))
	require.True(t, published, "the event is fanned out, not parked behind a deleted endpoint")
}

// TestPostgresRelayDoesNotRedeliverToAnEndpointWithHistory pins the dedupe that
// makes ReplayEvents safe to run twice: a second fan-out of the same event
// creates no second delivery, and the event still completes.
func TestPostgresRelayDoesNotRedeliverToAnEndpointWithHistory(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	endpointID := seedWebhookEndpoint(t, orgID, true, externalEventType)

	event := publishExternal(t, transport, externalEventType, orgID, []byte(`{}`))
	require.Len(t, webhookDeliveries(t, pool, endpointID), 1)

	replayed, err := transport.Replay(testCtx, events.ReplaySelector{
		Type: externalEventType, TenantID: orgID,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, replayed, 1, "replay walks the event")
	require.Len(t, webhookDeliveries(t, pool, endpointID), 1,
		"an endpoint that already has history for this event gets no second delivery")
	require.NotEmpty(t, event.GetId())
}

// TestWebhookSubscriptionQueueIsPinnedToTheDispatcher guards the column against
// lying. The relay does not route a webhook row by its queue — the dispatcher
// owns that — so a row naming any other queue would display a route it never
// takes.
func TestWebhookSubscriptionQueueIsPinnedToTheDispatcher(t *testing.T) {
	orgID := seedOrg(t, seedUser(t))
	endpointID := seedWebhookEndpoint(t, orgID, true, externalEventType)

	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, e := tx.Exec(ctx, `
			INSERT INTO public.event_subscriptions
				(subscriber_principal_id, type_pattern, queue, delivery, org_id, webhook_subscription_id)
			VALUES (NULL, $1, 'not.the.dispatcher', 'webhook', $2::uuid, $3::uuid)`,
			"saas.org.created", orgID, endpointID)
		return e
	})
	require.Error(t, err, "a webhook subscription may not name a queue the dispatcher does not serve")
}

// TestRequireWebhookRelayRefusesATransportWithNoDispatcher is the startup guard.
// A transport that can resolve webhook subscriptions but cannot deliver to them
// fans out to module queues and silently never to endpoints; the assertion turns
// that mis-wiring into a boot failure instead of invisible loss.
func TestRequireWebhookRelayRefusesATransportWithNoDispatcher(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	bare := infra.NewPostgresEventTransport(store, pool, "relay-bare-"+uuid.NewString(), time.Second)
	require.Error(t, bare.RequireWebhookRelay())

	wired := infra.NewPostgresEventTransport(store, pool, "relay-wired-"+uuid.NewString(), time.Second,
		infra.WithWebhookRelay(infra.NewPostgresWebhookRelay(testStore)))
	require.NoError(t, wired.RequireWebhookRelay())
}

// A user-scoped tenant transaction has no organization scope. SQL NULL must
// deny the mutation just as an explicitly different organization does.
func TestSyncWebhookEventSubscriptionsRejectsMissingTenantScope(t *testing.T) {
	orgID := seedOrg(t, seedUser(t))
	endpointID := seedWebhookEndpoint(t, orgID, true, externalEventType)
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE app_tenant"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_org_id', '', true)"); err != nil {
			return err
		}
		return testStore.SyncWebhookEventSubscriptions(ctx, orgID, endpointID, nil)
	})
	require.ErrorContains(t, err, "webhook subscription org does not match the signed request scope")
	require.Equal(t, []string{externalEventType}, webhookSubscriptionPatterns(t, endpointID))
}
