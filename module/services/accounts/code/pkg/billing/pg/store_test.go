package pgbilling_test

import (
	"context"
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

	"accounts/internal/testdb"
	"accounts/pkg/billing"
	pgbilling "accounts/pkg/billing/pg"
	"accounts/pkg/business"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runBillingStoreTests(m))
}

func runBillingStoreTests(m *testing.M) int {
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
		return 1
	}
	defer deps.Destroy(ctx)

	if _, err := codefly.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init: %v\n", err)
		return 1
	}
	// Billing SQL tests need deterministic cross-tenant setup and teardown.
	// Resolve the test-only migration-owner capability through the Codefly SDK;
	// production code continues to use the managed read/write capability and
	// assumes app_billing_worker in infra.NewBillingWorkerPool.
	conn, err := codefly.For(ctx).Service("store").Secret("postgres", "owner-connection")
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection: %v\n", err)
		return 1
	}
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxpool: %v\n", err)
		return 1
	}
	defer pool.Close()
	testPool = pool
	releasePackageLock, err := testdb.AcquirePackageLock(ctx, pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration test lock: %v\n", err)
		return 1
	}
	defer func() {
		if err := releasePackageLock(); err != nil {
			fmt.Fprintf(os.Stderr, "release integration test lock: %v\n", err)
		}
	}()

	return m.Run()
}

// resetBilling wipes the billing-related tables between tests.
func resetBilling(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
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

func TestUpsertSubscription_OlderObservationCannotOverwriteNewerState(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)
	_, orgID := seedOrg(t, "cus_monotonic")
	proPlanID := seedPlanWithPrice(t, "pro", "price_pro")
	enterprisePlanID := seedPlanWithPrice(t, "enterprise", "price_enterprise")
	older := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID: orgID.String(), PlanID: proPlanID.String(), Status: "active",
		StripeSubscriptionID: "sub_monotonic", StateObservedAt: older,
	}))
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID: orgID.String(), PlanID: enterprisePlanID.String(), Status: "canceled",
		StripeSubscriptionID: "sub_monotonic", StateObservedAt: newer,
	}))
	// This stale response finishes last and must be ignored.
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID: orgID.String(), PlanID: proPlanID.String(), Status: "active",
		StripeSubscriptionID: "sub_monotonic", StateObservedAt: older,
	}))

	var status string
	var planID uuid.UUID
	var observedAt time.Time
	err := testPool.QueryRow(ctx, `
		SELECT status, plan_id, stripe_state_observed_at
		FROM subscriptions WHERE stripe_subscription_id = $1`, "sub_monotonic",
	).Scan(&status, &planID, &observedAt)
	require.NoError(t, err)
	require.Equal(t, "canceled", status)
	require.Equal(t, enterprisePlanID, planID)
	require.True(t, newer.Equal(observedAt))
}

func TestUpsertSubscription_LateOldSubscriptionCannotCancelNewCurrentSubscription(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)
	_, orgID := seedOrg(t, "cus_resubscribed")
	planID := seedPlanWithPrice(t, "pro", "price_pro")
	firstObserved := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	newObserved := firstObserved.Add(2 * time.Minute)

	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID: orgID.String(), PlanID: planID.String(), Status: "active",
		StripeSubscriptionID: "sub_old", StateObservedAt: firstObserved,
	}))
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID: orgID.String(), PlanID: planID.String(), Status: "active",
		StripeSubscriptionID: "sub_new", StateObservedAt: newObserved,
	}))
	// An old subscription response begun between those observations arrives
	// after the new subscription was made current.
	require.NoError(t, s.UpsertSubscription(ctx, billing.SubscriptionUpsert{
		OrgID: orgID.String(), PlanID: planID.String(), Status: "active",
		StripeSubscriptionID: "sub_old", StateObservedAt: firstObserved.Add(time.Minute),
	}))

	var activeStripeID string
	err := testPool.QueryRow(ctx, `
		SELECT stripe_subscription_id
		FROM subscriptions
		WHERE org_id = $1
		  AND status IN ('incomplete', 'trialing', 'active', 'past_due', 'unpaid', 'paused')`,
		orgID,
	).Scan(&activeStripeID)
	require.NoError(t, err)
	require.Equal(t, "sub_new", activeStripeID)
}

func TestUpsertSubscription_ConcurrentReverseCompletionConvergesToNewerObservation(t *testing.T) {
	resetBilling(t)
	ctx := context.Background()
	s := pgbilling.New(testPool)
	_, orgID := seedOrg(t, "cus_converges")
	proPlanID := seedPlanWithPrice(t, "pro", "price_pro")
	enterprisePlanID := seedPlanWithPrice(t, "enterprise", "price_enterprise")
	older := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, projection := range []billing.SubscriptionUpsert{
		{
			OrgID: orgID.String(), PlanID: proPlanID.String(), Status: "active",
			StripeSubscriptionID: "sub_concurrent", StateObservedAt: older,
		},
		{
			OrgID: orgID.String(), PlanID: enterprisePlanID.String(), Status: "canceled",
			StripeSubscriptionID: "sub_concurrent", StateObservedAt: newer,
		},
	} {
		go func() {
			<-start
			errs <- s.UpsertSubscription(ctx, projection)
		}()
	}
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	var status string
	var planID uuid.UUID
	err := testPool.QueryRow(ctx, `
		SELECT status, plan_id
		FROM subscriptions WHERE stripe_subscription_id = $1`, "sub_concurrent",
	).Scan(&status, &planID)
	require.NoError(t, err)
	require.Equal(t, "canceled", status)
	require.Equal(t, enterprisePlanID, planID)
}
