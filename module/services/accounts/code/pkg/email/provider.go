package email

// Capabilities describes what an email provider supports so service wiring can
// branch on features instead of hardcoding provider names. It is a plain value:
// adapters construct it, the service reads it, nothing mutates it at runtime.
type Capabilities struct {
	// IdempotencyKeys is true when the provider deduplicates a retried send by
	// Message.IdempotencyKey. When false the durable outbox remains
	// at-least-once: a crash between a successful send and the job ack can
	// deliver the same message twice. The DB ON CONFLICT dedup collapses
	// duplicate *jobs*, not duplicate *sends*.
	IdempotencyKeys bool
	// DeliveryWebhooks is true when the provider emits delivery/bounce/complaint
	// callbacks. When false, delivery status never advances past enqueue and no
	// webhook route is wired.
	DeliveryWebhooks bool
	// BatchSend is true when the provider accepts multiple messages per request.
	BatchSend bool
	// ProviderTemplating is true when the provider renders templates itself.
	// This module renders then persists, so shipped adapters report false.
	ProviderTemplating bool
	// VerifiedSenderRequired is true when the From domain or mailbox must be
	// provisioned with the provider before it will accept a send.
	VerifiedSenderRequired bool
	// Tagging is true when the provider stores key/value labels for analytics.
	Tagging bool
}

// Provider is a Sender that also declares its identity and capabilities. The
// job worker depends only on Sender; service wiring depends on Provider so it
// can register provider-specific side channels such as delivery webhooks.
type Provider interface {
	Sender
	Name() string
	Capabilities() Capabilities
}
