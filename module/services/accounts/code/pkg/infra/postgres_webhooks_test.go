package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"

	"accounts/pkg/billing"
	pgbilling "accounts/pkg/billing/pg"
	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	webhooksv1 "accounts/pkg/gen/saas/webhooks/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"google.golang.org/protobuf/proto"
)

type webhookProjectionCipher struct{}

type rejectingWebhookJobProducer struct{}

func (rejectingWebhookJobProducer) EnqueueJob(
	context.Context,
	*jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	return nil, errors.New("reject generated webhook job")
}

func (webhookProjectionCipher) EncryptSecret(_ context.Context, _, plaintext string) (string, error) {
	return plaintext, nil
}

func (webhookProjectionCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return envelope, nil
}

// Shared test fixtures — initialized once in TestMain.
var (
	testStore *infra.PostgresStore
	testPool  *pgxpool.Pool
	testCtx   context.Context
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	wool.SetGlobalLogLevel(wool.DEBUG)

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("test-infra"),
		sdk.WithTimeout(90*time.Second),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies: %v\n", err)
		os.Exit(1)
	}

	if _, err := codefly.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init: %v\n", err)
		os.Exit(1)
	}

	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewPostgresStore: %v\n", err)
		os.Exit(1)
	}

	testStore = store
	testPool = store.Pool()
	testCtx = ctx

	code := m.Run()
	store.Close()
	deps.Destroy(ctx)
	os.Exit(code)
}

func TestBillingWorkerPoolUsesLeastPrivilegeBypassRole(t *testing.T) {
	pool, err := infra.NewBillingWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var currentRole string
	var bypassRLS bool
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT current_user, rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user`).Scan(&currentRole, &bypassRLS))
	require.Equal(t, "app_billing_worker", currentRole)
	require.True(t, bypassRLS)

	var planCount int
	require.NoError(t, pool.QueryRow(testCtx, `SELECT COUNT(*) FROM plans`).Scan(&planCount))
	require.Greater(t, planCount, 0)

	var forbiddenCount int
	err = pool.QueryRow(testCtx, `SELECT COUNT(*) FROM api_keys`).Scan(&forbiddenCount)
	require.Error(t, err, "billing worker must not inherit unrelated tenant-table access")
	err = pool.QueryRow(testCtx, `SELECT COUNT(*) FROM job_messages`).Scan(&forbiddenCount)
	require.Error(t, err, "billing projection worker must not inherit generic job authority")
}

func TestBillingWorkerPoolReconcilesAcrossTenantRLS(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	customerID := "cus_worker_" + business.NewIDString()
	priceID := "price_worker_" + business.NewIDString()
	subscriptionID := "sub_worker_" + business.NewIDString()

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.SetOrgStripeCustomerID(ctx, orgID, customerID)
	}))
	var planID string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx, `
			UPDATE plans SET stripe_price_id = $1
			WHERE name = 'pro'
			RETURNING id::text`, priceID).Scan(&planID)
	}))

	pool, err := infra.NewBillingWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	workerStore := pgbilling.New(pool)

	resolvedOrg, err := workerStore.OrgByStripeCustomerID(testCtx, customerID)
	require.NoError(t, err)
	require.Equal(t, orgID, resolvedOrg)
	plan, err := workerStore.PlanByStripePriceID(testCtx, priceID)
	require.NoError(t, err)
	require.Equal(t, planID, plan.ID)

	observedAt := time.Now().UTC()
	require.NoError(t, workerStore.UpsertSubscription(testCtx, billing.SubscriptionUpsert{
		OrgID: orgID, PlanID: planID, Status: "active",
		StripeSubscriptionID: subscriptionID,
		StateObservedAt:      observedAt,
	}))

	var storedOrgID, storedPlanID string
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT org_id::text, plan_id::text
		FROM subscriptions
		WHERE stripe_subscription_id = $1`, subscriptionID).Scan(&storedOrgID, &storedPlanID))
	require.Equal(t, orgID, storedOrgID)
	require.Equal(t, planID, storedPlanID)
}

