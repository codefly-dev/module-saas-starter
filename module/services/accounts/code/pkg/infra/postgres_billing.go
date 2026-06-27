package infra

// Billing-specific store methods: Stripe customer mapping + plan lookup
// by name. These back the billing.Service.StartCheckout /
// OpenBillingPortal flows without exposing pkg/billing's internal
// PlanRef type to the business layer.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// GetOrgStripeCustomerID returns the stored stripe_customer_id for an
// org or an empty string if the org exists but has no customer yet.
// Returns pgx.ErrNoRows semantics only for a missing org (unusual —
// treat as bug); otherwise returns "" so the caller can create one.
func (s *PostgresStore) GetOrgStripeCustomerID(ctx context.Context, orgID string) (string, error) {
	q := s.getQueryExecutor(ctx)
	var customerID *string
	err := q.QueryRow(ctx,
		`SELECT stripe_customer_id FROM organizations WHERE id = $1`,
		orgID,
	).Scan(&customerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if customerID == nil {
		return "", nil
	}
	return *customerID, nil
}

// SetOrgStripeCustomerID stores a freshly-created Stripe customer id
// on the org row. Called once per org on the first StartCheckout.
func (s *PostgresStore) SetOrgStripeCustomerID(ctx context.Context, orgID, stripeCustomerID string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx,
		`UPDATE organizations SET stripe_customer_id = $2, updated_at = NOW() WHERE id = $1`,
		orgID, stripeCustomerID,
	)
	return err
}

// GetPlanByName returns the full plan row including the Stripe price
// id. Used by StartCheckout to map "pro" / "enterprise" / etc. onto
// a Stripe line item.
func (s *PostgresStore) GetPlanByName(ctx context.Context, name string) (*business.PlanFull, error) {
	q := s.getQueryExecutor(ctx)
	var (
		p               business.PlanFull
		stripeProductID *string
		stripePriceID   *string
	)
	err := q.QueryRow(ctx, `
		SELECT id::text, name, display_name, is_default, sort_order,
		       stripe_product_id, stripe_price_id, currency
		FROM plans
		WHERE name = $1`,
		name,
	).Scan(&p.ID, &p.Name, &p.DisplayName, &p.IsDefault, &p.SortOrder,
		&stripeProductID, &stripePriceID, &p.Currency)
	if err != nil {
		return nil, err
	}
	if stripeProductID != nil {
		p.StripeProductID = *stripeProductID
	}
	if stripePriceID != nil {
		p.StripePriceID = *stripePriceID
	}
	return &p, nil
}
