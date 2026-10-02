//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The relay half of #954, against Postgres.
//
// A solution- or module-declared audit event type is registered at runtime, in
// audit_event_types, where module-compose never saw it — so the compiled event
// catalog cannot answer whether it may leave the platform and the relay must
// read the row. These are the only tests that exercise that read: the unit
// gates run over a recording transport and touch no database, which is exactly
// why the relay path shipped broken once already. The lookup short-circuits on
// the compiled catalog, so every code-owned type keeps working and only this
// path reaches the table — a role that cannot select it fails here and nowhere
// else.

// admitDeclaredType writes the audit_event_types row admission would write, at
// the given visibility, under an owner that marks it solution-declared.
func admitDeclaredType(t *testing.T, visibility string) business.EventType {
	t.Helper()
	namespace := "example" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	declared := business.DeclaredAuditEventType{
		Type:       business.EventType(namespace + ".item.created"),
		Namespace:  namespace,
		SolutionID: namespace,
		Visibility: visibility,
		Retention:  business.RetentionSecurity,
		Fields:     []business.PayloadField{{Name: "count", Kind: business.FieldInt}},
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.PutDeclaredAuditEventType(ctx, declared)
	}))

	// Read back through the ordinary path: the row must carry the visibility the
	// gates read, not whatever a default would have supplied.
	var admitted *business.DeclaredAuditEventType
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		admitted, err = testStore.GetDeclaredAuditEventType(ctx, declared.Type)
		return err
	}))
	require.NotNil(t, admitted)
	require.Equal(t, visibility, admitted.Visibility)
	require.Equal(t, business.RetentionSecurity, admitted.Retention, "and the retention class it was declared with")
	return declared.Type
}

// The acceptance case this issue exists for: a declared type with external
// visibility reaches a tenant's endpoint through the ordinary relay. It failed
// before the fix because the emitter published nothing for it; it fails without
// migration 10's grant because the relay cannot read the registry that says it
// is deliverable, and a refused read aborts the whole relay pass.
func TestPostgresRelayDeliversDeclaredExternalType(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	eventType := admitDeclaredType(t, business.AuditVisibilityExternal)
	endpointID := seedWebhookEndpoint(t, orgID, true, string(eventType))

	data := []byte(`{"event_type":"` + string(eventType) + `","payload":{"count":1}}`)
	event := publishExternal(t, transport, string(eventType), orgID, data)

	deliveries := webhookDeliveries(t, pool, endpointID)
	require.Len(t, deliveries, 1,
		"a declared type the producer marked external and the operator granted must reach the endpoint")
	require.Equal(t, event.GetId(), deliveries[0].EventID)
	require.Equal(t, string(eventType), deliveries[0].EventType)
}

// The other half of the same decision: a declared type left at the default
// visibility is registered, readable in the tenant's audit log, and never
// delivered — even to an endpoint that named it. The subscription is refused at
// CreateSubscription, but a row written before the type was declared, or by the
// store's deliberately permissive sync, still has to be stopped here.
func TestPostgresRelayWithholdsDeclaredTenantType(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	eventType := admitDeclaredType(t, business.AuditVisibilityTenant)
	endpointID := seedWebhookEndpoint(t, orgID, true, string(eventType))

	publishExternal(t, transport, string(eventType), orgID, []byte(`{"payload":{"count":1}}`))

	require.Empty(t, webhookDeliveries(t, pool, endpointID),
		"a tenant-visible declared type must never leave the platform")
}

// A type in neither half of the registry is not deliverable: eligibility is
// granted by declaration, never by omission. This is also the path that proves
// a missing row is read as "no" rather than as an error — the relay must still
// drain, marking the event published with no delivery.
func TestPostgresRelayWithholdsUnregisteredType(t *testing.T) {
	transport, pool := newWebhookRelayTransport(t)
	orgID := seedOrg(t, seedUser(t))
	eventType := "example" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8] + ".item.created"
	endpointID := seedWebhookEndpoint(t, orgID, true, eventType)

	event := publishExternal(t, transport, eventType, orgID, []byte(`{"payload":{}}`))

	require.Empty(t, webhookDeliveries(t, pool, endpointID),
		"an unregistered type must not be delivered")
	var published bool
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT published_at IS NOT NULL FROM public.domain_events WHERE id = $1::uuid`,
		event.GetId()).Scan(&published))
	require.True(t, published,
		"the relay must drain an undeliverable event rather than abort on the missing row")
}