// seedUser inserts a minimal user and returns its UUID.
func seedUser(t *testing.T) string {
	t.Helper()
	id := business.NewIDString()
	email := fmt.Sprintf("user-%s@test.local", id)
	// users is RLS-protected (WITH CHECK: self or System); a fixture bootstrap
	// has no caller identity yet, so seed under the audited System bypass.
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx,
			`INSERT INTO users (uuid, primary_email, status) VALUES ($1, $2, 'active')`, id, email)
		return err
	}))
	return id
}

// seedOrg inserts a minimal organization and returns its ID.
// organizations is RLS-protected (Phase 2F); seed under WithControlPlane.
// We grab the tx from ctx instead of using testPool — testPool would
// check out a fresh app_tenant connection that doesn't see the
// app_control_plane role installed by WithControlPlane.
func seedOrg(t *testing.T, ownerID string) string {
	t.Helper()
	id := business.NewIDString()
	slug := fmt.Sprintf("org-%s", id)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, err := tx.Exec(ctx,
			`INSERT INTO organizations (id, name, slug, owner_id) VALUES ($1, 'Test Org', $2, $3)`,
			id, slug, ownerID)
		return err
	}))
	return id
}

// seedOrgMember adds a membership row via the domain method as a tenant-scoped
// identity — organization_members is RLS-gated (org_id = app.current_org_id), so
// the identity's OrgID satisfies the WITH CHECK. No raw SQL, no bypass.
func seedOrgMember(t *testing.T, orgID, userID string) {
	t.Helper()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, userID, "member"))
}

// ============================================================================
// Webhook Subscription tests
// ============================================================================

func TestCreateAndListWebhookSubscriptions(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID:              business.NewIDString(),
		OrgID:           orgID,
		URL:             "https://example.com/hook",
		SecretEncrypted: "encrypted:s3cret",
		Events:          []string{"user.registered", "org.created"},
		Description:     "Test hook",
		Active:          true,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateWebhookSubscription(ctx, sub); err != nil {
			return err
		}
		subs, err := testStore.ListWebhookSubscriptions(ctx, orgID)
		require.NoError(t, err)
		require.Len(t, subs, 1)
		require.Equal(t, sub.ID, subs[0].ID)
		require.Equal(t, sub.URL, subs[0].URL)
		require.Equal(t, sub.SecretEncrypted, subs[0].SecretEncrypted)
		require.Equal(t, sub.Events, subs[0].Events)
		require.Equal(t, sub.Description, subs[0].Description)
		require.True(t, subs[0].Active)
		return nil
	}))
}

func TestDeleteWebhookSubscription(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID:              business.NewIDString(),
		OrgID:           orgID,
		URL:             "https://example.com/hook-del",
		SecretEncrypted: "encrypted:sec",
		Events:          []string{"user.deleted"},
		Active:          true,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, sub))
		require.NoError(t, testStore.DeleteWebhookSubscription(ctx, sub.ID))
		subs, err := testStore.ListWebhookSubscriptions(ctx, orgID)
		require.NoError(t, err)
		require.Empty(t, subs)
		return nil
	}))
}

func TestGetActiveWebhookSubscriptions(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	activeSub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/active", SecretEncrypted: "encrypted:sec",
		Events: []string{"user.registered"}, Active: true,
	}
	inactiveSub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/inactive", SecretEncrypted: "encrypted:sec",
		Events: []string{"user.registered"}, Active: false,
	}
	otherEventSub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/other", SecretEncrypted: "encrypted:sec",
		Events: []string{"org.created"}, Active: true,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, activeSub))
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, inactiveSub))
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, otherEventSub))
		return nil
	}))

	// Use the control-plane scope here to exercise event/active filtering across
	// all fixtures. Production audit fan-out calls the same query inside one
	// organization transaction and RLS restricts it to that organization.
	var subs []*business.WebhookSubscription
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		s, err := testStore.GetActiveWebhookSubscriptions(ctx, "user.registered")
		subs = s
		return err
	}))

	ids := make(map[string]bool)
	for _, s := range subs {
		ids[s.ID] = true
	}
	require.True(t, ids[activeSub.ID], "active sub with matching event should be returned")
	require.False(t, ids[inactiveSub.ID], "inactive sub should not be returned")
	require.False(t, ids[otherEventSub.ID], "sub with different event should not be returned")
}

