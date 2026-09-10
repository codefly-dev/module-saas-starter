package adapters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

const (
	webhookOrgID    = "019f6bf7-6a01-7001-8001-000000000001"
	webhookAdminAID = "019f6bf7-6a01-7001-8001-0000000000a1"
	webhookAdminBID = "019f6bf7-6a01-7001-8001-0000000000b1"
	webhookMemberID = "019f6bf7-6a01-7001-8001-0000000000c1"
	// A literal public address keeps endpoint validation off the resolver: the
	// policy short-circuits DNS when the host already parses as an address.
	webhookEndpointURL = "https://93.184.216.34/hook?token=not-a-real-credential"
)

// webhookAttributionStore is the minimum store surface the webhook
// administration handlers touch, plus the membership lookups their
// authorization runs.
type webhookAttributionStore struct {
	business.Store
	roles         map[string]gen.OrgRole
	subscriptions map[string]*business.WebhookSubscription
	deliveries    map[string]*business.WebhookDelivery
}

func (s *webhookAttributionStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *webhookAttributionStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *webhookAttributionStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *webhookAttributionStore) GetPlatformRole(context.Context, string) (string, error) {
	return "", nil
}

func (s *webhookAttributionStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	role, ok := s.roles[userID]
	if !ok || orgID != webhookOrgID {
		return nil, nil
	}
	return &gen.OrgMembership{UserId: userID, OrgId: orgID, Role: role}, nil
}

func (s *webhookAttributionStore) CreateWebhookSubscription(_ context.Context, sub *business.WebhookSubscription) error {
	stored := *sub
	s.subscriptions[sub.ID] = &stored
	return nil
}

func (s *webhookAttributionStore) GetWebhookSubscription(_ context.Context, id string) (*business.WebhookSubscription, error) {
	sub, ok := s.subscriptions[id]
	if !ok {
		return nil, nil
	}
	loaded := *sub
	return &loaded, nil
}

func (s *webhookAttributionStore) UpdateWebhookSubscription(_ context.Context, sub *business.WebhookSubscription) error {
	stored := *sub
	s.subscriptions[sub.ID] = &stored
	return nil
}

func (s *webhookAttributionStore) DeleteWebhookSubscription(_ context.Context, id string) error {
	delete(s.subscriptions, id)
	return nil
}

func (s *webhookAttributionStore) GetWebhookDelivery(_ context.Context, id string) (*business.WebhookDelivery, error) {
	delivery, ok := s.deliveries[id]
	if !ok {
		return nil, nil
	}
	loaded := *delivery
	return &loaded, nil
}

func (s *webhookAttributionStore) CreateWebhookDelivery(_ context.Context, delivery *business.WebhookDelivery) error {
	stored := *delivery
	s.deliveries[delivery.ID] = &stored
	return nil
}

