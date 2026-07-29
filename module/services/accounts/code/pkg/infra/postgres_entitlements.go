package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
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

// GetPlanByID resolves a plan row by id. Used by GetOrgEntitlements
// to surface the human-readable plan name on the dashboard.
func (s *PostgresStore) GetPlanByID(ctx context.Context, planID string) (*business.Plan, error) {
	q := s.getQueryExecutor(ctx)
	var p business.Plan
	err := q.QueryRow(ctx, `
		SELECT id, name, display_name, is_default, sort_order
		FROM plans WHERE id = $1`, planID,
	).Scan(&p.ID, &p.Name, &p.DisplayName, &p.IsDefault, &p.SortOrder)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// ListPlanEntitlements returns every (feature, limit) row for a plan.
// Limit is -1 for unlimited (NULL in DB), 0 for disabled, >0 for a
// metered cap. Used by GetOrgEntitlements to enumerate features.
func (s *PostgresStore) ListPlanEntitlements(ctx context.Context, planID string) ([]business.PlanFeatureLimit, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT feature, limit_value FROM plan_entitlements
		WHERE plan_id = $1
		ORDER BY feature`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.PlanFeatureLimit
	for rows.Next() {
		var fl business.PlanFeatureLimit
		var limit *int64
		if err := rows.Scan(&fl.Feature, &limit); err != nil {
			return nil, err
		}
		if limit == nil {
			fl.Limit = -1 // unlimited
		} else {
			fl.Limit = *limit
		}
		out = append(out, fl)
	}
	return out, nil
}

// ListEntitlementOverrides returns every per-org override. Used so
// GetOrgEntitlements can stamp has_override = true on the resolved
// rows without N+1 lookups against GetEntitlementOverride.
func (s *PostgresStore) ListEntitlementOverrides(ctx context.Context, orgID string) ([]*business.EntitlementOverride, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, org_id, feature, limit_value, reason, created_by, expires_at
		FROM entitlement_overrides WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*business.EntitlementOverride
	for rows.Next() {
		var o business.EntitlementOverride
		var limitValue *int64
		var expiresAt *time.Time
		var createdBy *string
		if err := rows.Scan(&o.ID, &o.OrgID, &o.Feature, &limitValue,
			&o.Reason, &createdBy, &expiresAt); err != nil {
			return nil, err
		}
		o.LimitValue = limitValue
		o.ExpiresAt = expiresAt
		if createdBy != nil {
			o.CreatedBy = *createdBy
		}
		out = append(out, &o)
	}
	return out, nil
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

// LockEntitlementQuota serializes live-resource admission for one
// organization and entitlement. It must be held in the same transaction as
// both the authoritative count and the resource insert; otherwise a pair of
// concurrent requests could observe the same remaining slot.
func (s *PostgresStore) LockEntitlementQuota(ctx context.Context, orgID string, feature string) error {
	if _, ok := ctx.Value("tx").(pgx.Tx); !ok { //nolint:staticcheck // shared transaction context key
		return errors.New("entitlement quota lock requires a tenant transaction")
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"cardinality:"+orgID+":"+feature,
	)
	if err != nil {
		return fmt.Errorf("lock %s entitlement quota: %w", feature, err)
	}
	return nil
}

func (s *PostgresStore) GetUsageTotal(ctx context.Context, orgID string, meter string, periodStart time.Time) (int64, error) {
	q := s.getQueryExecutor(ctx)
	var quantity int64
	err := q.QueryRow(ctx, `
		SELECT quantity FROM usage_totals
		WHERE org_id = $1 AND meter = $2 AND period_start = $3`,
		orgID, meter, periodStart).Scan(&quantity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return quantity, nil
}

func (s *PostgresStore) GetUsageBuckets(
	ctx context.Context,
	orgID string,
	meter string,
	from time.Time,
	to time.Time,
	bucket business.UsageBucketInterval,
) ([]business.UsageBucketValue, error) {
	var truncate string
	switch bucket {
	case business.UsageBucketHour:
		truncate = "hour"
	case business.UsageBucketDay:
		truncate = "day"
	case business.UsageBucketMonth:
		truncate = "month"
	default:
		return nil, errors.New("unsupported usage bucket")
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT date_trunc($5, occurred_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
		       SUM(quantity)
		FROM usage_events
		WHERE org_id = $1
		  AND meter = $2
		  AND occurred_at >= $3
		  AND occurred_at < $4
		  AND accepted = TRUE
		GROUP BY bucket_start
		ORDER BY bucket_start`,
		orgID, meter, from, to, truncate,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.UsageBucketValue
	for rows.Next() {
		var value business.UsageBucketValue
		if err := rows.Scan(&value.Start, &value.Quantity); err != nil {
			return nil, err
		}
		value.Start = value.Start.UTC()
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ConsumeUsage(ctx context.Context, consumption business.UsageConsumption) (*business.UsageReceipt, error) {
	// The advisory idempotency lock and aggregate row lock only have meaning in
	// one explicit transaction. The business layer always enters through
	// WithOrgTx; reject accidental direct calls instead of weakening atomicity.
	if _, ok := ctx.Value("tx").(pgx.Tx); !ok { //nolint:staticcheck // shared transaction context key
		return nil, errors.New("usage consumption requires a tenant transaction")
	}
	q := s.getQueryExecutor(ctx)

	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		consumption.OrgID+":"+consumption.IdempotencyKey); err != nil {
		return nil, fmt.Errorf("lock usage idempotency key: %w", err)
	}

	var (
		existing    business.UsageReceipt
		requestHash []byte
	)
	err := q.QueryRow(ctx, `
		SELECT id, org_id, meter, quantity, accepted, total_after,
		       limit_at_ingestion, period_start, period_end, occurred_at, request_hash
		FROM usage_events
		WHERE org_id = $1 AND idempotency_key = $2`,
		consumption.OrgID, consumption.IdempotencyKey,
	).Scan(
		&existing.EventID, &existing.OrgID, &existing.Meter, &existing.Quantity,
		&existing.Accepted, &existing.Used, &existing.Limit, &existing.PeriodStart,
		&existing.PeriodEnd, &existing.OccurredAt, &requestHash,
	)
	if err == nil {
		if !bytes.Equal(requestHash, consumption.RequestHash[:]) {
			return nil, business.ErrUsageIdempotencyConflict
		}
		existing.Duplicate = true
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read usage idempotency receipt: %w", err)
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO usage_totals (org_id, meter, period_start, period_end, quantity)
		VALUES ($1, $2, $3, $4, 0)
		ON CONFLICT (org_id, meter, period_start) DO NOTHING`,
		consumption.OrgID, consumption.Meter, consumption.PeriodStart, consumption.PeriodEnd,
	); err != nil {
		return nil, fmt.Errorf("initialize usage total: %w", err)
	}

	var used int64
	if err := q.QueryRow(ctx, `
		SELECT quantity FROM usage_totals
		WHERE org_id = $1 AND meter = $2 AND period_start = $3
		FOR UPDATE`,
		consumption.OrgID, consumption.Meter, consumption.PeriodStart,
	).Scan(&used); err != nil {
		return nil, fmt.Errorf("lock usage total: %w", err)
	}
	if consumption.Quantity > math.MaxInt64-used {
		return nil, errors.New("usage total exceeds bigint capacity")
	}

	totalAfter := used
	accepted := consumption.Limit == -1 || used+consumption.Quantity <= consumption.Limit
	if accepted {
		totalAfter = used + consumption.Quantity
		if _, err := q.Exec(ctx, `
			UPDATE usage_totals
			SET quantity = $4, period_end = $5, updated_at = CURRENT_TIMESTAMP
			WHERE org_id = $1 AND meter = $2 AND period_start = $3`,
			consumption.OrgID, consumption.Meter, consumption.PeriodStart, totalAfter, consumption.PeriodEnd,
		); err != nil {
			return nil, fmt.Errorf("update usage total: %w", err)
		}
	}

	dimensions, err := json.Marshal(consumption.Dimensions)
	if err != nil {
		return nil, fmt.Errorf("encode usage dimensions: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO usage_events (
			id, org_id, meter, quantity, idempotency_key, request_hash,
			accepted, occurred_at, period_start, period_end, dimensions,
			limit_at_ingestion, total_after
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		consumption.EventID, consumption.OrgID, consumption.Meter, consumption.Quantity,
		consumption.IdempotencyKey, consumption.RequestHash[:], accepted, consumption.OccurredAt,
		consumption.PeriodStart, consumption.PeriodEnd, dimensions, consumption.Limit, totalAfter,
	); err != nil {
		return nil, fmt.Errorf("insert usage event: %w", err)
	}

	return &business.UsageReceipt{
		EventID: consumption.EventID, OrgID: consumption.OrgID, Meter: consumption.Meter,
		Quantity: consumption.Quantity, Accepted: accepted, Used: totalAfter,
		Limit: consumption.Limit, PeriodStart: consumption.PeriodStart,
		PeriodEnd: consumption.PeriodEnd, OccurredAt: consumption.OccurredAt,
	}, nil
}

func (s *PostgresStore) GetSubscription(ctx context.Context, orgID string) (*business.Subscription, error) {
	q := s.getQueryExecutor(ctx)
	var sub business.Subscription
	err := q.QueryRow(ctx, `
		SELECT id, org_id, plan_id, status, COALESCE(stripe_subscription_id, ''),
		       current_period_start, current_period_end
		FROM subscriptions
		WHERE org_id = $1
		  AND status IN ('incomplete', 'trialing', 'active', 'past_due', 'unpaid', 'paused')
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
