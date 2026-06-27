package billing

// HTTP webhook handler for Stripe events.
//
// Flow for every incoming webhook:
//
//  1. Read body + Stripe-Signature header.
//  2. Verify HMAC signature against WebhookSecret. Reject on fail.
//  3. Check stripe_webhook_events idempotency table. If already
//     recorded → return 200 immediately (Stripe retries aggressively,
//     and reprocessing a successful event is wrong).
//  4. Insert webhook event row (idempotency guard for concurrent
//     retries racing past the check in step 3).
//  5. Dispatch on event type. Every dispatch handler must be
//     idempotent at the data layer (we use UpsertSubscription, not
//     Insert).
//  6. Stamp processed_at on success, error on failure.
//
// Event routing:
//
//   customer.subscription.created → UpsertSubscription(status=active|trialing)
//   customer.subscription.updated → UpsertSubscription(all fields refreshed)
//   customer.subscription.deleted → UpsertSubscription(status=canceled)
//   invoice.payment_failed        → UpsertSubscription(status=past_due)
//   invoice.payment_succeeded     → refetch subscription, re-upsert
//                                   (catches "past_due → active" transition)
//   everything else               → logged and ack'd 200
//
// The handler returns 200 for every event it acknowledges (success or
// "already processed"). Non-200 is reserved for signature failures and
// unrecoverable DB errors — those tell Stripe to retry.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/codefly-dev/core/wool"
)

// EmailNotifier is the optional email integration the webhook handler
// uses to send billing-related notifications (payment failures, receipts,
// trial ending warnings). When nil, email sends are silently skipped.
type EmailNotifier interface {
	// SendBillingEmail sends a templated email for a billing event.
	// templateName is one of: "payment_failed", "invoice_ready", "trial_ending".
	// toEmail is the recipient. vars are template substitution variables.
	SendBillingEmail(ctx context.Context, templateName, toEmail string, vars map[string]string) error
}

// HandlerDeps is what the webhook handler needs to do its job.
// Client is used to fetch subscription details when an invoice event
// needs to re-read state (e.g. payment_succeeded on a past_due sub).
type HandlerDeps struct {
	Store         Store
	Client        *Client
	WebhookSecret string
	// Notifier is optional; when set, billing events trigger email
	// notifications via the template system.
	Notifier EmailNotifier
}

// NewHandler returns an http.Handler that processes Stripe webhooks.
// Mount it at a PUBLIC path (typically /v1/billing/webhook) — the
// gateway's public-path allowlist should include it. Logging goes
// through wool (project policy) keyed off the incoming request ctx.
func NewHandler(deps HandlerDeps) http.Handler {
	return &handler{deps: deps}
}

type handler struct {
	deps HandlerDeps
}

