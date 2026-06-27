package pgbilling_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"

	"accounts/pkg/billing"
	pgbilling "accounts/pkg/billing/pg"
	"accounts/pkg/business"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx := context.Background()
	wool.SetGlobalLogLevel(wool.DEBUG)

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("pgbilling-test"),
		sdk.WithTimeout(120*time.Second),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies: %v\n", err)
		os.Exit(1)
	}
	defer deps.Destroy(ctx)

	if _, err := codefly.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init: %v\n", err)
		os.Exit(1)
	}
	conn, err := codefly.For(ctx).Service("store").Secret("postgres", "connection")
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection: %v\n", err)
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxpool: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// resetBilling wipes the billing-related tables between tests.
func resetBilling(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE TABLE stripe_webhook_events RESTART IDENTITY`,
		`TRUNCATE TABLE subscriptions RESTART IDENTITY CASCADE`,
		`TRUNCATE TABLE organization_members RESTART IDENTITY CASCADE`,
		`TRUNCATE TABLE organizations RESTART IDENTITY CASCADE`,
		`TRUNCATE TABLE users RESTART IDENTITY CASCADE`,
		// seed-restore plans — the initial migration populated three rows
		// and our tests re-seed with deterministic Stripe price ids.
		`UPDATE plans SET stripe_price_id = NULL, stripe_product_id = NULL`,
	} {
		_, err := testPool.Exec(ctx, q)
		require.NoError(t, err, q)
	}
}

// seedOrg creates a user + org returning their ids. Stripe customer
// id is populated so billing lookups resolve.
func seedOrg(t *testing.T, stripeCustomerID string) (userID, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	userID = business.NewID()
	orgID = business.NewID()
	email := fmt.Sprintf("user-%s@test.local", userID.String())
	_, err := testPool.Exec(ctx, `
		INSERT INTO users (uuid, primary_email, status)
		VALUES ($1, $2, 'active')`, userID, email)
	require.NoError(t, err)

	slug := fmt.Sprintf("org-%s", orgID.String())
	_, err = testPool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, owner_id, stripe_customer_id)
		VALUES ($1, 'Test Org', $2, $3, $4)`,
		orgID, slug, userID, stripeCustomerID)
	require.NoError(t, err)
	return
}

// seedPlanWithPrice updates an existing plan row with a stripe price id.
func seedPlanWithPrice(t *testing.T, planName, stripePriceID string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	err := testPool.QueryRow(ctx, `
		UPDATE plans SET stripe_price_id = $2 WHERE name = $1 RETURNING id`,
		planName, stripePriceID).Scan(&id)
	require.NoError(t, err)
	return id
}

// ============================================================================
// Webhook idempotency
// ============================================================================

func TestWebhook_RecordThenAlreadyProcessed(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	seen, err := s.WebhookAlreadyProcessed(ctx, "evt_01")
	require.NoError(t, err)
	require.False(t, seen)

	require.NoError(t, s.RecordWebhook(ctx, "evt_01", "customer.subscription.created"))

	seen, err = s.WebhookAlreadyProcessed(ctx, "evt_01")
	require.NoError(t, err)
	require.True(t, seen)
}

func TestWebhook_DuplicateRecordFails(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	require.NoError(t, s.RecordWebhook(ctx, "evt_dup", "x"))
	err := s.RecordWebhook(ctx, "evt_dup", "x")
	require.Error(t, err, "second insert with same id must fail (PK violation)")
}

func TestWebhook_MarkProcessedAndFailed(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	require.NoError(t, s.RecordWebhook(ctx, "evt_mark", "x"))
	require.NoError(t, s.MarkWebhookProcessed(ctx, "evt_mark"))

	// Verify processed_at is set.
	var processed *time.Time
	err := testPool.QueryRow(ctx,
		`SELECT processed_at FROM stripe_webhook_events WHERE id = $1`, "evt_mark").
		Scan(&processed)
	require.NoError(t, err)
	require.NotNil(t, processed)

	// Now mark failed.
	require.NoError(t, s.MarkWebhookFailed(ctx, "evt_mark", "plan not found"))
	var errMsg *string
	err = testPool.QueryRow(ctx,
		`SELECT error FROM stripe_webhook_events WHERE id = $1`, "evt_mark").Scan(&errMsg)
	require.NoError(t, err)
	require.NotNil(t, errMsg)
	require.Contains(t, *errMsg, "plan not found")
}

// ============================================================================
// Lookups
// ============================================================================

func TestPlanByStripePriceID_Happy(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	planID := seedPlanWithPrice(t, "pro", "price_test_pro")
	plan, err := s.PlanByStripePriceID(ctx, "price_test_pro")
	require.NoError(t, err)
	require.Equal(t, planID.String(), plan.ID)
	require.Equal(t, "pro", plan.Name)
}

