package main

import (
	"testing"

	"accounts/pkg/email"

	"github.com/stretchr/testify/require"
)

func TestConfiguredEmailSenderDefaultsToLog(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "")
	sender, err := configuredEmailSender(t.Context())
	require.NoError(t, err)
	require.IsType(t, &email.LogSender{}, sender)
}

func TestConfiguredEmailSenderFailsClosed(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "log")
	t.Setenv("RESEND_API_KEY", "re_accidental")
	_, err := configuredEmailSender(t.Context())
	require.ErrorContains(t, err, "credentials are present")

	t.Setenv("EMAIL_PROVIDER", "resend")
	t.Setenv("RESEND_API_KEY", "")
	t.Setenv("RESEND_WEBHOOK_SECRET", "")
	_, err = configuredEmailSender(t.Context())
	require.ErrorContains(t, err, "RESEND_API_KEY")

	t.Setenv("RESEND_API_KEY", "re_test")
	t.Setenv("RESEND_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("RESEND_API_BASE", "http://localhost:9999")
	sender, err := configuredEmailSender(t.Context())
	require.NoError(t, err)
	require.IsType(t, &email.ResendSender{}, sender)

	t.Setenv("EMAIL_PROVIDER", "typo")
	_, err = configuredEmailSender(t.Context())
	require.ErrorContains(t, err, "log or resend")
}

func TestConfiguredAbuseVerifierFailsClosed(t *testing.T) {
	t.Setenv("ABUSE_PROTECTION_MODE", "disabled")
	t.Setenv("TURNSTILE_SECRET_KEY", "accidental")
	_, err := configuredAbuseVerifier()
	require.ErrorContains(t, err, "while ABUSE_PROTECTION_MODE is disabled")

	t.Setenv("ABUSE_PROTECTION_MODE", "turnstile")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	_, err = configuredAbuseVerifier()
	require.ErrorContains(t, err, "secret key")

	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	t.Setenv("TURNSTILE_ALLOWED_HOSTNAMES", "localhost,app.example.com")
	t.Setenv("TURNSTILE_VERIFY_URL", "http://localhost:9999/siteverify")
	verifier, err := configuredAbuseVerifier()
	require.NoError(t, err)
	require.NotNil(t, verifier)
}