func TestCreateAndListWebhookDeliveries(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/hook-delivery", SecretEncrypted: "encrypted:sec",
		Events: []string{"user.registered"}, Active: true,
	}
	delivery := &business.WebhookDelivery{
		ID: business.NewIDString(), SubscriptionID: sub.ID,
		EventType: "user.registered", Payload: `{"user_id":"123"}`,
		Status: "pending", AttemptCount: 0,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, sub))
		require.NoError(t, testStore.CreateWebhookDelivery(ctx, delivery))
		deliveries, err := testStore.ListWebhookDeliveries(ctx, sub.ID, 10)
		require.NoError(t, err)
		require.Len(t, deliveries, 1)
		require.Equal(t, delivery.ID, deliveries[0].ID)
		require.Equal(t, "user.registered", deliveries[0].EventType)
		require.JSONEq(t, `{"user_id":"123"}`, deliveries[0].Payload)
		require.Equal(t, "pending", deliveries[0].Status)
		return nil
	}))
}

func TestDurableAuditEmitterCreatesWebhookOutboxAtomically(t *testing.T) {
	var receivedBody []byte
	var receivedDeliveryID, receivedEventID string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedDeliveryID = r.Header.Get("X-Webhook-Delivery-ID")
		receivedEventID = r.Header.Get("X-Webhook-Event-ID")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(endpoint.Close)

	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	eventID := business.NewIDString()
	eventType := "test.webhook.outbox." + eventID
	sub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: endpoint.URL, SecretEncrypted: "test-signing-secret",
		Events: []string{eventType}, Active: true,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateWebhookSubscription(ctx, sub)
	}))

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	emitter.Emit(testCtx, business.AuditEntry{
		ID: eventID, ActorID: userID, ActorType: "user",
		Action: eventType, Resource: "user", ResourceID: userID, OrgID: orgID,
		Metadata:  map[string]string{"source": "integration-test"},
		CreatedAt: time.Date(2026, 7, 13, 12, 0, 0, 123, time.UTC),
	})

	var deliveries []*business.WebhookDelivery
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		deliveries, err = testStore.ListWebhookDeliveries(ctx, sub.ID, 10)
		return err
	}))
	require.Len(t, deliveries, 1)
	require.Equal(t, eventID, deliveries[0].EventID)
	require.Equal(t, eventID, deliveries[0].OutboxEventID)
	require.Equal(t, "pending", deliveries[0].Status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(deliveries[0].Payload), &payload))
	require.Equal(t, eventID, payload["id"])
	require.Equal(t, eventType, payload["event_type"])
	require.Equal(t, deliveries[0].ID, payload["delivery_id"])

	jobPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(jobPool.Close)
	projectionPool, err := infra.NewWebhookProjectionPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(projectionPool.Close)
	projection := infra.NewPostgresWebhookProjection(projectionPool)
	handler, err := business.NewOutboundWebhookJobHandler(
		projection,
		business.NewWebhookSenderWithClient(webhookProjectionCipher{}, endpoint.Client()),
	)
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: infra.NewPostgresJobStore(jobPool), Queue: business.OutboundWebhookQueue,
		Handler: handler, WorkerID: "outbound-webhook-integration", BatchSize: 100,
	})
	require.NoError(t, err)
	processed, _ := worker.RunOnce(testCtx)
	require.Positive(t, processed)
	require.Equal(t, deliveries[0].Payload, string(receivedBody))
	require.Equal(t, deliveries[0].ID, receivedDeliveryID)
	require.Equal(t, eventID, receivedEventID)

	var jobState string
	var jobAttempts int
	require.NoError(t, jobPool.QueryRow(testCtx, `
		SELECT state, attempt_count
		FROM job_messages
		WHERE direction = 'outbox' AND queue = $1 AND idempotency_key = $2`,
		business.OutboundWebhookQueue, deliveries[0].ID,
	).Scan(&jobState, &jobAttempts))
	require.Equal(t, "succeeded", jobState)
	require.Equal(t, 1, jobAttempts)

	var updated *business.WebhookDelivery
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		updated, err = testStore.GetWebhookDelivery(ctx, deliveries[0].ID)
		return err
	}))
	require.Equal(t, "delivered", updated.Status)
	require.Equal(t, http.StatusNoContent, updated.HTTPStatus)
	require.Equal(t, 1, updated.AttemptCount)
	require.NotNil(t, updated.DeliveredAt)

	var currentRole string
	require.NoError(t, projectionPool.QueryRow(testCtx, `SELECT current_user`).Scan(&currentRole))
	require.Equal(t, "app_webhook_worker", currentRole)
	var forbidden int
	require.Error(t, projectionPool.QueryRow(testCtx, `SELECT COUNT(*) FROM job_messages`).Scan(&forbidden))

	var specializedColumnCount int
	require.NoError(t, testPool.QueryRow(testCtx, `
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'webhook_deliveries'
		  AND column_name IN (
		      'max_attempts', 'next_retry_at', 'lease_owner', 'lease_expires_at',
		      'last_error', 'dead_lettered_at'
		  )`,
	).Scan(&specializedColumnCount))
	require.Zero(t, specializedColumnCount)

	// A duplicate event insert rolls the entire transaction back; the partial
	// unique outbox key also prevents a second endpoint row for the same event.
	emitter.Emit(testCtx, business.AuditEntry{
		ID: eventID, ActorType: "system", Action: eventType, OrgID: orgID,
	})
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		again, err := testStore.ListWebhookDeliveries(ctx, sub.ID, 10)
		require.Len(t, again, 1)
		return err
	}))
}

