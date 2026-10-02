//go:build !pure

package pgauth_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// newResolver is the production wiring: the resolver records its events
// through the audit emitter, in whichever mode opts select.
func newResolver(t *testing.T, opts ...business.DurableAuditEmitterOption) *pgauth.Resolver {
	t.Helper()
	recorder, err := business.NewDurableAuditEmitter(testStore, testStore, opts...)
	require.NoError(t, err)
	r := pgauth.NewResolver(testStore)
	r.SetAuditRecorder(recorder)
	return r
}

func TestResolver_RefusesWithoutAnAuditRecorder(t *testing.T) {
	r := pgauth.NewResolver(testStore)
	_, err := r.Resolve(context.Background(), claims("nobody@test.local", "dev-nobody"), auth.SignupIntent{})
	require.ErrorContains(t, err, "audit recorder is required",
		"a resolver that cannot record a registration must not provision one")
}

type queuedRow struct {
	eventType  string
	actorID    string
	resource   string
	resourceID string
	orgID      string
	payload    map[string]any
}

// drainQueueFor reads and removes the queued events for one organization ("" is
// the platform's), as the relay — the job worker — does. Writers cannot read
// the queue back at all.
func drainQueueFor(t *testing.T, orgID string) []queuedRow {
	t.Helper()
	ctx := context.Background()
	pool, err := infra.NewAuditRelayPool(ctx)
	require.NoError(t, err)
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT event_type, COALESCE(actor_id::text, ''), resource, COALESCE(resource_id, ''),
		       COALESCE(org_id::text, ''), payload
		  FROM audit_event_queue
		 WHERE COALESCE(org_id::text, '') = $1
		 ORDER BY seq`, orgID)
	require.NoError(t, err)
	var out []queuedRow
	for rows.Next() {
		var row queuedRow
		var payload []byte
		require.NoError(t, rows.Scan(&row.eventType, &row.actorID, &row.resource, &row.resourceID, &row.orgID, &payload))
		require.NoError(t, json.Unmarshal(payload, &row.payload))
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	_, err = pool.Exec(ctx, `DELETE FROM audit_event_queue WHERE COALESCE(org_id::text, '') = $1`, orgID)
	require.NoError(t, err)
	return out
}

// Under a swap value the resolver's two events reach the queue, on the
// resolution transaction, and audit_events receives nothing — the raw SQL they
// were written with before would have kept them in audit_events, out of reach
// of the warehouse and the archive.
func TestResolver_UnderTheSwap_RecordsSsoProvisioningInTheQueue(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := newResolver(t, business.WithQueuedRecords())

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	setSsoProvisioning(t, orgID, "jit", "member", []string{"acme.test"})

	identity, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)

	require.Zero(t, registeredAuditCount(t, orgID), "audit_events receives no new rows under a swap value")
	require.Zero(t, ssoJitAuditCount(t, orgID))

	queued := drainQueueFor(t, orgID.String())
	require.Equal(t, []queuedRow{
		{
			eventType: string(business.EventAuthSSOJitProvisioned), actorID: identity.UserID.String(),
			resource: "organization", resourceID: orgID.String(), orgID: orgID.String(),
			payload: map[string]any{"provider": "dev"},
		},
		{
			eventType: string(business.EventUserRegistered), actorID: identity.UserID.String(),
			resource: "user", resourceID: identity.UserID.String(), orgID: orgID.String(),
			payload: map[string]any{"email": "worker@acme.test", "signup_method": "sso"},
		},
	}, queued, "the same two records the raw SQL wrote, in the order the resolution wrote them")
}

// A registration with no organization is a platform event; under a swap value
// it reaches the queue too, as the actor-scoped row audit_events would have held.
func TestResolver_UnderTheSwap_RecordsAnOrglessRegistrationInTheQueue(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := newResolver(t, business.WithQueuedRecords())

	identity, err := r.Resolve(ctx, claims("solo@test.local", "dev-solo"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, identity.OrgID)

	var inAuditEvents int
	scanControlPlane(t, &inAuditEvents,
		`SELECT COUNT(*) FROM audit_events WHERE event_type = 'saas.user.registered' AND actor_id = $1`, identity.UserID)
	require.Zero(t, inAuditEvents)

	var registrations []queuedRow
	for _, row := range drainQueueFor(t, "") {
		if row.actorID == identity.UserID.String() {
			registrations = append(registrations, row)
		}
	}
	require.Len(t, registrations, 1)
	require.Equal(t, string(business.EventUserRegistered), registrations[0].eventType)
	require.Equal(t, "solo@test.local", registrations[0].payload["email"])
}
