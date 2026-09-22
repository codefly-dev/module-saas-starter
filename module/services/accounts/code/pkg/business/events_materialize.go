package business

import (
	"context"

	"accounts/pkg/eventcatalog"

	"github.com/codefly-dev/core/wool"
)

// MaterializeSubscriptionsFromCatalog turns the composed catalog's `consumes`
// declarations into durable subscription rows for one subscriber principal —
// the compose/install half of the Subscribe grant (EVENTS.md §Subscriptions,
// issue #493 §5/§6). For every catalog `consumes` entry whose namespace is one
// the subscriber declares, it creates the exact-pattern subscription the entry
// describes (`type_pattern := consumes.type`, `queue`, `delivery`), keyed to the
// subscriber principal. The install hook calls this with the fresh install's
// agent principal and the namespaces the installed solution consumes from, so a
// solution starts receiving its declared events the moment it is installed,
// without a runtime Subscribe round-trip.
//
// It runs on the control plane (event_subscriptions has no tenant RLS) and is
// idempotent: CreateEventSubscription collapses a re-materialization of the same
// (principal, pattern, queue) onto the existing live row, so a reinstall or a
// retried install never doubles a subscription and only a genuinely new row is
// audited. A consume that resolves to an `internal`-visibility published type is
// skipped, never materialized: internal events stay intra-platform and are never
// delivered to a solution principal (the same rule ModuleSubscribe enforces at
// runtime). Materialization is therefore safe to call after the install commits;
// a failure here leaves the install intact and is recovered by the next install
// or a runtime Subscribe.
func (s *Service) MaterializeSubscriptionsFromCatalog(ctx context.Context, subscriberPrincipalID, createdBy string, namespaces []string) error {
	w := wool.Get(ctx).In("MaterializeSubscriptionsFromCatalog",
		wool.Field("subscriber_principal_id", subscriberPrincipalID))
	if subscriberPrincipalID == "" || len(namespaces) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(namespaces))
	for _, n := range namespaces {
		wanted[n] = struct{}{}
	}
	for _, consume := range eventcatalog.Consumed() {
		if _, ok := wanted[consume.Subscriber]; !ok {
			continue
		}
		// A consume always names a registered published type (compose validates
		// this); an internal one is never subscribable by a solution principal, so
		// materialization skips it rather than minting an ineligible grant.
		if published, ok := eventcatalog.LookupPublished(consume.Type); ok && published.Visibility == "internal" {
			continue
		}
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			sub, inserted, e := s.store.CreateEventSubscription(ctx, &EventSubscription{
				SubscriberPrincipalID: subscriberPrincipalID,
				TypePattern:           consume.Type,
				Queue:                 consume.Queue,
				Delivery:              consume.Delivery,
				CreatedBy:             createdBy,
			})
			if e != nil {
				return e
			}
			if !inserted {
				return nil // idempotent: the subscription already exists, no second audit
			}
			return s.emitTx(ctx, createdBy, "agent", EventEventSubscriptionCreated, "event_subscription", sub.ID, "", map[string]any{
				"subscription_id":         sub.ID,
				"subscriber_principal_id": sub.SubscriberPrincipalID,
				"type_pattern":            sub.TypePattern,
				"queue":                   sub.Queue,
			})
		}); err != nil {
			return w.Wrapf(err, "cannot materialize subscription for %q", consume.Type)
		}
	}
	return nil
}