func (s *webhookAttributionStore) EnqueueJob(
	context.Context, *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	return &jobsv1.EnqueueJobResponse{
		JobId:       business.NewIDString(),
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

// recordingAuditEmitter captures what the mutation would have committed.
type recordingAuditEmitter struct{ entries []business.AuditEntry }

func (e *recordingAuditEmitter) Emit(_ context.Context, entry business.AuditEntry) {
	e.entries = append(e.entries, entry)
}

func (e *recordingAuditEmitter) EmitTx(_ context.Context, entry business.AuditEntry) error {
	e.entries = append(e.entries, entry)
	return nil
}

func (e *recordingAuditEmitter) only(t *testing.T) business.AuditEntry {
	t.Helper()
	require.Len(t, e.entries, 1, "expected exactly one audit event")
	return e.entries[0]
}

type passthroughWebhookCipher struct{}

func (passthroughWebhookCipher) EncryptSecret(_ context.Context, _, plaintext string) (string, error) {
	return "envelope:" + plaintext, nil
}

func (passthroughWebhookCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return strings.TrimPrefix(envelope, "envelope:"), nil
}

func installWebhookAttributionService(t *testing.T) (*webhookConnectHandler, *webhookAttributionStore, *recordingAuditEmitter) {
	t.Helper()
	store := &webhookAttributionStore{
		roles: map[string]gen.OrgRole{
			webhookAdminAID: gen.OrgRole_ORG_ROLE_ADMIN,
			webhookAdminBID: gen.OrgRole_ORG_ROLE_OWNER,
			webhookMemberID: gen.OrgRole_ORG_ROLE_MEMBER,
		},
		subscriptions: map[string]*business.WebhookSubscription{},
		deliveries:    map[string]*business.WebhookDelivery{},
	}
	emitter := &recordingAuditEmitter{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetWebhookSecurity(passthroughWebhookCipher{}, business.NewWebhookEndpointPolicy())
	svc.SetWebhookJobProducer(store)
	svc.SetAuditEmitter(emitter)
	require.NoError(t, svc.VerifyAuditWiring())

	previous := service
	service = svc
	t.Cleanup(func() { service = previous })
	return &webhookConnectHandler{svc: svc}, store, emitter
}

// webhookCallerContext models a caller the auth interceptor has already
// verified: a session identity plus the recent step-up sensitive webhook
// operations require.
func webhookCallerContext(userID string) context.Context {
	return stampVerifiedIdentity(context.Background(), userID, webhookOrgID, auth.Assurance{
		AuthenticationMethods: []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodOTP},
		Level:                 auth.AssuranceLevelAAL2,
		AuthenticatedAt:       time.Now(),
		MFAVerifiedAt:         time.Now(),
	})
}

func seedSubscription(t *testing.T, store *webhookAttributionStore) *business.WebhookSubscription {
	t.Helper()
	sub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: webhookOrgID, URL: webhookEndpointURL,
		SecretEncrypted: "envelope:whsec_seeded", Events: []string{"saas.user.registered"}, Active: true,
	}
	store.subscriptions[sub.ID] = sub
	return sub
}

// TestWebhookMutationsRecordTheAuthenticatedCaller is the attribution
// regression: the four webhook administration mutations must name the human who
// made them, not the organization they were made in.
func TestWebhookMutationsRecordTheAuthenticatedCaller(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		handler, store, emitter := installWebhookAttributionService(t)

		_, err := handler.CreateSubscription(webhookCallerContext(webhookAdminAID),
			connect.NewRequest(&gen.CreateWebhookSubscriptionRequest{
				OrgId: webhookOrgID, Url: webhookEndpointURL, Events: []string{"saas.user.registered"},
			}))
		require.NoError(t, err)

		entry := emitter.only(t)
		require.Equal(t, webhookAdminAID, entry.ActorID)
		require.Equal(t, business.ActorTypeUser, entry.ActorType)
		require.NotEqual(t, webhookOrgID, entry.ActorID, "the tenant is not an actor")
		require.Equal(t, business.EventWebhookCreated, entry.EventType)
		require.Equal(t, webhookOrgID, entry.OrgID)
		require.Equal(t, "webhook_subscription", entry.Resource)
		require.Len(t, store.subscriptions, 1)
		for id := range store.subscriptions {
			require.Equal(t, id, entry.ResourceID)
		}
	})

	t.Run("delete", func(t *testing.T) {
		handler, store, emitter := installWebhookAttributionService(t)
		sub := seedSubscription(t, store)

		_, err := handler.DeleteSubscription(webhookCallerContext(webhookAdminBID),
			connect.NewRequest(&gen.DeleteWebhookSubscriptionRequest{Id: sub.ID}))
		require.NoError(t, err)

		entry := emitter.only(t)
		require.Equal(t, webhookAdminBID, entry.ActorID)
		require.Equal(t, business.ActorTypeUser, entry.ActorType)
		require.Equal(t, business.EventWebhookDeleted, entry.EventType)
		require.Equal(t, webhookOrgID, entry.OrgID)
		require.Equal(t, "webhook_subscription", entry.Resource)
		require.Equal(t, sub.ID, entry.ResourceID)
	})

	t.Run("replay", func(t *testing.T) {
		handler, store, emitter := installWebhookAttributionService(t)
		sub := seedSubscription(t, store)
		original := &business.WebhookDelivery{
			ID: business.NewIDString(), SubscriptionID: sub.ID, EventID: business.NewIDString(),
			EventType: "saas.user.registered", Payload: `{"id":"evt"}`, Status: "failed",
		}
		store.deliveries[original.ID] = original

		replayed, err := handler.ReplayDelivery(webhookCallerContext(webhookAdminAID),
			connect.NewRequest(&gen.ReplayWebhookDeliveryRequest{Id: original.ID}))
		require.NoError(t, err)

		entry := emitter.only(t)
		require.Equal(t, webhookAdminAID, entry.ActorID)
		require.Equal(t, business.ActorTypeUser, entry.ActorType)
		require.Equal(t, business.EventWebhookReplayed, entry.EventType)
		require.Equal(t, webhookOrgID, entry.OrgID)
		require.Equal(t, "webhook_delivery", entry.Resource)
		require.Equal(t, replayed.Msg.GetId(), entry.ResourceID)
	})

	t.Run("rotate", func(t *testing.T) {
		handler, store, emitter := installWebhookAttributionService(t)
		sub := seedSubscription(t, store)

		_, err := handler.RotateSecret(webhookCallerContext(webhookAdminBID),
			connect.NewRequest(&gen.RotateWebhookSecretRequest{Id: sub.ID}))
		require.NoError(t, err)

		entry := emitter.only(t)
		require.Equal(t, webhookAdminBID, entry.ActorID)
		require.Equal(t, business.ActorTypeUser, entry.ActorType)
		require.Equal(t, business.EventWebhookSecretRotated, entry.EventType)
		require.Equal(t, webhookOrgID, entry.OrgID)
		require.Equal(t, "webhook_subscription", entry.Resource)
		require.Equal(t, sub.ID, entry.ResourceID)
	})
}

