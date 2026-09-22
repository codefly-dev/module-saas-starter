//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/events"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
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
		if err := testStore.CreateWebhookSubscription(ctx, &business.WebhookSubscription{
			ID:              id,
			OrgID:           orgID,
			URL:             "https://example.com/hooks/" + id,
			SecretEncrypted: "secret",
			Events:          []string{eventType},
			Active:          true,
		}); err != nil {
			return err
		}
		return testStore.SyncWebhookEventSubscriptions(ctx, orgID, id, []string{eventType})
	}))
	return id
}

// auditRelayTransport is the events transport the emitter publishes into and the
// relay delivers from, wired to the outbound dispatcher so a published event
// reaches a subscribed endpoint.
func auditRelayTransport(t *testing.T) *infra.PostgresEventTransport {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return infra.NewPostgresEventTransport(
		infra.NewPostgresJobStore(pool), pool, "audit-atomicity-"+business.NewIDString(), time.Second,
		infra.WithWebhookRelay(infra.NewPostgresWebhookRelay(testStore)),
	)
}

// rejectingEventTransport fails the publish that the audit write stages beside
// itself, standing in for an events subsystem that cannot accept the fact.
type rejectingEventTransport struct{ events.Transport }

func (rejectingEventTransport) Publish(context.Context, events.TxHandle, *events.EventEnvelope) error {
	return errors.New("publish rejected")
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

// TestSecurityMutation_CommitsDomainAuditAndEventTogether is the positive half:
// on success the mutation, its record, and the event that carries it to
// subscribers all land in one transaction. Delivery is the relay's work
// afterwards, so the endpoint's history appears once the relay has run.
func TestSecurityMutation_CommitsDomainAuditAndEventTogether(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	subscriptionID := seedWebhookSubscription(t, orgID, string(business.EventRoleCreated))

	transport := auditRelayTransport(t)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore,
		business.WithDomainEventTransport(transport))
	require.NoError(t, err)
	service := auditedService(t, emitter)

	response, err := service.CreateRole(testCtx, owner, createRoleRequest(orgID))
	require.NoError(t, err)

	require.Equal(t, 1, countRows(t, `SELECT count(*) FROM roles WHERE id = $1`, response.GetRole().GetId()))
	require.Equal(t, 1, countAuditEvents(t, string(business.EventRoleCreated), response.GetRole().GetId()))
	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM domain_events WHERE type = $1 AND tenant_id = $2`,
		string(business.EventRoleCreated), orgID),
		"the event must be published from the mutation's own transaction")

	_, err = transport.RelayOnce(testCtx)
	require.NoError(t, err)
	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, subscriptionID),
		"the subscribed endpoint receives the event through the relay")
}

// TestSecurityMutation_PublishFailureRollsBackDomainAndAudit injects the failure
// one step later than the audit insert: the audit row is staged, the publish
// fails, and nothing at all commits — no record of a fact that no subscriber can
// ever be told about.
func TestSecurityMutation_PublishFailureRollsBackDomainAndAudit(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore,
		business.WithDomainEventTransport(rejectingEventTransport{}))
	require.NoError(t, err)
	service := auditedService(t, emitter)

	_, err = service.CreateRole(testCtx, owner, createRoleRequest(orgID))
	require.Error(t, err, "an event that cannot be published must fail the mutation")

	require.Zero(t, countRows(t, `SELECT count(*) FROM roles WHERE org_id = $1`, orgID))
	require.Zero(t, countRows(t, `SELECT count(*) FROM audit_events WHERE org_id = $1`, orgID),
		"the staged audit row must roll back with the publish")
	require.Zero(t, countRows(t,
		`SELECT count(*) FROM domain_events WHERE tenant_id = $1`, orgID))
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

	transport := auditRelayTransport(t)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore,
		business.WithDomainEventTransport(transport))
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

// TestSecurityMutation_FanOutStaysWithinTheEventTenant guards the tenant gate the
// relay applies: it resolves subscriptions across every tenant with RLS
// bypassed, so the subscription's own org is what confines a control-plane
// security mutation's event to the tenant it happened in.
func TestSecurityMutation_FanOutStaysWithinTheEventTenant(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	otherOwner := seedUser(t)
	otherOrgID := seedOrg(t, otherOwner)

	subscriptionID := seedWebhookSubscription(t, orgID, string(business.EventPrincipalRevoked))
	foreignSubscriptionID := seedWebhookSubscription(t, otherOrgID, string(business.EventPrincipalRevoked))

	transport := auditRelayTransport(t)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore,
		business.WithDomainEventTransport(transport))
	require.NoError(t, err)
	service := auditedService(t, emitter)

	principal, err := service.CreateAgentPrincipal(testCtx, business.CreateAgentRequest{
		OrgID:           orgID,
		AgentIdentifier: "example/tenant-scoped-agent:1.0.0",
		CreatedBy:       owner,
	})
	require.NoError(t, err)
	require.NoError(t, service.RevokePrincipal(testCtx, principal.ID, "end of engagement"))

	_, err = transport.RelayOnce(testCtx)
	require.NoError(t, err)

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

// TestSecurityMutation_HumanPrincipalRevokeRecordsInControlScope covers the
// other half of the principal estate. principals_org_scope (migration 36) makes
// org_id NULL for humans and NOT NULL for agents, so the two lifecycles need
// different transaction scopes: an agent's event carries an org and its fan-out
// must be enqueued from tenant traffic, while a human's carries none. Revoking a
// human is the revocation an incident reaches for first, and an org-only scope
// rule turns it into a NotFound that reads as "already handled".
func TestSecurityMutation_HumanPrincipalRevokeRecordsInControlScope(t *testing.T) {
	userID := seedUser(t)

	var kind string
	var org *string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx,
			`SELECT kind, org_id FROM principals WHERE id = $1`, userID).Scan(&kind, &org)
	}))
	require.Equal(t, "human", kind, "seeding a user must give it a human principal")
	require.Nil(t, org, "a human principal is org-less by schema CHECK")

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	require.NoError(t, service.RevokePrincipal(testCtx, userID, "credential compromise"),
		"an org-less human principal must still be revocable")

	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM principals WHERE id = $1 AND revoked_at IS NOT NULL`, userID))
	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM audit_events WHERE event_type = $1 AND resource_id = $2 AND org_id IS NULL`,
		string(business.EventPrincipalRevoked), userID),
		"the revocation must be recorded on the control-scope spine")

	// A repeat is a no-op and records nothing further, on this scope too.
	require.NoError(t, service.RevokePrincipal(testCtx, userID, "credential compromise"))
	require.Equal(t, 1, countAuditEvents(t, string(business.EventPrincipalRevoked), userID))
}

// TestSecurityMutation_DisableRejectsHumanPrincipalAsConflict pins the error the
// disable path reports for a non-agent. Resolving scope before the kind guard
// made this a NotFound, which the RPC layer maps to codes.NotFound rather than
// codes.AlreadyExists (see mapPrincipalError) — a silent contract change for
// any client branching on it.
func TestSecurityMutation_DisableRejectsHumanPrincipalAsConflict(t *testing.T) {
	userID := seedUser(t)
	service := auditedService(t, &recordingEmitter{})

	err := service.DisableAgentPrincipal(testCtx, userID, "not an agent")
	require.Error(t, err)
	require.Equal(t, business.ErrTypeConflict, storeErrTypeInfra(t, err),
		"a human principal must be rejected as a conflict, not reported missing")
}

// TestSecurityMutation_RegistrationRecordDoesNotHingeOnTheRoleCatalog pins where
// the registration record lives. Attaching it to the admin-role assignment made
// the record — and therefore registration itself — conditional on the built-in
// role catalog already being imported, so a fresh environment would fail every
// signup. The record belongs to the org bootstrap, which always runs.
func TestSecurityMutation_RegistrationRecordDoesNotHingeOnTheRoleCatalog(t *testing.T) {
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	var provider string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx, `SELECT provider_id FROM identity_providers LIMIT 1`).Scan(&provider)
	}))

	email := "registrant-" + business.NewIDString() + "@example.com"
	response, err := service.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider:      provider,
			ProviderId:    business.NewIDString(),
			ProviderEmail: email,
			EmailVerified: true,
		},
	})
	require.NoError(t, err)
	userID := response.GetUser().GetUuid()

	require.Equal(t, 1, countAuditEvents(t, string(business.EventUserRegistered), userID),
		"registration must be recorded by the transaction that always runs")
}

// TestSecurityMutation_MemberRemovalCascadeCommitsWithItsRecord pins the team
// cascade to the removal it belongs to. While the cascade ran in a transaction
// of its own after the removal committed, the org.member_removed record could be
// durable while the removed user still held team access through orphaned rows —
// a record describing a removal that had not fully happened.
func TestSecurityMutation_MemberRemovalCascadeCommitsWithItsRecord(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	seedOrgMember(t, orgID, owner)
	member := seedUser(t)
	seedOrgMember(t, orgID, member)

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	service := auditedService(t, emitter)

	team, err := service.CreateTeam(testCtx, owner, &gen.CreateTeamRequest{
		OrgId: orgID, Name: "Platform " + business.NewIDString()[:8],
	})
	require.NoError(t, err)
	teamID := team.GetTeam().GetId()
	require.NoError(t, service.AddTeamMember(testCtx, owner,
		&gen.AddTeamMemberRequest{TeamId: teamID, UserId: member}))
	require.Equal(t, 1, countRows(t,
		`SELECT count(*) FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, member))

	require.NoError(t, service.RemoveOrgMember(testCtx, owner,
		&gen.RemoveOrgMemberRequest{OrgId: orgID, UserId: member}))

	require.Zero(t, countRows(t,
		`SELECT count(*) FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, member))
	require.Zero(t, countRows(t,
		`SELECT count(*) FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, member),
		"team access must be gone by the time the removal is recorded")
	require.Equal(t, 1, countAuditEvents(t, string(business.EventOrgMemberRemoved), orgID))
}
