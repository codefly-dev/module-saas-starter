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

// LogSender writes every outgoing message to a writer (typically
// os.Stdout) instead of sending it. Used in dev when no Resend API
// key is configured — you still see what WOULD be sent.
type LogSender struct {
	Logger func(format string, args ...any)
}

// NewLogSender returns a LogSender using the given log function.
// Pass log.Printf for stdlib-style output.
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