// TestWebhookMutationsDistinguishTwoAdminsInOneOrganization is the case the
// organization-as-actor bug made unanswerable.
func TestWebhookMutationsDistinguishTwoAdminsInOneOrganization(t *testing.T) {
	handler, _, emitter := installWebhookAttributionService(t)

	for _, admin := range []string{webhookAdminAID, webhookAdminBID} {
		_, err := handler.CreateSubscription(webhookCallerContext(admin),
			connect.NewRequest(&gen.CreateWebhookSubscriptionRequest{
				OrgId: webhookOrgID, Url: webhookEndpointURL, Events: []string{"saas.user.registered"},
			}))
		require.NoError(t, err)
	}

	require.Len(t, emitter.entries, 2)
	require.Equal(t, webhookAdminAID, emitter.entries[0].ActorID)
	require.Equal(t, webhookAdminBID, emitter.entries[1].ActorID)
	require.NotEqual(t, emitter.entries[0].ResourceID, emitter.entries[1].ResourceID)
}

// TestWebhookMutationCarriesTheCredentialKind proves an API-key request is not
// filed as an interactive session, and that neither is filed as system work.
func TestWebhookMutationCarriesTheCredentialKind(t *testing.T) {
	handler, store, emitter := installWebhookAttributionService(t)
	sub := seedSubscription(t, store)

	ctx := withScopes(webhookCallerContext(webhookAdminAID), []string{"webhooks:write"})
	_, err := handler.DeleteSubscription(ctx, connect.NewRequest(&gen.DeleteWebhookSubscriptionRequest{Id: sub.ID}))
	require.NoError(t, err)

	entry := emitter.only(t)
	require.Equal(t, webhookAdminAID, entry.ActorID)
	require.Equal(t, business.ActorTypeAPIKey, entry.ActorType)
	require.NotEqual(t, business.ActorTypeSystem, entry.ActorType)
}

// TestWebhookMutationRecordsDelegationSeparatelyFromTheActor proves the RFC 8693
// `act` chain lands in its own field: the subject stays the initiator, and the
// delegate is recorded beside it rather than replacing it.
func TestWebhookMutationRecordsDelegationSeparatelyFromTheActor(t *testing.T) {
	handler, store, emitter := installWebhookAttributionService(t)
	sub := seedSubscription(t, store)

	ctx := auth.WithVerifiedActor(webhookCallerContext(webhookAdminAID), &auth.Actor{Subject: "svc:automation-runner"})
	_, err := handler.DeleteSubscription(ctx, connect.NewRequest(&gen.DeleteWebhookSubscriptionRequest{Id: sub.ID}))
	require.NoError(t, err)

	entry := emitter.only(t)
	require.Equal(t, webhookAdminAID, entry.ActorID)
	require.Equal(t, business.ActorTypeUser, entry.ActorType)
	require.Equal(t, map[string]any{"delegated_by": "svc:automation-runner"}, entry.Payload)
	require.NoError(t, business.ValidatePayload(entry.EventType, entry.Payload))
	require.False(t, entry.IsImpersonated, "a delegation chain is not an impersonation")
}

