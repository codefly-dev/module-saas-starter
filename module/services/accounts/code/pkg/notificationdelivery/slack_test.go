package notificationdelivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func message() Message {
	return Message{EffectID: "effect-1", Destination: "C12345", Title: "Source updated", Body: "<!channel> <https://example.com|link> & text"}
}

func TestSlackPlainTextAndFixedEndpoint(t *testing.T) {
	client := NewSlack("test-secret")
	client.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https://slack.com/api/chat.postMessage", r.URL.String())
		require.Equal(t, "Bearer test-secret", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "C12345", payload["channel"])
		require.Equal(t, "none", payload["parse"])
		require.Equal(t, false, payload["mrkdwn"])
		require.Equal(t, false, payload["unfurl_links"])
		require.Equal(t, false, payload["unfurl_media"])
		require.Contains(t, payload["text"], "&lt;!channel&gt;")
		blocks := payload["blocks"].([]any)
		for _, b := range blocks {
			require.Equal(t, "plain_text", b.(map[string]any)["text"].(map[string]any)["type"])
		}
		return response(200, `{"ok":true,"ts":"123.456"}`), nil
	})
	result, err := client.Send(context.Background(), message())
	require.NoError(t, err)
	require.Equal(t, "123.456", result.ProviderMessageID)
}

func TestSlackFailureClassificationAndNoBlindRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		ambiguous  bool
	}{
		{"rate limit", 429, "token-secret", "rate_limited", false},
		{"revoked", 200, `{"ok":false,"error":"token_revoked"}`, "installation_revoked", false},
		{"arbitrary provider error", 200, `{"ok":false,"error":"secret-token"}`, "provider_rejected", true},
		{"server failure", 503, "secret-token", "http_error", true},
		{"broken json", 200, "secret-token", "invalid_response", true},
		{"missing receipt", 200, `{"ok":true}`, "missing_message_id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewSlack("secret-token")
			client.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				r := response(tc.status, tc.body)
				r.Header.Set("Retry-After", "42")
				return r, nil
			})
			_, err := client.Send(context.Background(), message())
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.code, failure.Code)
			require.Equal(t, tc.ambiguous, failure.Ambiguous)
			require.NotContains(t, err.Error(), "secret-token")
			require.Equal(t, 1, calls)
			if tc.status == 429 {
				require.Equal(t, 42*time.Second, failure.RetryAfter)
			}
		})
	}
	client := NewSlack("secret-token")
	client.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("credential in transport URL") })
	_, err := client.Send(context.Background(), message())
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.Ambiguous)
	require.NotContains(t, err.Error(), "credential")
}

func TestSlackRefusesRedirectsAndInvalidDestinations(t *testing.T) {
	client := NewSlack("test-token")
	calls := 0
	client.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(302, "")
		r.Header.Set("Location", "https://example.com/steal")
		return r, nil
	})
	_, err := client.Send(context.Background(), message())
	require.Error(t, err)
	require.Equal(t, 1, calls)
	for _, destination := range []string{"", "@everyone", "https://example.com", "C123\n"} {
		m := message()
		m.Destination = destination
		_, err := client.Send(context.Background(), m)
		require.ErrorIs(t, err, ErrInvalidMessage)
	}
	require.Equal(t, 1, calls)
	_, err = NewSlack("").Send(context.Background(), message())
	require.ErrorIs(t, err, ErrNotConfigured)
}

func TestSlackSignatureBindsRawBytesAndRejectsReplay(t *testing.T) {
	body := []byte(`{"team_id":"T123","event_id":"event-1"}`)
	timestamp := "1700000000"
	mac := hmac.New(sha256.New, []byte("signing-secret"))
	_, _ = mac.Write(append([]byte("v0:"+timestamp+":"), body...))
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))
	now := time.Unix(1700000000, 0)
	require.True(t, VerifySlackSignature("signing-secret", timestamp, signature, body, now))
	for _, offset := range []time.Duration{-301 * time.Second, 301 * time.Second} {
		require.False(t, VerifySlackSignature("signing-secret", timestamp, signature, body, now.Add(offset)))
	}
	require.False(t, VerifySlackSignature("wrong-secret", timestamp, signature, body, now))
	require.False(t, VerifySlackSignature("signing-secret", timestamp, signature, append(body, ' '), now))
	require.False(t, VerifySlackSignature("", timestamp, signature, body, now))
	require.False(t, VerifySlackSignature("signing-secret", "9223372036854775807", signature, body, now))
}

func TestSMSPreparedButNeverAcknowledgedWithoutProvider(t *testing.T) {
	for _, phone := range []string{"+12025550123", "+442079460123"} {
		require.NoError(t, ValidateSMS(Message{EffectID: "effect-1", Destination: phone, Body: "Source updated"}))
	}
	for _, phone := range []string{"2025550123", "+012345", "+1234567890123456", "+1 202 555 0123"} {
		require.ErrorIs(t, ValidateSMS(Message{EffectID: "effect-1", Destination: phone, Body: "Source updated"}), ErrInvalidMessage)
	}
	result, err := (UnconfiguredSMS{}).Send(context.Background(), Message{EffectID: "effect-1", Destination: "+12025550123", Body: "Source updated"})
	require.ErrorIs(t, err, ErrNotConfigured)
	require.Empty(t, result.ProviderMessageID)
}
