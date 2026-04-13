// Package pgbilling is the Postgres implementation of billing.Store.
//
// The schema it talks to:
//
//   - plans                      (stripe_price_id column added by migration 14)
//   - organizations              (stripe_customer_id column added by migration 14)
//   - subscriptions              (existing entitlements migration)
//   - stripe_webhook_events      (idempotency table added by migration 14)
//
// This package deliberately does NOT depend on pkg/business so the
// billing package stays decoupled. It shares the same pgxpool as the
// rest of the api service via infra.PostgresStore.Pool().
package pgbilling

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"api/pkg/billing"
	"api/pkg/business"
)

// Store is the Postgres implementation of billing.Store.
type Store struct {
	pool *pgxpool.Pool
}

// New constructs a Store around an existing pgxpool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ============================================================================
// Webhook idempotency
// ============================================================================

// WebhookAlreadyProcessed returns true if we have a row for this event id.
// The caller uses this as a fast path; RecordWebhook is the authoritative
// insert with a PK conflict.
func (s *Store) WebhookAlreadyProcessed(ctx context.Context, eventID string) (bool, error) {
	var dummy string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM stripe_webhook_events WHERE id = $1`, eventID,
	).Scan(&dummy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RecordWebhook inserts a new row. A concurrent insert for the same
// event id will violate the primary key and return an error — the
// handler checks for this and returns 200 as a duplicate.
func (s *Store) RecordWebhook(ctx context.Context, eventID, eventType string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO stripe_webhook_events (id, type, received_at)
		VALUES ($1, $2, NOW())`,
		eventID, eventType,
	)
	return err
}

// MarkWebhookProcessed stamps processed_at on the event row.
func (s *Store) MarkWebhookProcessed(ctx context.Context, eventID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE stripe_webhook_events SET processed_at = NOW() WHERE id = $1`,
		eventID,
	)
	return err
}

// MarkWebhookFailed stamps the error message so ops can see what went
// wrong when reconciling.
func (s *Store) MarkWebhookFailed(ctx context.Context, eventID, msg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE stripe_webhook_events SET error = $2 WHERE id = $1`,
		eventID, truncate(msg, 1000),
	)
	return err
}

// ============================================================================
// Lookups
// ============================================================================

// PlanByStripePriceID resolves a local plan by the stripe price id
// stored on plans.stripe_price_id.
func (s *Store) PlanByStripePriceID(ctx context.Context, stripePriceID string) (*billing.PlanRef, error) {
	var p billing.PlanRef
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, name FROM plans WHERE stripe_price_id = $1`,
		stripePriceID,
	).Scan(&p.ID, &p.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, billing.ErrPlanNotFound
		}
		return nil, err
	}
	return &p, nil
}

// OrgByStripeCustomerID resolves an internal org id by the stripe
// customer id stored on organizations.stripe_customer_id.
func (s *Store) OrgByStripeCustomerID(ctx context.Context, stripeCustomerID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text FROM organizations WHERE stripe_customer_id = $1`,
		stripeCustomerID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", billing.ErrOrgNotFound
		}
		return "", err
	}
	return id, nil
}