// TestDeniedWebhookMutationRecordsNoSuccessEvent — authorization runs before the
// mutation, so a denied caller leaves neither state nor a misleading event.
func TestDeniedWebhookMutationRecordsNoSuccessEvent(t *testing.T) {
	handler, store, emitter := installWebhookAttributionService(t)
	sub := seedSubscription(t, store)

	_, err := handler.DeleteSubscription(webhookCallerContext(webhookMemberID),
		connect.NewRequest(&gen.DeleteWebhookSubscriptionRequest{Id: sub.ID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Empty(t, emitter.entries)
	require.Len(t, store.subscriptions, 1)

	_, err = handler.CreateSubscription(context.Background(),
		connect.NewRequest(&gen.CreateWebhookSubscriptionRequest{
			OrgId: webhookOrgID, Url: webhookEndpointURL, Events: []string{"saas.user.registered"},
		}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Empty(t, emitter.entries)
}

// TestDeletingAnAbsentSubscriptionRecordsNothing — the operation contract is
// "not found", not a no-op success, so no event claims a deletion happened.
func TestDeletingAnAbsentSubscriptionRecordsNothing(t *testing.T) {
	handler, _, emitter := installWebhookAttributionService(t)

	_, err := handler.DeleteSubscription(webhookCallerContext(webhookAdminAID),
		connect.NewRequest(&gen.DeleteWebhookSubscriptionRequest{Id: business.NewIDString()}))
	require.Error(t, err)
	require.Empty(t, emitter.entries)
}

// TestWebhookAuditPayloadCarriesNoSecretMaterial — the events describe a
// destination and a signing key; neither the key nor the endpoint's query
// string may travel in the record.
func TestWebhookAuditPayloadCarriesNoSecretMaterial(t *testing.T) {
	handler, store, emitter := installWebhookAttributionService(t)

	created, err := handler.CreateSubscription(webhookCallerContext(webhookAdminAID),
		connect.NewRequest(&gen.CreateWebhookSubscriptionRequest{
			OrgId: webhookOrgID, Url: webhookEndpointURL, Events: []string{"saas.user.registered"},
		}))
	require.NoError(t, err)
	require.NotEmpty(t, created.Msg.GetSecret())

	_, err = handler.RotateSecret(webhookCallerContext(webhookAdminAID),
		connect.NewRequest(&gen.RotateWebhookSecretRequest{Id: created.Msg.GetId()}))
	require.NoError(t, err)

	require.Len(t, emitter.entries, 2)
	for _, entry := range emitter.entries {
		encoded, err := json.Marshal(entry.Payload)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "whsec_")
		require.NotContains(t, string(encoded), "envelope:")
		require.NotContains(t, string(encoded), "token=")
		require.NoError(t, business.ValidatePayload(entry.EventType, entry.Payload))
	}
	require.NotEmpty(t, store.subscriptions)
}

// TestWebhookMutationRefusesAnUnattributedActor — the business boundary fails
// closed rather than filing an anonymous or bogus initiator.
func TestWebhookMutationRefusesAnUnattributedActor(t *testing.T) {
	_, store, emitter := installWebhookAttributionService(t)
	sub := seedSubscription(t, store)

	for name, actor := range map[string]business.AuditActor{
		"no id":        {Type: business.ActorTypeUser},
		"unknown type": {ID: webhookAdminAID, Type: "operator"},
		"unattributed": {},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, service.DeleteSubscription(context.Background(), actor, webhookOrgID, sub.ID))
			require.Empty(t, emitter.entries)
			require.Len(t, store.subscriptions, 1)
		})
	}
}

// TestVerifiedActorReadsOnlyTrustedContext — the audit identity comes from the
// verified request context the interceptor installed, never from anything the
// caller can put in the request body.
func TestVerifiedActorReadsOnlyTrustedContext(t *testing.T) {
	session := verifiedActor(context.Background(), webhookAdminAID)
	require.Equal(t, business.AuditActor{ID: webhookAdminAID, Type: business.ActorTypeUser}, session)

	apiKey := verifiedActor(withScopes(context.Background(), []string{"webhooks:write"}), webhookAdminAID)
	require.Equal(t, business.ActorTypeAPIKey, apiKey.Type)

	delegated := verifiedActor(
		auth.WithVerifiedActor(context.Background(), &auth.Actor{
			Subject: "svc:automation-runner",
			Act:     &auth.Actor{Subject: "svc:gateway"},
		}),
		webhookAdminAID,
	)
	require.Equal(t, webhookAdminAID, delegated.ID)
	require.Equal(t, "svc:automation-runner", delegated.DelegatedBy,
		"the immediate delegate is recorded, not the outermost hop")
}
