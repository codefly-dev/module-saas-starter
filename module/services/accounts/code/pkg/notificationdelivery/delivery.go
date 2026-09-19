// Package notificationdelivery contains provider transports. The host resolves
// an authorized recipient and evaluates preferences before calling a transport;
// Runtime owns execution, retries and recovery. No transport schedules work.
package notificationdelivery

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var ErrNotConfigured = errors.New("notification delivery provider is not configured")
var ErrInvalidMessage = errors.New("invalid notification delivery message")

// Failure deliberately excludes provider response bodies and credential-bearing
// URLs. An ambiguous result must be recovered, never blindly retried: a timeout
// can happen after the provider accepted the message.
type Failure struct {
	Code       string
	RetryAfter time.Duration
	Ambiguous  bool
}

func (e *Failure) Error() string { return "notification delivery: " + e.Code }

// Message contains only presentation text and an opaque effect identifier.
// Destination is resolved by the host, never accepted from a solution as proof
// that a Slack conversation or phone number belongs to the recipient.
type Message struct {
	EffectID    string
	Destination string
	Title       string
	Body        string
}

type Result struct{ ProviderMessageID string }

// Sender is a single-attempt provider boundary, not a delivery promise. A future
// SMS provider implements the same boundary after verified-number enrollment,
// consent, policy and runtime recovery have been wired by the host.
type Sender interface {
	Send(context.Context, Message) (Result, error)
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)

// ValidateSMS validates wire shape only; it does not establish ownership,
// deliverability, verification, consent, or eligibility for a mandatory notice.
func ValidateSMS(message Message) error {
	if message.EffectID == "" || !e164.MatchString(message.Destination) || message.Body == "" {
		return ErrInvalidMessage
	}
	return nil
}

// UnconfiguredSMS fails closed until a real provider is installed. It must not
// acknowledge a message, charge a send, or turn on a user-facing SMS toggle.
type UnconfiguredSMS struct{}

func (UnconfiguredSMS) Send(context.Context, Message) (Result, error) {
	return Result{}, ErrNotConfigured
}
