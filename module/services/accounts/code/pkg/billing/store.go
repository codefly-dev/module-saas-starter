package billing

import (
	"context"
	"errors"
	"time"
)

// ProcessingStore contains the local billing reads and writes used by the
// asynchronous Stripe event processor.
type ProcessingStore interface {
	PlanByStripePriceID(ctx context.Context, stripePriceID string) (*PlanRef, error)
	OrgByStripeCustomerID(ctx context.Context, stripeCustomerID string) (string, error)
	UpsertSubscription(ctx context.Context, sub SubscriptionUpsert) error
	OwnerEmailByStripeCustomerID(ctx context.Context, stripeCustomerID string) (string, error)
}

// Store is the complete local projection surface used by the Stripe job
// handler. Durable receipt and lifecycle belong to the generic jobs platform.
type Store interface{ ProcessingStore }

// PlanRef is the minimum plan data the billing dispatcher needs.
type PlanRef struct {
	ID   string
	Name string
}

// SubscriptionUpsert is the normalized state written after resolving Stripe
// customer and price identifiers to local records.
type SubscriptionUpsert struct {
	OrgID                string
	PlanID               string
	Status               string
	StripeSubscriptionID string
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
	StateObservedAt      time.Time
}

var (
	ErrPlanNotFound                  = errors.New("billing: plan not found for stripe price id")
	ErrOrgNotFound                   = errors.New("billing: org not found for stripe customer id")
	ErrSubscriptionOwnershipMismatch = errors.New("billing: Stripe subscription belongs to a different organization")
)
