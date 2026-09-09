package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A security mutation and the record of it are one fact. These tests hold that
// against a real database: with the audit write inside the mutation's own
// transaction, an audit or fan-out failure takes the mutation with it, a
// cancelled request leaves nothing behind, and a retry of an idempotent
// mutation does not invent a second, contradictory record.

// auditedService returns a Service on the shared test store wired to emitter.
func auditedService(t *testing.T, emitter business.AuditEmitter) *business.Service {
	t.Helper()
	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetAuditEmitter(emitter)
	return service
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx, query, args...).Scan(&count)
	}))
	return count
}

func countAuditEvents(t *testing.T, eventType, resourceID string) int {
	t.Helper()
	return countRows(t,
		`SELECT count(*) FROM audit_events WHERE event_type = $1 AND resource_id = $2`,
		eventType, resourceID)
}

// seedWebhookSubscription registers an org endpoint for one event type so the
// fan-out half of the emitter has work to do.
func seedWebhookSubscription(t *testing.T, orgID, eventType string) string {
	t.Helper()
	id := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateWebhookSubscription(ctx, &business.WebhookSubscription{
			ID:              id,
			OrgID:           orgID,
			URL:             "https://example.com/hooks/" + id,
			SecretEncrypted: "secret",
			Events:          []string{eventType},
			Active:          true,
		})
	}))
	return id
}

func createRoleRequest(orgID string) *gen.CreateRoleRequest {
	return &gen.CreateRoleRequest{
		OrgId:       orgID,
		Name:        "auditor-" + business.NewIDString()[:8],
		Description: "role created by the audit atomicity suite",
		Permissions: []*gen.Permission{{Resource: "audit", Action: "read"}},
	}
}

// TestSecurityMutation_AuditFailureRollsBackTheDomainWrite is the core claim:
// a role that cannot be recorded is not created.
func TestSecurityMutation_AuditFailureRollsBackTheDomainWrite(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	service := auditedService(t, failingTxEmitter{})

	response, err := service.CreateRole(testCtx, owner, createRoleRequest(orgID))
	require.Error(t, err, "a failed audit write must fail the mutation (fail-closed)")
	require.Nil(t, response)

	require.Zero(t, countRows(t, `SELECT count(*) FROM roles WHERE org_id = $1`, orgID),
		"the role must roll back when its audit event cannot commit")
	require.Zero(t, countRows(t, `SELECT count(*) FROM audit_events WHERE org_id = $1`, orgID),
		"no audit row may survive the rolled-back mutation")
}

// TestSecurityMutation_CommitsDomainAuditAndFanOutTogether is the positive half:
// on success all three land, in one transaction.
func TestSecurityMutation_CommitsDomainAuditAndFanOutTogether(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	subscriptionID := seedWebhookSubscription(t, orgID, string(business.EventRoleCreated))

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	response, err := service.CreateRole(testCtx, owner, createRoleRequest(orgID))
	require.NoError(t, err)

	require.Equal(t, 1, countRows(t, `SELECT count(*) FROM roles WHERE id = $1`, response.GetRole().GetId()))
	require.Equal(t, 1, countAuditEvents(t, string(business.EventRoleCreated), response.GetRole().GetId()))
	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, subscriptionID),
		"the subscribed endpoint must have a durable delivery from the same transaction")
}

// TestSecurityMutation_FanOutFailureRollsBackDomainAndAudit injects the failure
// one step later than the audit insert: the audit row is staged, the outbox
// enqueue fails, and nothing at all commits — no half-written audit/delivery set.
func TestSecurityMutation_FanOutFailureRollsBackDomainAndAudit(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	subscriptionID := seedWebhookSubscription(t, orgID, string(business.EventRoleCreated))

	emitter, err := business.NewDurableAuditEmitter(testStore, rejectingWebhookJobProducer{})
	require.NoError(t, err)
	service := auditedService(t, emitter)

	_, err = service.CreateRole(testCtx, owner, createRoleRequest(orgID))
	require.Error(t, err, "a fan-out that cannot be enqueued must fail the mutation")

	require.Zero(t, countRows(t, `SELECT count(*) FROM roles WHERE org_id = $1`, orgID))
	require.Zero(t, countRows(t, `SELECT count(*) FROM audit_events WHERE org_id = $1`, orgID),
		"the staged audit row must roll back with the fan-out")
	require.Zero(t, countRows(t,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, subscriptionID))
}

// TestSecurityMutation_CancelledRequestCommitsNothing covers the crash boundary
// a cancelled request stands in for: the caller goes away mid-mutation and
// neither the change nor its record is left behind.
func TestSecurityMutation_CancelledRequestCommitsNothing(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	cancelled, cancel := context.WithCancel(testCtx)
	cancel()

	_, err = service.CreateRole(cancelled, owner, createRoleRequest(orgID))
	require.Error(t, err)

	require.Zero(t, countRows(t, `SELECT count(*) FROM roles WHERE org_id = $1`, orgID))
	require.Zero(t, countRows(t, `SELECT count(*) FROM audit_events WHERE org_id = $1`, orgID))
}

