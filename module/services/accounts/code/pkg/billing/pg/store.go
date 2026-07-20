// Package pgbilling is the Postgres implementation of billing.Store.
//
// The schema it talks to:
//
//   - plans                      (stripe_price_id column added by migration 14)
//   - organizations              (stripe_customer_id column added by migration 14)
//   - subscriptions              (existing entitlements migration)
//
// The cross-tenant projection is constructed with infra's dedicated
// app_billing_worker pool. Durable Stripe receipt, claims, and lifecycle are
// owned by the generic jobs platform and never enter this store.
package pgbilling

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"accounts/pkg/billing"
	"accounts/pkg/business"
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

// UpsertSubscription projects freshly-read Stripe state monotonically. A
// transaction-scoped advisory lock serializes one organization's writes across
// replicas. StateObservedAt is the instant the provider read began; a slower,
// older read therefore cannot overwrite a later observation that committed
// first. Stripe subscription id, not "current row for org", identifies the
// history row so a late event for an old subscription cannot cancel a newer
// subscription.
func (s *Store) UpsertSubscription(ctx context.Context, u billing.SubscriptionUpsert) error {
	if u.OrgID == "" || u.StripeSubscriptionID == "" || u.Status == "" {
		return errors.New("billing: subscription projection requires org, Stripe subscription, and status")
	}
	observedAt := u.StateObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback

	// Serialize every projection for this organization without holding a
	// connection while calling Stripe (the provider read happens first).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, u.OrgID); err != nil {
		return err
	}

	var (
		existingID       string
		existingOrgID    string
		existingObserved *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id::text, org_id::text, stripe_state_observed_at
		FROM subscriptions
		WHERE stripe_subscription_id = $1
		FOR UPDATE`,
		u.StripeSubscriptionID,
	).Scan(&existingID, &existingOrgID, &existingObserved)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if u.PlanID == "" {
			return errors.New("billing: new subscription projection requires a mapped plan")
		}
		if isCurrentSubscriptionStatus(u.Status) {
			newer, err := hasNewerCurrentSubscription(ctx, tx, u.OrgID, "", observedAt)
			if err != nil {
				return err
			}
			if newer {
				return tx.Commit(ctx)
			}
			if err := cancelOtherCurrentSubscriptions(ctx, tx, u.OrgID, "", observedAt); err != nil {
				return err
			}
		}
		if err := insertSubscription(ctx, tx, u, observedAt); err != nil {
			return err
		}

	case err != nil:
		return err

	default:
		if existingOrgID != u.OrgID {
			return billing.ErrSubscriptionOwnershipMismatch
		}
		if existingObserved != nil && !existingObserved.Before(observedAt) {
			return tx.Commit(ctx)
		}
		if isCurrentSubscriptionStatus(u.Status) {
			newer, err := hasNewerCurrentSubscription(ctx, tx, u.OrgID, existingID, observedAt)
			if err != nil {
				return err
			}
			if newer {
				return tx.Commit(ctx)
			}
			if err := cancelOtherCurrentSubscriptions(ctx, tx, u.OrgID, existingID, observedAt); err != nil {
				return err
			}
		}
		if err := updateSubscription(ctx, tx, existingID, u, observedAt); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func insertSubscription(ctx context.Context, tx pgx.Tx, u billing.SubscriptionUpsert, observedAt time.Time) error {
	id := business.NewID()
	_, err := tx.Exec(ctx, `
		INSERT INTO subscriptions (
			id, org_id, plan_id, status, stripe_subscription_id,
			current_period_start, current_period_end, stripe_state_observed_at,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())`,
		id, u.OrgID, u.PlanID, u.Status, u.StripeSubscriptionID,
		nullIfZeroTime(u.CurrentPeriodStart),
		nullIfZeroTime(u.CurrentPeriodEnd),
		observedAt,
	)
	return err
}

func updateSubscription(ctx context.Context, tx pgx.Tx, id string, u billing.SubscriptionUpsert, observedAt time.Time) error {
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
				   stripe_state_observed_at = $7,
				   updated_at = NOW()
			 WHERE id = $1`,
			id, u.PlanID, u.Status,
			nullIfEmpty(u.StripeSubscriptionID),
			nullIfZeroTime(u.CurrentPeriodStart),
			nullIfZeroTime(u.CurrentPeriodEnd),
			observedAt,
		)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET status = $2,
			   stripe_subscription_id = COALESCE($3, stripe_subscription_id),
			   current_period_start = COALESCE($4, current_period_start),
			   current_period_end = COALESCE($5, current_period_end),
			   stripe_state_observed_at = $6,
			   updated_at = NOW()
		 WHERE id = $1`,
		id, u.Status,
		nullIfEmpty(u.StripeSubscriptionID),
		nullIfZeroTime(u.CurrentPeriodStart),
		nullIfZeroTime(u.CurrentPeriodEnd),
		observedAt,
	)
	return err
}

func hasNewerCurrentSubscription(
	ctx context.Context,
	tx pgx.Tx,
	orgID, exceptID string,
	observedAt time.Time,
) (bool, error) {
	var newer bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM subscriptions
			WHERE org_id = $1
			  AND ($2 = '' OR id::text <> $2)
			  AND status IN ('incomplete', 'trialing', 'active', 'past_due', 'unpaid', 'paused')
			  AND stripe_state_observed_at >= $3
		)`,
		orgID, exceptID, observedAt,
	).Scan(&newer)
	return newer, err
}

func cancelOtherCurrentSubscriptions(
	ctx context.Context,
	tx pgx.Tx,
	orgID, exceptID string,
	observedAt time.Time,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE subscriptions
		SET status = 'canceled',
			stripe_state_observed_at = $3,
			updated_at = NOW()
		WHERE org_id = $1
		  AND ($2 = '' OR id::text <> $2)
		  AND status IN ('incomplete', 'trialing', 'active', 'past_due', 'unpaid', 'paused')`,
		orgID, exceptID, observedAt,
	)
	return err
}

func isCurrentSubscriptionStatus(status string) bool {
	switch status {
	case "incomplete", "trialing", "active", "past_due", "unpaid", "paused":
		return true
	default:
		return false
	}
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

// Compile-time assertion that Store satisfies billing.Store.
var _ billing.Store = (*Store)(nil)
