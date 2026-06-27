package billing

import (
	"context"
	"time"
)

// Store is the persistence surface the webhook handler needs. Kept
// minimal on purpose — the billing package stays decoupled from the
// main business.Store interface so it can evolve (and be tested)
// independently.
//
// Implementations:
//   - billing/pg (not yet written): real Postgres backed by the
//     existing plans/subscriptions/organizations/stripe_webhook_events
//     tables.
//   - billing (in _test.go): in-memory fake used by handler_test.go.
//
// All methods must be safe for concurrent use.
type Store interface {
	// WebhookAlreadyProcessed returns true if the event id has been
	// recorded previously. Used for Stripe retry idempotency.
	WebhookAlreadyProcessed(ctx context.Context, eventID string) (bool, error)

	// RecordWebhook inserts the event id + type at receipt. Called
	// before dispatching so concurrent retries conflict on the primary
	// key and return a duplicate error.
	RecordWebhook(ctx context.Context, eventID, eventType string) error

	// MarkWebhookProcessed stamps processed_at on the webhook row.
	// Called after a successful dispatch.
	MarkWebhookProcessed(ctx context.Context, eventID string) error

	// MarkWebhookFailed stamps the error message on the webhook row.
	// Called when dispatch fails; the event is NOT re-processed on the
	// next Stripe retry (idempotency row already exists).
	MarkWebhookFailed(ctx context.Context, eventID, errorMsg string) error

	// PlanByStripePriceID resolves a local plan from a Stripe price id
	// attached to a subscription. Returns ErrPlanNotFound if the price
	// isn't mapped in the plans table.
	PlanByStripePriceID(ctx context.Context, stripePriceID string) (*PlanRef, error)

	// OrgByStripeCustomerID resolves an internal org id from a Stripe
	// customer id. Returns ErrOrgNotFound if no mapping exists.
	OrgByStripeCustomerID(ctx context.Context, stripeCustomerID string) (string, error)

	// UpsertSubscription writes a subscription row, creating or updating
	// the existing active record for the org. Called from every
	// subscription-touching webhook event (created/updated/deleted).
	UpsertSubscription(ctx context.Context, sub SubscriptionUpsert) error

	// OwnerEmailByStripeCustomerID resolves the billing contact email
	// for a Stripe customer. Used by the webhook handler to send
	// notifications (payment failed, receipt, trial ending). Returns ""
	// if no email can be determined (caller skips the notification).
	OwnerEmailByStripeCustomerID(ctx context.Context, stripeCustomerID string) (string, error)
}

// PlanRef is the minimum plan data the billing dispatcher needs —
// just the id for the UpsertSubscription call.
type PlanRef struct {
	ID   string
	Name string
}

// SubscriptionUpsert is the shape the handler passes to Store after
// resolving customer → org and price → plan. The orchestration of
// "look up, rewrite, upsert" happens in the handler; the Store is
// pure persistence.
type SubscriptionUpsert struct {
	OrgID                string
	PlanID               string
	Status               string // active | past_due | canceled | trialing
	StripeSubscriptionID string
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
}

// Sentinel errors the handler checks against.
var (
	ErrPlanNotFound = errorf("billing: plan not found for stripe price id")
	ErrOrgNotFound  = errorf("billing: org not found for stripe customer id")
)

// errorf is a tiny helper for package-level sentinel errors.
func errorf(msg string) error { return staticErr(msg) }

type staticErr string

func (e staticErr) Error() string { return string(e) }
