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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/billing"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// BillingClient is the subset of billing.Client we need here. Kept
// as an interface so tests can swap a fake.
type BillingClient interface {
	CreateCustomer(ctx context.Context, email, orgID, idempotencyKey string) (*billing.Customer, error)
	CreateCheckoutSession(ctx context.Context, p billing.CheckoutParams) (*billing.CheckoutSession, error)
	CreateBillingPortalSession(ctx context.Context, customerID, returnURL, idempotencyKey string) (*billing.BillingPortalSession, error)
	ListInvoices(ctx context.Context, customerID string, limit int) ([]billing.Invoice, error)
}

// SetBillingClient wires the Stripe client used by StartCheckout and
// OpenBillingPortal. Optional: when nil those methods return an error
// advising ops to set STRIPE_API_KEY.
func (s *Service) SetBillingClient(c BillingClient) {
	s.billing = c
}

// BillingRedirects are server-owned destinations for Stripe-hosted flows.
// Callers never supply arbitrary redirect URLs.
type BillingRedirects struct {
	CheckoutSuccessURL string
	CheckoutCancelURL  string
	PortalReturnURL    string
}

func (s *Service) SetBillingRedirects(redirects BillingRedirects) {
	s.billingURLs = redirects
}

// StartCheckoutInput is what the HTTP route passes in.
type StartCheckoutInput struct {
	UserID         string // the caller (for audit)
	OrgID          string
	PlanName       string // server-owned catalog key
	IdempotencyKey string // caller operation key; required and stable across retries
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
	if in.OrgID == "" || in.PlanName == "" {
		return "", w.NewError("StartCheckout requires OrgID and PlanName")
	}
	if err := validateBillingIdempotencyKey(in.IdempotencyKey); err != nil {
		return "", w.Wrapf(err, "invalid checkout idempotency key")
	}
	if s.billingURLs.CheckoutSuccessURL == "" || s.billingURLs.CheckoutCancelURL == "" {
		return "", w.NewError("billing checkout redirects are not configured")
	}

	plan, err := s.store.GetPlanByName(ctx, in.PlanName)
	if err != nil {
		return "", w.Wrapf(err, "look up plan %q", in.PlanName)
	}
	if plan.StripePriceID == "" {
		return "", w.NewError("plan %q has no stripe_price_id configured", in.PlanName)
	}
	if !plan.CheckoutEnabled {
		return "", w.NewError("plan %q is not available for checkout", in.PlanName)
	}

	customerID, err := s.ensureStripeCustomer(ctx, in.OrgID)
	if err != nil {
		return "", w.Wrapf(err, "ensure stripe customer")
	}

	session, err := s.billing.CreateCheckoutSession(ctx, billing.CheckoutParams{
		CustomerID:     customerID,
		PriceID:        plan.StripePriceID,
		SuccessURL:     s.billingURLs.CheckoutSuccessURL,
		CancelURL:      s.billingURLs.CheckoutCancelURL,
		OrgID:          in.OrgID,
		TrialDays:      plan.TrialDays,
		Currency:       plan.Currency,
		AutomaticTax:   plan.TaxBehavior == "automatic",
		IdempotencyKey: billingMutationKey("checkout", in.IdempotencyKey),
	})
	if err != nil {
		return "", w.Wrapf(err, "create checkout session")
	}

	s.emit(ctx, in.UserID, "user", "billing.checkout_started", "subscription", session.ID, in.OrgID)
	return session.URL, nil
}

// OpenBillingPortalInput is the HTTP route input.
type OpenBillingPortalInput struct {
	UserID         string
	OrgID          string
	IdempotencyKey string
}

// OpenBillingPortal returns the URL of a Stripe-hosted billing portal
// session for the org. The org must already have a stripe_customer_id
// (they must have started checkout at least once).
func (s *Service) OpenBillingPortal(ctx context.Context, in OpenBillingPortalInput) (string, error) {
	w := wool.Get(ctx).In("OpenBillingPortal")

	if s.billing == nil {
		return "", w.NewError("billing not configured")
	}
	if in.OrgID == "" {
		return "", w.NewError("OpenBillingPortal requires OrgID")
	}
	if err := validateBillingIdempotencyKey(in.IdempotencyKey); err != nil {
		return "", w.Wrapf(err, "invalid portal idempotency key")
	}
	if s.billingURLs.PortalReturnURL == "" {
		return "", w.NewError("billing portal return URL is not configured")
	}

	// stripe_customer_id is on organizations (RLS-protected, Phase 2F).
	var customerID string
	if err := s.store.WithOrgTx(ctx, in.OrgID, func(ctx context.Context) error {
		c, err := s.store.GetOrgStripeCustomerID(ctx, in.OrgID)
		customerID = c
		return err
	}); err != nil {
		return "", w.Wrapf(err, "get org stripe customer")
	}
	if customerID == "" {
		return "", w.NewError("org has no stripe customer — start a checkout first")
	}

	session, err := s.billing.CreateBillingPortalSession(
		ctx,
		customerID,
		s.billingURLs.PortalReturnURL,
		billingMutationKey("portal", in.IdempotencyKey),
	)
	if err != nil {
		return "", w.Wrapf(err, "create billing portal session")
	}

	s.emit(ctx, in.UserID, "user", "billing.portal_opened", "customer", customerID, in.OrgID)
	return session.URL, nil
}

