package abuse_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"accounts/pkg/abuse"

	"github.com/stretchr/testify/require"
)

func TestTurnstileVerifierPassFailAndReplayFixture(t *testing.T) {
	var used atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "secret_test", r.Form.Get("secret"))
		switch r.Form.Get("response") {
		case "pass":
			_, _ = w.Write([]byte(`{"success":true,"action":"waitlist_join","hostname":"app.example.com"}`))
		case "replay":
			if used.Swap(true) {
				_, _ = w.Write([]byte(`{"success":false,"error-codes":["timeout-or-duplicate"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"action":"waitlist_join","hostname":"app.example.com"}`))
		default:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		}
	}))
	defer server.Close()

	verifier, err := abuse.NewTurnstileVerifier(abuse.TurnstileConfig{
		SecretKey:        "secret_test",
		VerifyURL:        server.URL,
		AllowedHostnames: []string{"app.example.com"},
	})
	require.NoError(t, err)
	require.NoError(t, verifier.Verify(t.Context(), abuse.Challenge{
		Token: "pass", Action: "waitlist_join",
	}))
	require.ErrorIs(t, verifier.Verify(t.Context(), abuse.Challenge{
		Token: "fail", Action: "waitlist_join",
	}), abuse.ErrChallengeRejected)
	require.NoError(t, verifier.Verify(t.Context(), abuse.Challenge{
		Token: "replay", Action: "waitlist_join",
	}))
	require.ErrorIs(t, verifier.Verify(t.Context(), abuse.Challenge{
		Token: "replay", Action: "waitlist_join",
	}), abuse.ErrChallengeRejected)
}

func TestTurnstileVerifierRejectsActionAndHostnameMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"action":"login","hostname":"evil.example"}`))
	}))
	defer server.Close()
	verifier, err := abuse.NewTurnstileVerifier(abuse.TurnstileConfig{
		SecretKey: "secret", VerifyURL: server.URL,
		AllowedHostnames: []string{"app.example.com"},
	})
	require.NoError(t, err)
	err = verifier.Verify(t.Context(), abuse.Challenge{Token: "token", Action: "waitlist_join"})
	require.True(t, errors.Is(err, abuse.ErrChallengeRejected))
}
