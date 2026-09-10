package business_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A literal public address keeps endpoint validation off the resolver: the
// policy short-circuits DNS when the host already parses as an address.
const attributionWebhookURL = "https://93.184.216.34/hook"

type reversibleWebhookCipher struct{}

func (reversibleWebhookCipher) EncryptSecret(_ context.Context, _, plaintext string) (string, error) {
	return "envelope:" + plaintext, nil
}

func (reversibleWebhookCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return strings.TrimPrefix(envelope, "envelope:"), nil
}

func withWebhookSecurity(t *testing.T) {
	t.Helper()
	testService.SetWebhookSecurity(reversibleWebhookCipher{}, business.NewWebhookEndpointPolicy())
	t.Cleanup(func() { testService.SetWebhookSecurity(nil, nil) })
}

func mustOrgMember(t *testing.T, ctx context.Context, orgID, ownerID, email, providerID string, role gen.OrgRole) string {
	t.Helper()
	resp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: providerID, ProviderEmail: email,
		},
	})
	require.NoError(t, err)
	require.NoError(t, testService.AddOrgMember(ctx, ownerID, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: resp.User.Uuid, Role: role,
	}))
	return resp.User.Uuid
}

func webhookAuditEntries(t *testing.T, ctx context.Context, orgID string, eventType business.EventType) []business.AuditEntry {
	t.Helper()
	entries, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID: orgID, EventType: string(eventType), PageSize: 50,
	})
	require.NoError(t, err)
	return entries
}

// TestWebhookAdministrationIsAttributedToItsInitiatorInTheDatabase is the
// round trip the attribution fix exists for: two administrators of one
// organization each drive a webhook mutation, and the committed audit row names
// which of them did it — not the organization they share.
func TestWebhookAdministrationIsAttributedToItsInitiatorInTheDatabase(t *testing.T) {
	clearData(t)
	withWebhookSecurity(t)
	ctx := testCtx

	ownerResp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "owner@webhook-attribution.example.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "wh-attrib-owner",
			ProviderEmail: "owner@webhook-attribution.example.com",
		},
	})
	require.NoError(t, err)
	adminA := ownerResp.User.Uuid

	org, err := testService.CreateOrganization(ctx, adminA, &gen.CreateOrganizationRequest{
		Name: "Acme Webhook Attribution", Slug: "acme-webhook-attribution",
	})
	require.NoError(t, err)
	orgID := org.Organization.Id

	adminB := mustOrgMember(t, ctx, orgID, adminA,
		"second-admin@webhook-attribution.example.com", "wh-attrib-admin-b",
		gen.OrgRole_ORG_ROLE_ADMIN)

	actorA := business.AuditActor{ID: adminA, Type: business.ActorTypeUser}
	actorB := business.AuditActor{ID: adminB, Type: business.ActorTypeUser}

	sub, err := testService.CreateSubscription(ctx, actorA, orgID, attributionWebhookURL,
		[]string{"saas.user.registered"}, "attribution round trip")
	require.NoError(t, err)

	created := webhookAuditEntries(t, ctx, orgID, business.EventWebhookCreated)
	require.Len(t, created, 1)
	require.Equal(t, adminA, created[0].ActorID, "the creating administrator, not the organization")
	require.NotEqual(t, orgID, created[0].ActorID)
	// Version 2 is what separates a row whose actor_id is a user from a v1 row
	// whose actor_id is the organization. Without it the release boundary
	// exists only in prose, and an append-only trail cannot be re-dated.
	require.Equal(t, 2, created[0].SchemaVersion,
		"the revised actor_id contract must be legible from the row itself")
	require.Equal(t, business.ActorTypeUser, created[0].ActorType)
	require.Equal(t, orgID, created[0].OrgID)
	require.Equal(t, "webhook_subscription", created[0].Resource)
	require.Equal(t, sub.ID, created[0].ResourceID)
	require.Equal(t, business.EventWebhookCreated, created[0].EventType)

	// The second administrator rotates the secret: a different row, a different
	// actor, same tenant and resource.
	rotated, _, err := testService.RotateWebhookSecret(ctx, actorB, orgID, sub.ID, time.Hour)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(rotated, "whsec_"))

	rotations := webhookAuditEntries(t, ctx, orgID, business.EventWebhookSecretRotated)
	require.Len(t, rotations, 1)
	require.Equal(t, adminB, rotations[0].ActorID)
	require.Equal(t, 2, rotations[0].SchemaVersion)
	require.Equal(t, business.ActorTypeUser, rotations[0].ActorType)
	require.Equal(t, sub.ID, rotations[0].ResourceID)
	require.NotEqual(t, created[0].ActorID, rotations[0].ActorID,
		"two administrators of one organization must be distinguishable")

	// Replay carries the same attribution through a delivery-scoped event.
	delivery := &business.WebhookDelivery{
		ID: business.NewIDString(), SubscriptionID: sub.ID, EventID: business.NewIDString(),
		EventType: "saas.user.registered", Payload: `{"id":"evt","data":{}}`, Status: "failed",
	}
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return testStore.CreateWebhookDelivery(ctx, delivery)
	}))
	replay, err := testService.ReplayWebhookDelivery(ctx, actorB, orgID, delivery.ID)
	require.NoError(t, err)

	replays := webhookAuditEntries(t, ctx, orgID, business.EventWebhookReplayed)
	require.Len(t, replays, 1)
	require.Equal(t, adminB, replays[0].ActorID)
	require.Equal(t, "webhook_delivery", replays[0].Resource)
	require.Equal(t, replay.ID, replays[0].ResourceID)

	// Deletion closes the lifecycle, attributed to the administrator who ran it.
	require.NoError(t, testService.DeleteSubscription(ctx, actorA, orgID, sub.ID))
	deletions := webhookAuditEntries(t, ctx, orgID, business.EventWebhookDeleted)
	require.Len(t, deletions, 1)
	require.Equal(t, adminA, deletions[0].ActorID)
	require.Equal(t, sub.ID, deletions[0].ResourceID)

	// No signing material reaches the stored payloads.
	for _, entry := range [][]business.AuditEntry{created, rotations, replays, deletions} {
		encoded, err := json.Marshal(entry[0].Payload)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "whsec_")
		require.NotContains(t, string(encoded), "envelope:")
	}
}