// OwnerEmailByStripeCustomerID resolves the billing contact email for
// a Stripe customer. Joins organizations → users via owner_id to find
// the primary email. Returns "" (no error) if no email is found.
func (s *Store) OwnerEmailByStripeCustomerID(ctx context.Context, stripeCustomerID string) (string, error) {
	var email string
	err := s.pool.QueryRow(ctx, `
		SELECT u.primary_email
		FROM organizations o
		JOIN users u ON u.uuid = o.owner_id
		WHERE o.stripe_customer_id = $1`,
		stripeCustomerID,
	).Scan(&email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return email, nil
}

// ============================================================================
// Subscription upsert
// ============================================================================

// UpsertSubscription creates or updates the active subscription row
// for an org. Rules:
//
//   - If a row exists for the org with matching stripe_subscription_id,
//     update it in place.
//   - If the stripe_subscription_id differs from any existing row, the
//     old one is marked canceled and a new one is inserted. (Rare —
//     mostly happens when a customer re-subscribes after canceling.)
//   - If no row exists, a new one is inserted.
//
// Runs in a single transaction to avoid leaving the org with two
// "active" rows visible to the partial unique index
// idx_subscriptions_org_active.
func (s *Store) UpsertSubscription(ctx context.Context, u billing.SubscriptionUpsert) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback

	// Find any active/trialing/past_due row for this org.
	var (
		existingID                 string
		existingStripeSubscription *string
	)
	err = tx.QueryRow(ctx, `
		SELECT id::text, stripe_subscription_id
		FROM subscriptions
		WHERE org_id = $1 AND status IN ('active', 'trialing', 'past_due')
		LIMIT 1`,
		u.OrgID,
	).Scan(&existingID, &existingStripeSubscription)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Fresh insert.
		if err := insertSubscription(ctx, tx, u); err != nil {
			return err
		}

	case err != nil:
		return err

	default:
		// A row exists. Decide whether to update in place or rotate.
		matches := existingStripeSubscription != nil && *existingStripeSubscription == u.StripeSubscriptionID
		if matches || u.StripeSubscriptionID == "" {
			if err := updateSubscription(ctx, tx, existingID, u); err != nil {
				return err
			}
		} else {
			// Different stripe subscription id — cancel the old one and
			// insert a new one. Keeps the partial unique index happy.
			if _, err := tx.Exec(ctx, `
				UPDATE subscriptions SET status = 'canceled', updated_at = NOW() WHERE id = $1`,
				existingID,
			); err != nil {
				return err
			}
			if err := insertSubscription(ctx, tx, u); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

func insertSubscription(ctx context.Context, tx pgx.Tx, u billing.SubscriptionUpsert) error {
	id := business.NewID()
	_, err := tx.Exec(ctx, `
		INSERT INTO subscriptions (
			id, org_id, plan_id, status, stripe_subscription_id,
			current_period_start, current_period_end, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())`,
		id, u.OrgID, nullIfZeroPlan(u.PlanID), u.Status,
		nullIfEmpty(u.StripeSubscriptionID),
		nullIfZeroTime(u.CurrentPeriodStart),
		nullIfZeroTime(u.CurrentPeriodEnd),
	)
	return err
}

func updateSubscription(ctx context.Context, tx pgx.Tx, id string, u billing.SubscriptionUpsert) error {
	// Plan id is only updated if non-empty so invoice.payment_failed
	// (which doesn't carry plan info) doesn't wipe it.
	if u.PlanID != "" {
		_, err := tx.Exec(ctx, `
			UPDATE subscriptions
			   SET plan_id = $2,
				   status = $3,
				   stripe_subscription_id = COALESCE($4, stripe_subscription_id),
				   current_period_start = COALESCE($5, current_period_start),
				   current_period_end = COALESCE($6, current_period_end),
				   updated_at = NOW()
			 WHERE id = $1`,
			id, u.PlanID, u.Status,
			nullIfEmpty(u.StripeSubscriptionID),
			nullIfZeroTime(u.CurrentPeriodStart),
			nullIfZeroTime(u.CurrentPeriodEnd),
		)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET status = $2,
			   stripe_subscription_id = COALESCE($3, stripe_subscription_id),
			   current_period_start = COALESCE($4, current_period_start),
			   current_period_end = COALESCE($5, current_period_end),
			   updated_at = NOW()
		 WHERE id = $1`,
		id, u.Status,
		nullIfEmpty(u.StripeSubscriptionID),
		nullIfZeroTime(u.CurrentPeriodStart),
		nullIfZeroTime(u.CurrentPeriodEnd),
	)
	return err
}

// ============================================================================
// Small helpers
// ============================================================================

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZeroTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

func nullIfZeroPlan(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Compile-time assertion that Store satisfies billing.Store.
var _ billing.Store = (*Store)(nil)
