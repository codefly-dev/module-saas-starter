package email

import (
	"context"
	"time"
)

// DeliveryStatus is the canonical, provider-neutral delivery projection. Every
// adapter translates its native event vocabulary into one of these (or the
// empty value for events that do not advance delivery state) so persistence and
// the invitation projection never learn a provider's event names. The values
// match the projecting subset of the invitations.delivery_status enum.
type DeliveryStatus string

const (
	DeliveryStatusSent       DeliveryStatus = "sent"
	DeliveryStatusDelivered  DeliveryStatus = "delivered"
	DeliveryStatusBounced    DeliveryStatus = "bounced"
	DeliveryStatusComplained DeliveryStatus = "complained"
)

// DeliveryEvent is a privacy-minimized, provider-neutral delivery projection
// retained from a verified provider webhook. Recipient addresses, subjects,
// click URLs, bounce messages, and raw payloads are deliberately excluded.
type DeliveryEvent struct {
	// Provider is the adapter name that produced the event ("resend", ...). It
	// scopes EventID so ids from different providers cannot collide.
	Provider string
	// EventID is the provider-unique, replay-stable id used to deduplicate a
	// redelivered callback (Resend: the Svix message id).
	EventID string
	// ProviderMessageID is the provider's identifier for the sent message,
	// stored for operational correlation.
	ProviderMessageID string
	// EventType is the provider's native event name, retained for audit. The
	// projection keys on Status, not on this.
	EventType string
	// Status is the canonical projected status, or the empty value when the
	// native event does not advance delivery state (opens, clicks, delays):
	// the event is still recorded, but no invitation projection runs.
	Status DeliveryStatus
	// InvitationID optionally links the event to an invitation whose
	// delivery_status is advanced monotonically.
	InvitationID string
	// OccurredAt is the provider's event timestamp (UTC).
	OccurredAt time.Time
}

// DeliveryEventRecorder durably deduplicates delivery events by
// (Provider, EventID) and projects invitation delivery state. The bool is true
// only for the first accepted delivery of an event. It is provider-agnostic: an
// adapter hands it already-canonical DeliveryEvents.
type DeliveryEventRecorder interface {
	RecordDeliveryEvent(ctx context.Context, event DeliveryEvent) (bool, error)
}