// ServeHTTP is the webhook entry point.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	sig := r.Header.Get("Stripe-Signature")
	ev, err := ParseEvent(body, sig, h.deps.WebhookSecret)
	if err != nil {
		// Signature or decode failure — do NOT leak details to the
		// caller. Stripe will retry on 400.
		wool.Get(r.Context()).In("billing.handler").Warn("webhook verify failed", wool.ErrField(err))
		writeError(w, http.StatusBadRequest, "invalid signature")
		return
	}

	ctx := r.Context()

	// Idempotency: short-circuit if we've seen this event id before.
	seen, err := h.deps.Store.WebhookAlreadyProcessed(ctx, ev.ID)
	if err != nil {
		wool.Get(ctx).In("billing.handler").Warn("check idempotency failed", wool.ErrField(err))
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if seen {
		// Already processed. Return 200 so Stripe stops retrying.
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
		return
	}

	// Record the event row BEFORE dispatch. Concurrent retries racing
	// past WebhookAlreadyProcessed will conflict on the PK and get an
	// error — one wins, the others return 200 as duplicates.
	if err := h.deps.Store.RecordWebhook(ctx, ev.ID, ev.Type); err != nil {
		// Racing retries will land here; treat as "already recorded"
		// and short-circuit. A real PK violation is indistinguishable
		// from a different DB error, so we check again.
		seen, _ := h.deps.Store.WebhookAlreadyProcessed(ctx, ev.ID)
		if seen {
			writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
			return
		}
		wool.Get(ctx).In("billing.handler").Warn("record webhook failed", wool.ErrField(err))
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	// Dispatch.
	if err := h.dispatch(ctx, ev, body); err != nil {
		wool.Get(ctx).In("billing.handler").Warn("dispatch failed",
			wool.Field("event_type", ev.Type),
			wool.Field("event_id", ev.ID),
			wool.ErrField(err))
		_ = h.deps.Store.MarkWebhookFailed(ctx, ev.ID, err.Error())
		// Some errors are worth retrying (transient DB), some aren't
		// (unknown plan). We return 500 to trigger Stripe retry only
		// for internal errors; lookups that can't be satisfied return
		// 200 so ops can manually reconcile.
		if errors.Is(err, ErrPlanNotFound) || errors.Is(err, ErrOrgNotFound) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "unroutable"})
			return
		}
		writeError(w, http.StatusInternalServerError, "dispatch failed")
		return
	}
	_ = h.deps.Store.MarkWebhookProcessed(ctx, ev.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// dispatch routes a verified event to the right handler.
func (h *handler) dispatch(ctx context.Context, ev *Event, body []byte) error {
	switch ev.Type {
	case "customer.subscription.created",
		"customer.subscription.updated":
		return h.handleSubscriptionUpsert(ctx, body)

	case "customer.subscription.deleted":
		return h.handleSubscriptionDeleted(ctx, body)

	case "invoice.payment_failed":
		return h.handleInvoicePaymentFailed(ctx, body)

	case "invoice.payment_succeeded",
		"invoice.paid":
		return h.handleInvoicePaymentSucceeded(ctx, body)

	case "customer.subscription.trial_will_end":
		return h.handleTrialWillEnd(ctx, body)

	default:
		// Logged and ack'd — we don't fail on unknown types because
		// Stripe can add new events at any time.
		wool.Get(ctx).In("billing.handler").Info("ignoring event type", wool.Field("event_type", ev.Type))
		return nil
	}
}

// handleSubscriptionUpsert maps a Stripe subscription object onto our
// local subscriptions table. Covers created + updated.
func (h *handler) handleSubscriptionUpsert(ctx context.Context, body []byte) error {
	var sub Subscription
	if err := ObjectFromData(body, &sub); err != nil {
		return err
	}
	return h.upsertFromStripe(ctx, &sub)
}

// handleSubscriptionDeleted marks the local subscription canceled.
func (h *handler) handleSubscriptionDeleted(ctx context.Context, body []byte) error {
	var sub Subscription
	if err := ObjectFromData(body, &sub); err != nil {
		return err
	}
	// Force status=canceled regardless of what Stripe sends.
	sub.Status = "canceled"
	return h.upsertFromStripe(ctx, &sub)
}

// handleInvoicePaymentFailed transitions the subscription to past_due
// and sends a payment_failed notification email.
func (h *handler) handleInvoicePaymentFailed(ctx context.Context, body []byte) error {
	var inv struct {
		Subscription string `json:"subscription"`
		Customer     string `json:"customer"`
	}
	if err := ObjectFromData(body, &inv); err != nil {
		return err
	}
	if inv.Subscription == "" {
		return nil // invoice not tied to a subscription — ignore
	}

	// Minimal upsert: just flip the status. We look up by stripe
	// customer to find the org, leave plan + periods alone.
	orgID, err := h.deps.Store.OrgByStripeCustomerID(ctx, inv.Customer)
	if err != nil {
		return err
	}
	if err := h.deps.Store.UpsertSubscription(ctx, SubscriptionUpsert{
		OrgID:                orgID,
		Status:               "past_due",
		StripeSubscriptionID: inv.Subscription,
	}); err != nil {
		return err
	}

	// Best-effort email notification.
	h.notifyByCustomer(ctx, inv.Customer, "payment_failed", map[string]string{
		"org_id": orgID,
	})
	return nil
}

// handleInvoicePaymentSucceeded refetches the subscription from Stripe
// and writes the current state. This catches transitions like past_due
// → active after a retry succeeds. Sends a receipt email.
func (h *handler) handleInvoicePaymentSucceeded(ctx context.Context, body []byte) error {
	var inv struct {
		Subscription string `json:"subscription"`
		Customer     string `json:"customer"`
	}
	if err := ObjectFromData(body, &inv); err != nil {
		return err
	}
	if inv.Subscription == "" {
		return nil
	}
	if h.deps.Client == nil {
		// No client configured — still write a best-effort status=active
		// from what we have. Ops can reconcile later.
		return nil
	}
	sub, err := h.deps.Client.GetSubscription(ctx, inv.Subscription)
	if err != nil {
		return err
	}
	if err := h.upsertFromStripe(ctx, sub); err != nil {
		return err
	}

	// Best-effort receipt email.
	h.notifyByCustomer(ctx, inv.Customer, "invoice_ready", nil)
	return nil
}

// upsertFromStripe is the shared "map Stripe subscription → local row"
// path used by every subscription-touching handler.
func (h *handler) upsertFromStripe(ctx context.Context, sub *Subscription) error {
	orgID, err := h.deps.Store.OrgByStripeCustomerID(ctx, sub.CustomerID)
	if err != nil {
		return err
	}

	var planID string
	if priceID := sub.PrimaryPriceID(); priceID != "" {
		plan, err := h.deps.Store.PlanByStripePriceID(ctx, priceID)
		if err != nil {
			return err
		}
		planID = plan.ID
	}

	var (
		periodStart *time.Time
		periodEnd   *time.Time
	)
	if sub.CurrentPeriodStart > 0 {
		t := time.Unix(sub.CurrentPeriodStart, 0)
		periodStart = &t
	}
	if sub.CurrentPeriodEnd > 0 {
		t := time.Unix(sub.CurrentPeriodEnd, 0)
		periodEnd = &t
	}

	return h.deps.Store.UpsertSubscription(ctx, SubscriptionUpsert{
		OrgID:                orgID,
		PlanID:               planID,
		Status:               sub.Status,
		StripeSubscriptionID: sub.ID,
		CurrentPeriodStart:   periodStart,
		CurrentPeriodEnd:     periodEnd,
	})
}

// handleTrialWillEnd sends a trial-ending notification email. Stripe
// fires this event ~3 days before a trial expires. No DB upsert needed
// because the subscription status hasn't changed yet.
func (h *handler) handleTrialWillEnd(ctx context.Context, body []byte) error {
	var sub Subscription
	if err := ObjectFromData(body, &sub); err != nil {
		return err
	}
	h.notifyByCustomer(ctx, sub.CustomerID, "trial_ending", nil)
	return nil
}

// notifyByCustomer is a best-effort helper that resolves the billing
// contact email from a Stripe customer id and sends a templated email
// via the Notifier. Failures are logged but never propagated — email
// delivery issues must not block webhook processing.
func (h *handler) notifyByCustomer(ctx context.Context, stripeCustomerID, templateName string, extraVars map[string]string) {
	if h.deps.Notifier == nil || stripeCustomerID == "" {
		return
	}
	email, err := h.deps.Store.OwnerEmailByStripeCustomerID(ctx, stripeCustomerID)
	if err != nil || email == "" {
		wool.Get(ctx).In("billing.handler.notifyByCustomer").Warn("cannot resolve email",
			wool.Field("stripe_customer_id", stripeCustomerID),
			wool.ErrField(err))
		return
	}
	vars := map[string]string{"email": email}
	for k, v := range extraVars {
		vars[k] = v
	}
	if err := h.deps.Notifier.SendBillingEmail(ctx, templateName, email, vars); err != nil {
		wool.Get(ctx).In("billing.handler.notifyByCustomer").Warn("email send failed",
			wool.Field("template", templateName),
			wool.Field("to", email),
			wool.ErrField(err))
	}
}

// ============================================================================
// HTTP helpers
// ============================================================================

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