// TestSecurityMutation_UserScopedWriteRecordsInItsOwnTransaction covers the
// user-scoped half of the estate — the mutations with no tenant, whose audit
// rows carry a NULL org. They commit on the mutation's own user-scoped
// transaction (migration 122), not a second control-plane one.
func TestSecurityMutation_UserScopedWriteRecordsInItsOwnTransaction(t *testing.T) {
	userID := seedUser(t)

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	require.NoError(t, service.AcceptTerms(testCtx, userID, business.CurrentTermsVersion, "test-suite"))

	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM users WHERE uuid = $1 AND terms_version = $2`,
		userID, business.CurrentTermsVersion))
	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM audit_events WHERE event_type = $1 AND actor_id = $2 AND org_id IS NULL`,
		string(business.EventConsentTerms), userID),
		"the user-scoped transaction must be able to commit its own control-scope audit row")
}

// TestSecurityMutation_UserScopedAuditFailureRollsBackTheDomainWrite is the
// fail-closed half of the same path.
func TestSecurityMutation_UserScopedAuditFailureRollsBackTheDomainWrite(t *testing.T) {
	userID := seedUser(t)
	service := auditedService(t, failingTxEmitter{})

	require.Error(t, service.AcceptTerms(testCtx, userID, business.CurrentTermsVersion, "test-suite"))

	require.Zero(t, countRows(t,
		`SELECT count(*) FROM users WHERE uuid = $1 AND terms_version IS NOT NULL`, userID),
		"consent must not be recorded on the user when its audit event cannot commit")
}

// TestSecurityMutation_RetriedIdempotentWriteRecordsItOnce exercises the lost
// response: the mutation committed, the caller never saw it and repeats. The
// second call is a no-op, so the trail keeps exactly one revocation — a retry
// cannot manufacture a second, contradictory record.
func TestSecurityMutation_RetriedIdempotentWriteRecordsItOnce(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	principal, err := service.CreateAgentPrincipal(testCtx, business.CreateAgentRequest{
		OrgID:           orgID,
		AgentIdentifier: "example/agent:1.0.0",
		DisplayName:     "Example Agent",
		CreatedBy:       owner,
	})
	require.NoError(t, err)
	require.Equal(t, 1, countAuditEvents(t, string(business.EventPrincipalCreated), principal.ID))

	require.NoError(t, service.RevokePrincipal(testCtx, principal.ID, "rotating credentials"))
	require.NoError(t, service.RevokePrincipal(testCtx, principal.ID, "rotating credentials"))

	require.Equal(t, 1, countAuditEvents(t, string(business.EventPrincipalRevoked), principal.ID),
		"a repeated revoke changes nothing, so it must add no second revocation record")
}

// TestSecurityMutation_FanOutStaysWithinTheEventTenant guards the widening this
// change makes: a control-plane security mutation reads webhook subscriptions
// with RLS bypassed, so the fan-out must scope itself to the event's own tenant
// rather than trusting the policy to do it.
func TestSecurityMutation_FanOutStaysWithinTheEventTenant(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	otherOwner := seedUser(t)
	otherOrgID := seedOrg(t, otherOwner)

	subscriptionID := seedWebhookSubscription(t, orgID, string(business.EventPrincipalRevoked))
	foreignSubscriptionID := seedWebhookSubscription(t, otherOrgID, string(business.EventPrincipalRevoked))

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	principal, err := service.CreateAgentPrincipal(testCtx, business.CreateAgentRequest{
		OrgID:           orgID,
		AgentIdentifier: "example/tenant-scoped-agent:1.0.0",
		CreatedBy:       owner,
	})
	require.NoError(t, err)
	require.NoError(t, service.RevokePrincipal(testCtx, principal.ID, "end of engagement"))

	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, subscriptionID))
	require.Zero(t, countRows(t,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, foreignSubscriptionID),
		"another tenant's endpoint must never receive this org's audit event")
}

// TestControlScopeAuditRowIsPinnedToTheActingUser guards the widening migration
// 122 makes. A user-scoped transaction may commit a NULL-org audit row, but only
// one attributed to the user it is running as — it cannot mint control-scope
// history for anyone else, and it still cannot read those rows back.
func TestControlScopeAuditRowIsPinnedToTheActingUser(t *testing.T) {
	userID := seedUser(t)
	otherUserID := seedUser(t)

	own := testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ID: business.NewIDString(), EventType: business.EventConsentTerms,
			ActorID: userID, ActorType: "user", Resource: "user", ResourceID: userID,
		})
	})
	require.NoError(t, own, "a user-scoped transaction must be able to record its own control-scope event")

	impersonated := testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ID: business.NewIDString(), EventType: business.EventConsentTerms,
			ActorID: otherUserID, ActorType: "user", Resource: "user", ResourceID: otherUserID,
		})
	})
	require.Error(t, impersonated, "a user-scoped transaction must not attribute a control-scope event to another user")

	var visible int
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_events WHERE org_id IS NULL`).Scan(&visible)
	}))
	require.Zero(t, visible, "NULL-org rows must stay invisible to tenant reads")
}