func TestManualWebhookCommandsUseTransactionalGenericOutbox(t *testing.T) {
	jobPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(jobPool.Close)

	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	subscription := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID, URL: "https://hooks.example.com/events",
		SecretEncrypted: "test-signing-secret", Events: []string{"webhook.test"}, Active: true,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateWebhookSubscription(ctx, subscription)
	}))

	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetWebhookJobProducer(rejectingWebhookJobProducer{})
	_, err = service.TestWebhook(testCtx, orgID, subscription.ID, "")
	require.ErrorContains(t, err, "reject generated webhook job")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		deliveries, err := testStore.ListWebhookDeliveries(ctx, subscription.ID, 10)
		require.Empty(t, deliveries, "job failure must roll back delivery history")
		return err
	}))

	service.SetWebhookJobProducer(testStore)
	testDelivery, err := service.TestWebhook(testCtx, orgID, subscription.ID, "")
	require.NoError(t, err)
	require.Equal(t, "pending", testDelivery.Status)
	require.Empty(t, testDelivery.OutboxEventID)

	var encoded []byte
	require.NoError(t, jobPool.QueryRow(testCtx, `
		SELECT payload
		FROM job_messages
		WHERE direction = 'outbox' AND queue = $1 AND idempotency_key = $2`,
		business.OutboundWebhookQueue, testDelivery.ID,
	).Scan(&encoded))
	workload := &webhooksv1.OutboundWebhookJob{}
	require.NoError(t, proto.Unmarshal(encoded, workload))
	require.Equal(t, testDelivery.ID, workload.GetDeliveryId())
	require.Equal(t, testDelivery.EventID, workload.GetEventId())
	require.Equal(t, []byte(testDelivery.Payload), workload.GetRawBody())

	replay, err := service.ReplayWebhookDelivery(testCtx, orgID, testDelivery.ID)
	require.NoError(t, err)
	require.NotEqual(t, testDelivery.ID, replay.ID)
	require.Equal(t, testDelivery.EventID, replay.EventID)
	require.Equal(t, testDelivery.Payload, replay.Payload)
	require.Empty(t, replay.OutboxEventID)
	require.NoError(t, jobPool.QueryRow(testCtx, `
		SELECT payload
		FROM job_messages
		WHERE direction = 'outbox' AND queue = $1 AND idempotency_key = $2`,
		business.OutboundWebhookQueue, replay.ID,
	).Scan(&encoded))
	require.NoError(t, proto.Unmarshal(encoded, workload))
	require.Equal(t, replay.ID, workload.GetDeliveryId())
	require.Equal(t, testDelivery.EventID, workload.GetEventId())
	require.Equal(t, []byte(testDelivery.Payload), workload.GetRawBody())
}
