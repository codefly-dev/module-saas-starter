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