// ListInvoices returns the org's recent Stripe invoices. Empty slice
// when the org has never started a checkout (no stripe customer id).
// limit clamps via the billing client; pass 0 for the default 12.
//
// Authz expectation: caller is org-admin, gated by the adapter.
func (s *Service) ListInvoices(ctx context.Context, orgID string, limit int) ([]billing.Invoice, error) {
	w := wool.Get(ctx).In("ListInvoices")
	if s.billing == nil {
		return nil, w.NewError("billing not configured")
	}
	var customerID string
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		c, err := s.store.GetOrgStripeCustomerID(ctx, orgID)
		customerID = c
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "get org stripe customer")
	}
	if customerID == "" {
		// Org never paid → no invoices. Empty list is the right answer
		// here; surfacing an error would noise the FE empty-state.
		return nil, nil
	}
	invoices, err := s.billing.ListInvoices(ctx, customerID, limit)
	if err != nil {
		return nil, w.Wrapf(err, "stripe list invoices")
	}
	return invoices, nil
}

// HandlePaymentFailed is called when a Stripe webhook reports a failed payment.
// It notifies the org owner and sends a critical alert to Slack.
//
// Stripe webhooks arrive without a tenant context (the webhook handler
// is unauthenticated by network boundary; the orgID comes from the
// Stripe customer mapping). Wrap the org read in WithOrgTx so RLS
// (Phase 2F) lets us resolve the owner.
func (s *Service) HandlePaymentFailed(ctx context.Context, orgID string) error {
	w := wool.Get(ctx).In("HandlePaymentFailed")

	var org *gen.Organization
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		o, err := s.store.GetOrganization(ctx, orgID)
		org = o
		return err
	}); err != nil {
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
//
// Both reads (Get + GetOrganization) and the write (Set) hit
// organizations (RLS-protected, Phase 2F) — bracket the whole flow
// in a single WithOrgTx so the SET LOCAL stays valid across reads
// AND writes. The Stripe API call itself is outside the tx since we
// don't want to hold a DB connection during a remote round-trip.
func (s *Service) ensureStripeCustomer(ctx context.Context, orgID string) (string, error) {
	var existing, ownerEmail string
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		c, err := s.store.GetOrgStripeCustomerID(ctx, orgID)
		if err != nil {
			return err
		}
		existing = c
		if existing != "" {
			return nil
		}
		org, err := s.store.GetOrganization(ctx, orgID)
		if err != nil {
			return err
		}
		if org != nil {
			ownerEmail, _ = s.store.GetOrganizationMemberPrimaryEmail(ctx, org.OwnerId)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("load existing customer: %w", err)
	}
	if existing != "" {
		return existing, nil
	}
	if ownerEmail == "" {
		ownerEmail = "billing+" + orgID + "@localhost"
	}

	customer, err := s.billing.CreateCustomer(
		ctx,
		ownerEmail,
		orgID,
		billingMutationKey("customer", orgID),
	)
	if err != nil {
		return "", fmt.Errorf("create stripe customer: %w", err)
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.SetOrgStripeCustomerID(ctx, orgID, customer.ID)
	}); err != nil {
		return "", fmt.Errorf("persist stripe customer id: %w", err)
	}
	return customer.ID, nil
}

// billingMutationKey namespaces and hashes caller keys so the same external
// token cannot collide across Stripe operations and arbitrary header length or
// tenant identifiers never leak into Stripe's request metadata.
func billingMutationKey(operation, seed string) string {
	digest := sha256.Sum256([]byte(operation + "\x00" + seed))
	return "saas-starter:" + operation + ":" + hex.EncodeToString(digest[:])
}

func validateBillingIdempotencyKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("Idempotency-Key is required")
	}
	// The key is hashed before it reaches Stripe, but bounding caller input
	// keeps request metadata and logs predictable.
	if len(key) > 512 {
		return fmt.Errorf("Idempotency-Key exceeds 512 characters")
	}
	return nil
}