func TestPlanByStripePriceID_NotFound(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, err := s.PlanByStripePriceID(ctx, "price_does_not_exist")
	require.Error(t, err)
	require.True(t, errors.Is(err, billing.ErrPlanNotFound))
}

func TestOrgByStripeCustomerID_Happy(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, orgID := seedOrg(t, "cus_test_01")
	resolved, err := s.OrgByStripeCustomerID(ctx, "cus_test_01")
	require.NoError(t, err)
	require.Equal(t, orgID.String(), resolved)
}

func TestOrgByStripeCustomerID_NotFound(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, err := s.OrgByStripeCustomerID(ctx, "cus_nope")
	require.Error(t, err)
	require.True(t, errors.Is(err, billing.ErrOrgNotFound))
}

// ============================================================================
// UpsertSubscription — the three cases
// ============================================================================

func TestUpsertSubscription_Insert_New(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, orgID := seedOrg(t, "cus_01")
	planID := seedPlanWithPrice(t, "pro", "price_pro")

	periodStart := time.Now()
	periodEnd := periodStart.Add(30 * 24 * time.Hour)

	err := s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		PlanID:               planID.String(),
		Status:               "active",
		StripeSubscriptionID: "sub_01",
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	require.NoError(t, err)

	var status, stripeID string
	err = testPool.QueryRow(ctx, `
		SELECT status, stripe_subscription_id FROM subscriptions WHERE org_id = $1`,
		orgID).Scan(&status, &stripeID)
	require.NoError(t, err)
	require.Equal(t, "active", status)
	require.Equal(t, "sub_01", stripeID)
}

func TestUpsertSubscription_Update_SameStripeID(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, orgID := seedOrg(t, "cus_01")
	planID := seedPlanWithPrice(t, "pro", "price_pro")
	enterprisePlanID := seedPlanWithPrice(t, "enterprise", "price_enterprise")

	// Initial create
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		PlanID:               planID.String(),
		Status:               "active",
		StripeSubscriptionID: "sub_01",
	}))

	// Plan change within the same Stripe subscription (upgrade).
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		PlanID:               enterprisePlanID.String(),
		Status:               "active",
		StripeSubscriptionID: "sub_01",
	}))

	// Should still be exactly one active row.
	var count int
	var planIDOut uuid.UUID
	err := testPool.QueryRow(ctx, `
		SELECT COUNT(*), plan_id FROM subscriptions
		WHERE org_id = $1 AND status IN ('active','trialing','past_due')
		GROUP BY plan_id`,
		orgID).Scan(&count, &planIDOut)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, enterprisePlanID, planIDOut)
}

func TestUpsertSubscription_Rotate_DifferentStripeID(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, orgID := seedOrg(t, "cus_01")
	planID := seedPlanWithPrice(t, "pro", "price_pro")

	// First subscription
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		PlanID:               planID.String(),
		Status:               "active",
		StripeSubscriptionID: "sub_first",
	}))

	// User cancels, re-subscribes → different Stripe subscription id.
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		PlanID:               planID.String(),
		Status:               "active",
		StripeSubscriptionID: "sub_second",
	}))

	// The old row should be canceled, the new row active.
	var activeCount, canceledCount int
	err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM subscriptions WHERE org_id = $1 AND status = 'active'`,
		orgID).Scan(&activeCount)
	require.NoError(t, err)
	require.Equal(t, 1, activeCount)

	err = testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM subscriptions WHERE org_id = $1 AND status = 'canceled'`,
		orgID).Scan(&canceledCount)
	require.NoError(t, err)
	require.Equal(t, 1, canceledCount)
}

func TestUpsertSubscription_MarkPastDue_NoPlanID(t *testing.T) {
	// invoice.payment_failed carries no plan info. UpsertSubscription
	// with an empty PlanID must NOT wipe the existing plan_id —
	// it should only update status.
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)

	_, orgID := seedOrg(t, "cus_01")
	planID := seedPlanWithPrice(t, "pro", "price_pro")

	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		PlanID:               planID.String(),
		Status:               "active",
		StripeSubscriptionID: "sub_01",
	}))

	// Now flip to past_due without plan info.
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID:                orgID.String(),
		Status:               "past_due",
		StripeSubscriptionID: "sub_01",
	}))

	var status string
	var planOut uuid.UUID
	err := testPool.QueryRow(ctx,
		`SELECT status, plan_id FROM subscriptions WHERE org_id = $1 AND status = 'past_due'`,
		orgID).Scan(&status, &planOut)
	require.NoError(t, err)
	require.Equal(t, "past_due", status)
	require.Equal(t, planID, planOut, "past_due must preserve existing plan_id")
}

// Prevent an unused-import warning on sha256 if the file ever adds
// more tests that need hash helpers.
var _ = sha256.Sum256
