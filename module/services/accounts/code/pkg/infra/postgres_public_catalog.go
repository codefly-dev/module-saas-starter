package infra

import (
	"context"
	"strings"

	"accounts/pkg/business"
)

func (s *PostgresStore) ListPublicPlans(ctx context.Context) ([]business.PublicPlan, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT p.name, p.display_name, p.description, p.currency,
		       p.amount_minor, p.billing_interval,
		       p.checkout_enabled AND COALESCE(p.stripe_price_id, '') <> '',
		       p.contact_sales, p.trial_days, p.tax_behavior, p.fixture,
		       pe.feature, pe.limit_value
		FROM plans p
		LEFT JOIN plan_entitlements pe ON pe.plan_id = p.id
		WHERE p.public_visible
		ORDER BY p.sort_order, p.name, pe.feature`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []business.PublicPlan
	var current *business.PublicPlan
	for rows.Next() {
		var (
			plan        business.PublicPlan
			amountMinor *int64
			feature     *string
			limit       *int64
		)
		if err := rows.Scan(
			&plan.Key,
			&plan.Name,
			&plan.Description,
			&plan.Currency,
			&amountMinor,
			&plan.Interval,
			&plan.CheckoutEnabled,
			&plan.ContactSales,
			&plan.TrialDays,
			&plan.TaxBehavior,
			&plan.Fixture,
			&feature,
			&limit,
		); err != nil {
			return nil, err
		}
		if current == nil || current.Key != plan.Key {
			plan.Currency = strings.ToUpper(plan.Currency)
			if amountMinor != nil {
				plan.AmountMinor = *amountMinor
			}
			plans = append(plans, plan)
			current = &plans[len(plans)-1]
		}
		if feature != nil {
			value := int64(-1)
			if limit != nil {
				value = *limit
			}
			current.Entitlements = append(current.Entitlements, business.PlanFeatureLimit{
				Feature: *feature,
				Limit:   value,
			})
		}
	}
	return plans, rows.Err()
}