// TestDelegatedWebhookAdministrationRecordsBothPartiesInTheDatabase — the
// subject stays the initiator and the acting service is recorded beside it, so
// the two are separable in the committed row.
func TestDelegatedWebhookAdministrationRecordsBothPartiesInTheDatabase(t *testing.T) {
	clearData(t)
	withWebhookSecurity(t)
	ctx := testCtx

	ownerResp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "delegating-owner@webhook-attribution.example.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "wh-attrib-delegating-owner",
			ProviderEmail: "delegating-owner@webhook-attribution.example.com",
		},
	})
	require.NoError(t, err)
	owner := ownerResp.User.Uuid

	org, err := testService.CreateOrganization(ctx, owner, &gen.CreateOrganizationRequest{
		Name: "Acme Delegated Webhooks", Slug: "acme-delegated-webhooks",
	})
	require.NoError(t, err)
	orgID := org.Organization.Id

	_, err = testService.CreateSubscription(ctx, business.AuditActor{
		ID: owner, Type: business.ActorTypeUser,
		DelegationChain: []string{"svc:automation-runner", "svc:gateway"},
	}, orgID, attributionWebhookURL, []string{"saas.user.registered"}, "delegated")
	require.NoError(t, err)

	created := webhookAuditEntries(t, ctx, orgID, business.EventWebhookCreated)
	require.Len(t, created, 1)
	require.Equal(t, owner, created[0].ActorID)
	require.Equal(t, business.ActorTypeUser, created[0].ActorType)
	// Read back through JSONB, so the hops arrive as []any. Every hop survives
	// the round trip: a multi-hop chain that reached the mutation must still be
	// answerable from the committed row.
	require.Equal(t, []any{"svc:automation-runner", "svc:gateway"}, created[0].Payload["delegated_by"])
	require.False(t, created[0].IsImpersonated)
}

// TestUnattributedWebhookAdministrationCommitsNothing — the audit contract is
// the mutation's precondition, so an unattributable call leaves no subscription
// and no event.
func TestUnattributedWebhookAdministrationCommitsNothing(t *testing.T) {
	clearData(t)
	withWebhookSecurity(t)
	ctx := testCtx

	ownerResp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "unattributed@webhook-attribution.example.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "wh-attrib-unattributed",
			ProviderEmail: "unattributed@webhook-attribution.example.com",
		},
	})
	require.NoError(t, err)
	org, err := testService.CreateOrganization(ctx, ownerResp.User.Uuid, &gen.CreateOrganizationRequest{
		Name: "Acme Unattributed", Slug: "acme-unattributed",
	})
	require.NoError(t, err)
	orgID := org.Organization.Id

	_, err = testService.CreateSubscription(ctx, business.AuditActor{}, orgID,
		attributionWebhookURL, []string{"saas.user.registered"}, "unattributed")
	require.Error(t, err)

	// An id the UUID column cannot hold would be stored as NULL — an
	// unattributed row wearing an actor_type — so it is refused before the
	// mutation opens its transaction.
	_, err = testService.CreateSubscription(ctx, business.AuditActor{
		ID: "module:acme", Type: business.ActorTypeUser,
	}, orgID, attributionWebhookURL, []string{"saas.user.registered"}, "unstorable actor")
	require.Error(t, err)

	subs, err := testService.ListSubscriptions(ctx, orgID)
	require.NoError(t, err)
	require.Empty(t, subs)
	require.Empty(t, webhookAuditEntries(t, ctx, orgID, business.EventWebhookCreated))
}
