// Package email provides a minimal transactional-email abstraction so
// the business layer can send invites, password resets, and billing
// notifications without being coupled to a specific provider.
//
// Implementations:
//   - pkg/email.NewResendSender(...) — production via https://resend.com
//   - pkg/email.NewFakeSender() — in-memory capture for tests
//   - pkg/email.NewLogSender(...) — writes to stdout for dev
//
// Business code depends only on the Sender interface. Swapping
// providers is one line in work.go.
package email

import (
	"context"
	"fmt"
)

// Message is the full payload for a single outgoing email.
type Message struct {
	From     string   // "Acme <noreply@acme.com>"
	To       []string // RFC 5321 addresses
	ReplyTo  string   // optional
	Subject  string
	HTMLBody string // rendered HTML (required)
	TextBody string // plaintext fallback, optional but recommended

	// Tags are arbitrary key/value labels for deliverability analytics.
	// Resend supports up to 10. Example: {"type": "invitation", "org": "acme"}.
	Tags map[string]string
}

// Validate returns an error describing the first missing required field.
func (m *Message) Validate() error {
	if m.From == "" {
		return fmt.Errorf("email: From is required")
	}
	if len(m.To) == 0 {
		return fmt.Errorf("email: at least one To address is required")
	}
	if m.Subject == "" {
		return fmt.Errorf("email: Subject is required")
	}
	if m.HTMLBody == "" && m.TextBody == "" {
		return fmt.Errorf("email: HTMLBody or TextBody is required")
	}
	return nil
}

// Sender is the transactional email boundary. Implementations are
// expected to be safe for concurrent use.
type Sender interface {
	// Send delivers a message. Returns the provider's message id on
	// success — stored in audit logs for delivery tracking.
	Send(ctx context.Context, msg *Message) (string, error)
}
