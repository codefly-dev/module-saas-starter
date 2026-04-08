package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"backend/pkg/business"
)

// GetOrgPlanID returns the plan ID for an org's active subscription.
// Falls back to the default (free) plan if no subscription exists.
func (s *PostgresStore) GetOrgPlanID(ctx context.Context, orgID string) (string, error) {
	q := s.getQueryExecutor(ctx)

	var planID string
	err := q.QueryRow(ctx, `
		SELECT plan_id FROM subscriptions
		WHERE org_id = $1 AND status IN ('active', 'trialing')
		LIMIT 1`, orgID).Scan(&planID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Fall back to default plan
			err = q.QueryRow(ctx, `SELECT id FROM plans WHERE is_default = true LIMIT 1`).Scan(&planID)
			if err != nil {
				return "", err
			}
			return planID, nil
		}
		return "", err
	}
	return planID, nil
}

// GetPlanEntitlement returns the limit for a feature in a plan.
// Returns -1 for unlimited (NULL in DB), 0 if feature not in plan.
func (s *PostgresStore) GetPlanEntitlement(ctx context.Context, planID string, feature string) (int64, error) {
	q := s.getQueryExecutor(ctx)

	var limit *int64
	err := q.QueryRow(ctx, `
		SELECT limit_value FROM plan_entitlements
		WHERE plan_id = $1 AND feature = $2`, planID, feature).Scan(&limit)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil // feature not in plan
		}
		return 0, err
	}
	if limit == nil {
		return -1, nil // unlimited
	}
	return *limit, nil
}

// GetEntitlementOverride returns an override for an org+feature, or nil if none exists.
func (s *PostgresStore) GetEntitlementOverride(ctx context.Context, orgID string, feature string) (*business.EntitlementOverride, error) {
	q := s.getQueryExecutor(ctx)

	var o business.EntitlementOverride
	var limitValue *int64
	var expiresAt *time.Time
	var createdBy *string

	err := q.QueryRow(ctx, `
		SELECT id, org_id, feature, limit_value, reason, created_by, expires_at
		FROM entitlement_overrides WHERE org_id = $1 AND feature = $2`,
		orgID, feature).Scan(&o.ID, &o.OrgID, &o.Feature, &limitValue, &o.Reason, &createdBy, &expiresAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	o.LimitValue = limitValue
	o.ExpiresAt = expiresAt
	if createdBy != nil {
		o.CreatedBy = *createdBy
	}
	return &o, nil
}

func (s *PostgresStore) CreateEntitlementOverride(ctx context.Context, override *business.EntitlementOverride) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO entitlement_overrides (id, org_id, feature, limit_value, reason, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (org_id, feature) DO UPDATE SET
			limit_value = EXCLUDED.limit_value,
			reason = EXCLUDED.reason,
			created_by = EXCLUDED.created_by,
			expires_at = EXCLUDED.expires_at`,
		override.ID, override.OrgID, override.Feature, override.LimitValue,
		override.Reason, nilIfEmpty(override.CreatedBy), override.ExpiresAt)
	return err
}

func (s *PostgresStore) GetUsageForPeriod(ctx context.Context, orgID string, feature string, period string) (int64, error) {
	q := s.getQueryExecutor(ctx)
	var quantity int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(quantity, 0) FROM usage_records
		WHERE org_id = $1 AND feature = $2 AND period = $3`,
		orgID, feature, period).Scan(&quantity)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return quantity, nil
}

func (s *PostgresStore) RecordUsage(ctx context.Context, orgID string, feature string, quantity int64, period string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO usage_records (org_id, feature, period, quantity)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, feature, period)
		DO UPDATE SET quantity = usage_records.quantity + EXCLUDED.quantity, updated_at = NOW()`,
		orgID, feature, period, quantity)
	return err
}

func (s *PostgresStore) GetSubscription(ctx context.Context, orgID string) (*business.Subscription, error) {
	q := s.getQueryExecutor(ctx)
	var sub business.Subscription
	err := q.QueryRow(ctx, `
		SELECT id, org_id, plan_id, status, stripe_subscription_id, current_period_start, current_period_end
		FROM subscriptions WHERE org_id = $1 AND status IN ('active', 'trialing', 'past_due')
		LIMIT 1`, orgID).Scan(&sub.ID, &sub.OrgID, &sub.PlanID, &sub.Status,
		&sub.StripeSubscriptionID, &sub.CurrentPeriodStart, &sub.CurrentPeriodEnd)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sub, nil
}

func (s *PostgresStore) CreateSubscription(ctx context.Context, sub *business.Subscription) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO subscriptions (id, org_id, plan_id, status, stripe_subscription_id, current_period_start, current_period_end)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sub.ID, sub.OrgID, sub.PlanID, sub.Status, nilIfEmpty(sub.StripeSubscriptionID),
		sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	return err
}

func (s *PostgresStore) UpdateSubscription(ctx context.Context, sub *business.Subscription) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE subscriptions SET plan_id = $2, status = $3, stripe_subscription_id = $4,
			current_period_start = $5, current_period_end = $6, updated_at = NOW()
		WHERE id = $1`,
		sub.ID, sub.PlanID, sub.Status, nilIfEmpty(sub.StripeSubscriptionID),
		sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	return err
}
