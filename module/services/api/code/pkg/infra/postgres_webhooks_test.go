package infra_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
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
func seedOrg(t *testing.T, ownerID string) string {
	t.Helper()
	id := business.NewIDString()
	slug := fmt.Sprintf("org-%s", id)
	_, err := testPool.Exec(testCtx,
		`INSERT INTO organizations (id, name, slug, owner_id) VALUES ($1, 'Test Org', $2, $3)`,
		id, slug, ownerID)
	require.NoError(t, err)
	return id
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
	err := testStore.CreateWebhookSubscription(testCtx, sub)
	require.NoError(t, err)

	subs, err := testStore.ListWebhookSubscriptions(testCtx, orgID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, sub.ID, subs[0].ID)
	require.Equal(t, sub.URL, subs[0].URL)
	require.Equal(t, sub.Secret, subs[0].Secret)
	require.Equal(t, sub.Events, subs[0].Events)
	require.Equal(t, sub.Description, subs[0].Description)
	require.True(t, subs[0].Active)
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
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, sub))

	err := testStore.DeleteWebhookSubscription(testCtx, sub.ID)
	require.NoError(t, err)

	subs, err := testStore.ListWebhookSubscriptions(testCtx, orgID)
	require.NoError(t, err)
	require.Empty(t, subs)
}

func TestGetActiveWebhookSubscriptions(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	activeSub := &business.WebhookSubscription{
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/active",
		Secret: "sec",
		Events: []string{"user.registered"},
		Active: true,
	}
	inactiveSub := &business.WebhookSubscription{
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/inactive",
		Secret: "sec",
		Events: []string{"user.registered"},
		Active: false,
	}
	otherEventSub := &business.WebhookSubscription{
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/other",
		Secret: "sec",
		Events: []string{"org.created"},
		Active: true,
	}
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, activeSub))
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, inactiveSub))
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, otherEventSub))

	subs, err := testStore.GetActiveWebhookSubscriptions(testCtx, "user.registered")
	require.NoError(t, err)

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
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/hook-delivery",
		Secret: "sec",
		Events: []string{"user.registered"},
		Active: true,
	}
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, sub))

	delivery := &business.WebhookDelivery{
		ID:             business.NewIDString(),
		SubscriptionID: sub.ID,
		EventType:      "user.registered",
		Payload:        `{"user_id":"123"}`,
		Status:         "pending",
		AttemptCount:   0,
	}
	err := testStore.CreateWebhookDelivery(testCtx, delivery)
	require.NoError(t, err)

	deliveries, err := testStore.ListWebhookDeliveries(testCtx, sub.ID, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.Equal(t, delivery.ID, deliveries[0].ID)
	require.Equal(t, "user.registered", deliveries[0].EventType)
	// JSONB re-serializes with a space after the colon. Compare by JSON
	// semantics, not string equality, so this test isn't brittle to pg's
	// formatting.
	require.JSONEq(t, `{"user_id":"123"}`, deliveries[0].Payload)
	require.Equal(t, "pending", deliveries[0].Status)
}

func TestUpdateWebhookDelivery(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/hook-update",
		Secret: "sec",
		Events: []string{"user.registered"},
		Active: true,
	}
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, sub))

	delivery := &business.WebhookDelivery{
		ID:             business.NewIDString(),
		SubscriptionID: sub.ID,
		EventType:      "user.registered",
		Payload:        `{}`,
		Status:         "pending",
		AttemptCount:   0,
	}
	require.NoError(t, testStore.CreateWebhookDelivery(testCtx, delivery))

	now := time.Now()
	delivery.Status = "delivered"
	delivery.HTTPStatus = 200
	delivery.ResponseBody = "OK"
	delivery.AttemptCount = 1
	delivery.DeliveredAt = &now

	err := testStore.UpdateWebhookDelivery(testCtx, delivery)
	require.NoError(t, err)

	deliveries, err := testStore.ListWebhookDeliveries(testCtx, sub.ID, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.Equal(t, "delivered", deliveries[0].Status)
	require.Equal(t, 200, deliveries[0].HTTPStatus)
	require.Equal(t, "OK", deliveries[0].ResponseBody)
	require.Equal(t, 1, deliveries[0].AttemptCount)
	require.NotNil(t, deliveries[0].DeliveredAt)
}

func TestGetPendingDeliveries(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	sub := &business.WebhookSubscription{
		ID:     business.NewIDString(),
		OrgID:  orgID,
		URL:    "https://example.com/hook-pending",
		Secret: "sec",
		Events: []string{"user.registered"},
		Active: true,
	}
	require.NoError(t, testStore.CreateWebhookSubscription(testCtx, sub))

	// Create a "retrying" delivery with past next_retry_at
	pastRetry := time.Now().Add(-1 * time.Minute)
	retryDelivery := &business.WebhookDelivery{
		ID:             business.NewIDString(),
		SubscriptionID: sub.ID,
		EventType:      "user.registered",
		Payload:        `{"retry":true}`,
		Status:         "retrying",
		AttemptCount:   1,
	}
	require.NoError(t, testStore.CreateWebhookDelivery(testCtx, retryDelivery))
	retryDelivery.NextRetryAt = &pastRetry
	require.NoError(t, testStore.UpdateWebhookDelivery(testCtx, retryDelivery))

	// Create a "pending" delivery — should NOT appear (only "retrying" qualifies)
	pendingDelivery := &business.WebhookDelivery{
		ID:             business.NewIDString(),
		SubscriptionID: sub.ID,
		EventType:      "user.registered",
		Payload:        `{"pending":true}`,
		Status:         "pending",
		AttemptCount:   0,
	}
	require.NoError(t, testStore.CreateWebhookDelivery(testCtx, pendingDelivery))

	deliveries, err := testStore.GetPendingDeliveries(testCtx, 10)
	require.NoError(t, err)

	ids := make(map[string]bool)
	for _, d := range deliveries {
		ids[d.ID] = true
	}
	require.True(t, ids[retryDelivery.ID], "retrying delivery with past next_retry_at should be returned")
	require.False(t, ids[pendingDelivery.ID], "pending (not retrying) delivery should not be returned")
}
