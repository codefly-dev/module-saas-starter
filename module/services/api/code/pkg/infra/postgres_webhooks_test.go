package infra_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"

	"api/pkg/business"
	"api/pkg/infra"
)

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

// seedUser inserts a minimal user and returns its UUID.
func seedUser(t *testing.T) string {
	t.Helper()
	id := business.NewIDString()
	email := fmt.Sprintf("user-%s@test.local", id)
	_, err := testPool.Exec(testCtx,
		`INSERT INTO users (uuid, primary_email, status) VALUES ($1, $2, 'active')`, id, email)
	require.NoError(t, err)
	return id
}

// seedOrg inserts a minimal organization and returns its ID.
// organizations is RLS-protected (Phase 2F); seed under WithBypass.
// We grab the tx from ctx instead of using testPool — testPool would
// check out a fresh app_tenant connection that doesn't see the
// SET LOCAL ROLE NONE installed by WithBypass.
func seedOrg(t *testing.T, ownerID string) string {
	t.Helper()
	id := business.NewIDString()
	slug := fmt.Sprintf("org-%s", id)
	require.NoError(t, testStore.WithBypass(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithBypass
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
		ID:          business.NewIDString(),
		OrgID:       orgID,
		URL:         "https://example.com/hook",
		Secret:      "s3cret",
		Events:      []string{"user.registered", "org.created"},
		Description: "Test hook",
		Active:      true,
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
		require.Equal(t, sub.Secret, subs[0].Secret)
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
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/hook-del",
		Secret: "sec",
		Events: []string{"user.deleted"},
		Active: true,
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
		URL: "https://example.com/active", Secret: "sec",
		Events: []string{"user.registered"}, Active: true,
	}
	inactiveSub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/inactive", Secret: "sec",
		Events: []string{"user.registered"}, Active: false,
	}
	otherEventSub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/other", Secret: "sec",
		Events: []string{"org.created"}, Active: true,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, activeSub))
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, inactiveSub))
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, otherEventSub))
		return nil
	}))

	// GetActiveWebhookSubscriptions is a cross-tenant scan (the
	// dispatcher needs to see every org's active subs). Run under
	// WithBypass — same shape as webhook_dispatcher.go.
	var subs []*business.WebhookSubscription
	require.NoError(t, testStore.WithBypass(testCtx, func(ctx context.Context) error {
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
		URL: "https://example.com/hook-delivery", Secret: "sec",
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

func TestUpdateWebhookDelivery(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/hook-update", Secret: "sec",
		Events: []string{"user.registered"}, Active: true,
	}
	delivery := &business.WebhookDelivery{
		ID: business.NewIDString(), SubscriptionID: sub.ID,
		EventType: "user.registered", Payload: `{}`,
		Status: "pending", AttemptCount: 0,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, sub))
		require.NoError(t, testStore.CreateWebhookDelivery(ctx, delivery))

		now := time.Now()
		delivery.Status = "delivered"
		delivery.HTTPStatus = 200
		delivery.ResponseBody = "OK"
		delivery.AttemptCount = 1
		delivery.DeliveredAt = &now
		require.NoError(t, testStore.UpdateWebhookDelivery(ctx, delivery))

		deliveries, err := testStore.ListWebhookDeliveries(ctx, sub.ID, 10)
		require.NoError(t, err)
		require.Len(t, deliveries, 1)
		require.Equal(t, "delivered", deliveries[0].Status)
		require.Equal(t, 200, deliveries[0].HTTPStatus)
		require.Equal(t, "OK", deliveries[0].ResponseBody)
		require.Equal(t, 1, deliveries[0].AttemptCount)
		require.NotNil(t, deliveries[0].DeliveredAt)
		return nil
	}))
}

func TestGetPendingDeliveries(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID: business.NewIDString(), OrgID: orgID,
		URL: "https://example.com/hook-pending", Secret: "sec",
		Events: []string{"user.registered"}, Active: true,
	}
	pastRetry := time.Now().Add(-1 * time.Minute)
	retryDelivery := &business.WebhookDelivery{
		ID: business.NewIDString(), SubscriptionID: sub.ID,
		EventType: "user.registered", Payload: `{"retry":true}`,
		Status: "retrying", AttemptCount: 1,
	}
	pendingDelivery := &business.WebhookDelivery{
		ID: business.NewIDString(), SubscriptionID: sub.ID,
		EventType: "user.registered", Payload: `{"pending":true}`,
		Status: "pending", AttemptCount: 0,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateWebhookSubscription(ctx, sub))
		require.NoError(t, testStore.CreateWebhookDelivery(ctx, retryDelivery))
		retryDelivery.NextRetryAt = &pastRetry
		require.NoError(t, testStore.UpdateWebhookDelivery(ctx, retryDelivery))
		require.NoError(t, testStore.CreateWebhookDelivery(ctx, pendingDelivery))
		return nil
	}))

	// GetPendingDeliveries is a cross-tenant scan (the dispatcher
	// polls every org's queue). Same WithBypass shape as production.
	var deliveries []*business.WebhookDelivery
	require.NoError(t, testStore.WithBypass(testCtx, func(ctx context.Context) error {
		d, err := testStore.GetPendingDeliveries(ctx, 10)
		deliveries = d
		return err
	}))

	ids := make(map[string]bool)
	for _, d := range deliveries {
		ids[d.ID] = true
	}
	require.True(t, ids[retryDelivery.ID], "retrying delivery with past next_retry_at should be returned")
	require.False(t, ids[pendingDelivery.ID], "pending (not retrying) delivery should not be returned")
}
