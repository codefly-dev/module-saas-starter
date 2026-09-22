package business

import "time"

// EventSubscription is one live entry in the domain-event pub/sub control plane
// (issue #493): a subscriber principal receives every event whose type matches
// TypePattern on the named delivery Queue. It is the Store-boundary projection
// of a public.event_subscriptions row — a control-plane-owned platform relation
// with no tenant RLS — created either by materializing a catalog `consumes`
// entry at install or through ModuleCapabilitiesService.Subscribe at runtime,
// and resolved by the relay for fan-out.
//
// TypePattern is an exact type ("reference.console.viewed") or a single trailing
// wildcard ("reference.*"); events.Matches decides membership. Delivery is
// "ordered" or "unordered". A row with a non-zero RevokedAt is no longer live
// and never appears in the Store's List results.
type EventSubscription struct {
	ID                    string
	SubscriberPrincipalID string
	TypePattern           string
	Queue                 string
	Delivery              string
	CreatedBy             string
	CreatedAt             time.Time
}
