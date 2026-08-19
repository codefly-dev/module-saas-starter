package email

import (
	"context"
	"fmt"
	"sync"
)

// FakeSender is an in-memory implementation of Sender for tests and
// local dev. Every Send call is captured in Sent for later inspection.
//
// Safe for concurrent use.
type FakeSender struct {
	mu   sync.Mutex
	Sent []Message
	// FailWith, if non-nil, causes Send to return this error immediately
	// (before recording the message). Used to exercise error paths.
	FailWith error
	// Caps is returned by Capabilities(). The zero value supports nothing; set
	// it to drive wiring that branches on provider features.
	Caps Capabilities
}

// NewFakeSender returns an empty FakeSender.
func NewFakeSender() *FakeSender { return &FakeSender{} }

// Send records the message and returns a synthetic id.
func (f *FakeSender) Send(_ context.Context, m *Message) (string, error) {
	if f.FailWith != nil {
		return "", f.FailWith
	}
	if err := m.Validate(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, *m)
	return fmt.Sprintf("fake-%d", len(f.Sent)), nil
}

// Count returns the number of messages captured.
func (f *FakeSender) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Sent)
}

// ToAddresses returns a flat list of every recipient across all sent
// messages, preserving order. Useful for "expected N emails to Y"
// assertions in tests.
func (f *FakeSender) ToAddresses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.Sent {
		out = append(out, m.To...)
	}
	return out
}

// Reset clears the captured messages. Use in test cleanup.
func (f *FakeSender) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = nil
}

func (f *FakeSender) Name() string { return "fake" }

func (f *FakeSender) Capabilities() Capabilities { return f.Caps }

// LogSender writes every outgoing message to a writer (typically
// os.Stdout) instead of sending it. Used in dev when no Resend API
// key is configured — you still see what WOULD be sent.
type LogSender struct {
	Logger func(format string, args ...any)
}

// NewLogSender returns a LogSender using the given log function.
// Project policy is wool everywhere; the canonical caller wraps a
// wool.Get(ctx) Info call so dev-mode email previews land in the
// same structured stream as the rest of the service's logs (see
// pickEmailSender in work.go).
func NewLogSender(logger func(format string, args ...any)) *LogSender {
	return &LogSender{Logger: logger}
}

// Send prints the message and returns a synthetic id.
func (l *LogSender) Send(_ context.Context, m *Message) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	l.Logger("\n=== EMAIL (not sent — dev mode) ===\n"+
		"From: %s\nTo: %s\nSubject: %s\n%s\n==================================\n",
		m.From, m.To, m.Subject, firstNonEmpty(m.TextBody, m.HTMLBody))
	return "log-dev", nil
}

func (l *LogSender) Name() string { return "log" }

// Capabilities reports nothing: the dev log sink neither dedups, emits
// webhooks, nor requires a verified sender.
func (l *LogSender) Capabilities() Capabilities { return Capabilities{} }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
