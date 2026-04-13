package business

// Billing operations exposed on Service.
//
// These are the methods the HTTP billing routes call into:
//
//   StartCheckout    — /v1/billing/checkout (user clicked "Upgrade")
//   OpenBillingPortal — /v1/billing/portal  (user clicked "Manage billing")
//
// They both need a Stripe customer record for the org. We create one
// lazily on first checkout and store the id on organizations.
// stripe_customer_id for subsequent calls.
//
// The billing.Client lives on Service via SetBillingClient (nil by
// default so dev runs without a Stripe account still work — calling
// StartCheckout when unwired returns a clear error).

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/wool"

	"api/pkg/billing"
)

// BillingClient is the subset of billing.Client we need here. Kept
// as an interface so tests can swap a fake.
type BillingClient interface {
	CreateCustomer(ctx context.Context, email, orgID string) (*billing.Customer, error)
	CreateCheckoutSession(ctx context.Context, p billing.CheckoutParams) (*billing.CheckoutSession, error)
	CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (*billing.BillingPortalSession, error)
}

// SetBillingClient wires the Stripe client used by StartCheckout and
// OpenBillingPortal. Optional: when nil those methods return an error
// advising ops to set STRIPE_API_KEY.
func (s *Service) SetBillingClient(c BillingClient) {
	s.billing = c
}

// StartCheckoutInput is what the HTTP route passes in.
type StartCheckoutInput struct {
	UserID     string // the caller (for audit)
	OrgID      string
	PlanName   string // "pro" | "enterprise" | ...
	SuccessURL string // where Stripe redirects on payment success
	CancelURL  string // where Stripe redirects on abort
	TrialDays  int    // 0 = no trial
}

// StartCheckout builds a Stripe Checkout session for the given org.
//
// Lazy customer creation:
//   - If the org already has stripe_customer_id, reuse it.
//   - Otherwise create a new customer with the org's owner email (or
//     any admin's email) and stamp the id.
//
// Returns the Stripe-hosted checkout URL — the caller redirects the
// browser there.
func (s *Service) StartCheckout(ctx context.Context, in StartCheckoutInput) (string, error) {
	w := wool.Get(ctx).In("StartCheckout")

	if s.billing == nil {
		return "", w.NewError("billing not configured: STRIPE_API_KEY missing")
	}
	if in.OrgID == "" || in.PlanName == "" || in.SuccessURL == "" || in.CancelURL == "" {
		return "", w.NewError("StartCheckout requires OrgID, PlanName, SuccessURL, CancelURL")
	}

	plan, err := s.store.GetPlanByName(ctx, in.PlanName)
	if err != nil {
		return "", w.Wrapf(err, "look up plan %q", in.PlanName)
	}
	if plan.StripePriceID == "" {
		return "", w.NewError("plan %q has no stripe_price_id configured", in.PlanName)
	}

	customerID, err := s.ensureStripeCustomer(ctx, in.OrgID)
	if err != nil {
		return "", w.Wrapf(err, "ensure stripe customer")
	}

	session, err := s.billing.CreateCheckoutSession(ctx, billing.CheckoutParams{
		CustomerID: customerID,
		PriceID:    plan.StripePriceID,
		SuccessURL: in.SuccessURL,
		CancelURL:  in.CancelURL,
		OrgID:      in.OrgID,
		TrialDays:  in.TrialDays,
	})
	if err != nil {
		return "", w.Wrapf(err, "create checkout session")
	}

	s.emit(ctx, in.UserID, "user", "billing.checkout_started", "subscription", session.ID, in.OrgID)
	return session.URL, nil
}

// OpenBillingPortalInput is the HTTP route input.
type OpenBillingPortalInput struct {
	UserID    string
	OrgID     string
	ReturnURL string
}

// OpenBillingPortal returns the URL of a Stripe-hosted billing portal
// session for the org. The org must already have a stripe_customer_id
// (they must have started checkout at least once).
func (s *Service) OpenBillingPortal(ctx context.Context, in OpenBillingPortalInput) (string, error) {
	w := wool.Get(ctx).In("OpenBillingPortal")

	if s.billing == nil {
		return "", w.NewError("billing not configured")
	}
	if in.OrgID == "" || in.ReturnURL == "" {
		return "", w.NewError("OpenBillingPortal requires OrgID and ReturnURL")
	}

	customerID, err := s.store.GetOrgStripeCustomerID(ctx, in.OrgID)
	if err != nil {
		return "", w.Wrapf(err, "get org stripe customer")
	}
	if customerID == "" {
		return "", w.NewError("org has no stripe customer — start a checkout first")
	}

	session, err := s.billing.CreateBillingPortalSession(ctx, customerID, in.ReturnURL)
	if err != nil {
		return "", w.Wrapf(err, "create billing portal session")
	}

	s.emit(ctx, in.UserID, "user", "billing.portal_opened", "customer", customerID, in.OrgID)
	return session.URL, nil
}

// HandlePaymentFailed is called when a Stripe webhook reports a failed payment.
// It notifies the org owner and sends a critical alert to Slack.
func (s *Service) HandlePaymentFailed(ctx context.Context, orgID string) error {
	w := wool.Get(ctx).In("HandlePaymentFailed")

	org, err := s.store.GetOrganization(ctx, orgID)
	if err != nil {
		return w.Wrapf(err, "cannot get organization")
	}
	if org == nil {
		return w.NewError("organization not found: %s", orgID)
	}

	// Notify the org owner
	_ = s.NotifyUser(ctx, org.OwnerId, "Payment failed", fmt.Sprintf("Payment failed for %s. Please update your payment method.", org.Name))

	// Critical event: also notify via Slack
	s.notifySlack(ctx, fmt.Sprintf(":rotating_light: Payment failed for org %s (%s)", org.Name, orgID))

	s.emit(ctx, "system", "system", "billing.payment_failed", "organization", orgID, orgID)

	return nil
}

// ensureStripeCustomer loads or creates the Stripe customer for an org.
// Runs on the first StartCheckout; subsequent calls hit the fast path.
func (s *Service) ensureStripeCustomer(ctx context.Context, orgID string) (string, error) {
	existing, err := s.store.GetOrgStripeCustomerID(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("load existing customer: %w", err)
	}
	if existing != "" {
		return existing, nil
	}

	// No customer yet — look up the org's owner to use their email as
	// the Stripe customer's primary email.
	org, err := s.store.GetOrganization(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("load org: %w", err)
	}
	ownerID := ""
	if org != nil {
		ownerID = org.OwnerId
	}
	var ownerEmail string
	if ownerID != "" {
		user, err := s.store.GetUser(ctx, ownerID)
		if err == nil && user != nil {
			ownerEmail = user.PrimaryEmail
		}
	}
	if ownerEmail == "" {
		ownerEmail = "billing+" + orgID + "@localhost"
	}

	customer, err := s.billing.CreateCustomer(ctx, ownerEmail, orgID)
	if err != nil {
		return "", fmt.Errorf("create stripe customer: %w", err)
	}
	if err := s.store.SetOrgStripeCustomerID(ctx, orgID, customer.ID); err != nil {
		return "", fmt.Errorf("persist stripe customer id: %w", err)
	}
	return customer.ID, nil
}
