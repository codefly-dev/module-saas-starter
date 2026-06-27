package email_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/email"
)

// ============================================================================
// Message validation
// ============================================================================

func TestMessage_Validate(t *testing.T) {
	cases := []struct {
		name string
		msg  email.Message
		ok   bool
	}{
		{
			name: "happy",
			msg: email.Message{
				From:     "noreply@acme.com",
				To:       []string{"user@acme.com"},
				Subject:  "hi",
				HTMLBody: "<p>hi</p>",
			},
			ok: true,
		},
		{name: "missing from", msg: email.Message{To: []string{"u@a.com"}, Subject: "s", HTMLBody: "h"}},
		{name: "missing to", msg: email.Message{From: "f@a.com", Subject: "s", HTMLBody: "h"}},
		{name: "missing subject", msg: email.Message{From: "f@a.com", To: []string{"u@a.com"}, HTMLBody: "h"}},
		{name: "missing body", msg: email.Message{From: "f@a.com", To: []string{"u@a.com"}, Subject: "s"}},
		{
			name: "text-only body ok",
			msg: email.Message{
				From: "f@a.com", To: []string{"u@a.com"}, Subject: "s", TextBody: "plain",
			},
			ok: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.msg.Validate()
			if c.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// ============================================================================
// FakeSender
// ============================================================================

func TestFakeSender_CapturesMessages(t *testing.T) {
	f := email.NewFakeSender()
	ctx := context.Background()

	id1, err := f.Send(ctx, &email.Message{
		From: "from@a.com", To: []string{"a@b.com"}, Subject: "one", HTMLBody: "1",
	})
	require.NoError(t, err)
	require.Equal(t, "fake-1", id1)

	id2, err := f.Send(ctx, &email.Message{
		From: "from@a.com", To: []string{"c@d.com", "e@f.com"}, Subject: "two", HTMLBody: "2",
	})
	require.NoError(t, err)
	require.Equal(t, "fake-2", id2)

	require.Equal(t, 2, f.Count())
	require.Equal(t, []string{"a@b.com", "c@d.com", "e@f.com"}, f.ToAddresses())
}

func TestFakeSender_FailWith(t *testing.T) {
	f := email.NewFakeSender()
	f.FailWith = errors.New("network down")

	_, err := f.Send(context.Background(), &email.Message{
		From: "f@a.com", To: []string{"u@a.com"}, Subject: "s", HTMLBody: "h",
	})
	require.Error(t, err)
	require.Equal(t, 0, f.Count())
}

func TestFakeSender_RejectsInvalid(t *testing.T) {
	f := email.NewFakeSender()
	_, err := f.Send(context.Background(), &email.Message{})
	require.Error(t, err)
}

func TestFakeSender_Reset(t *testing.T) {
	f := email.NewFakeSender()
	_, _ = f.Send(context.Background(), &email.Message{
		From: "f@a.com", To: []string{"u@a.com"}, Subject: "s", HTMLBody: "h",
	})
	f.Reset()
	require.Equal(t, 0, f.Count())
}

// ============================================================================
// LogSender
// ============================================================================

func TestLogSender_Writes(t *testing.T) {
	var captured string
	l := email.NewLogSender(func(format string, args ...any) {
		captured = format
	})
	id, err := l.Send(context.Background(), &email.Message{
		From: "f@a.com", To: []string{"u@a.com"}, Subject: "hello", HTMLBody: "<p>hi</p>",
	})
	require.NoError(t, err)
	require.Equal(t, "log-dev", id)
	require.Contains(t, captured, "EMAIL")
}

// ============================================================================
// ResendSender
// ============================================================================

func TestResendSender_Happy(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/emails", r.URL.Path)
		require.Equal(t, "Bearer re_test", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &received))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_01ABC"}`))
	}))
	defer server.Close()

	s, err := email.NewResendSender(email.ResendConfig{
		APIKey:  "re_test",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	id, err := s.Send(context.Background(), &email.Message{
		From:     "Acme <noreply@acme.com>",
		To:       []string{"user@example.com"},
		ReplyTo:  "support@acme.com",
		Subject:  "Welcome",
		HTMLBody: "<p>welcome</p>",
		TextBody: "welcome",
		Tags:     map[string]string{"type": "welcome"},
	})
	require.NoError(t, err)
	require.Equal(t, "msg_01ABC", id)

	require.Equal(t, "Acme <noreply@acme.com>", received["from"])
	tos, _ := received["to"].([]any)
	require.Len(t, tos, 1)
	require.Equal(t, "user@example.com", tos[0])
	require.Equal(t, "Welcome", received["subject"])
	require.Equal(t, "<p>welcome</p>", received["html"])
	require.Equal(t, "welcome", received["text"])
	require.Equal(t, "support@acme.com", received["reply_to"])
}

func TestResendSender_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"invalid to address"}`))
	}))
	defer server.Close()

	s, _ := email.NewResendSender(email.ResendConfig{APIKey: "re_test", BaseURL: server.URL})
	_, err := s.Send(context.Background(), &email.Message{
		From: "f@a.com", To: []string{"u@a.com"}, Subject: "s", HTMLBody: "h",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "422")
	require.Contains(t, err.Error(), "invalid to address")
}

func TestResendSender_RequiresAPIKey(t *testing.T) {
	_, err := email.NewResendSender(email.ResendConfig{})
	require.Error(t, err)
}

func TestResendSender_RejectsInvalidMessage(t *testing.T) {
	s, _ := email.NewResendSender(email.ResendConfig{APIKey: "re_test"})
	_, err := s.Send(context.Background(), &email.Message{})
	require.Error(t, err)
}

// ============================================================================
// Interface conformance
// ============================================================================

func TestSenderInterfaceConformance(t *testing.T) {
	// Compile-time: all three implementations satisfy email.Sender.
	var _ email.Sender = (*email.FakeSender)(nil)
	var _ email.Sender = (*email.LogSender)(nil)
	var _ email.Sender = (*email.ResendSender)(nil)
	require.True(t, strings.HasPrefix("fake-", "fake-"))
}
