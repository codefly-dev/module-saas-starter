package auth_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

func TestOAuthRequestPolicyExactMatch(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", []string{
		"https://app.example.com/auth/callback",
		"http://localhost:3000/auth/callback",
	})
	require.NoError(t, err)
	require.NoError(t, policy.Validate("workos", "https://app.example.com/auth/callback"))
	require.NoError(t, policy.Validate("workos", "http://localhost:3000/auth/callback"))

	for _, tc := range []struct {
		provider string
		redirect string
	}{
		{"auth0", "https://app.example.com/auth/callback"},
		{"workos", "https://app.example.com.evil.test/auth/callback"},
		{"workos", "https://app.example.com/auth/callback/extra"},
		{"workos", "https://app.example.com:444/auth/callback"},
	} {
		err := policy.Validate(tc.provider, tc.redirect)
		require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)
	}
}

func TestOAuthRequestPolicyRejectsUnsafeConfiguration(t *testing.T) {
	for _, redirect := range []string{
		"/auth/callback",
		"http://app.example.com/auth/callback",
		"javascript:alert(1)",
		"https://user@app.example.com/auth/callback",
		"https://app.example.com/auth/callback#fragment",
	} {
		_, err := auth.NewOAuthRequestPolicy("workos", []string{redirect})
		require.Error(t, err, redirect)
	}
}

func TestOAuthRequestPolicyEmptyFallbackFailsClosed(t *testing.T) {
	for _, redirects := range [][]string{nil, {""}, {"REPLACE_ME"}} {
		policy, err := auth.NewOAuthRequestPolicy("workos", redirects)
		require.NoError(t, err)
		require.ErrorIs(t, policy.Validate("workos", "https://app.example.com/auth/callback"), auth.ErrInvalidOAuthRequest)
	}
}

func TestOAuthRequestPolicyUsesGenericRuntimeError(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", []string{"https://app.example.com/auth/callback"})
	require.NoError(t, err)
	err = policy.Validate("workos", "https://evil.example/auth/callback")
	require.True(t, errors.Is(err, auth.ErrInvalidOAuthRequest))
	require.Equal(t, auth.ErrInvalidOAuthRequest, err)
}

func TestOAuthRequestPolicyBindsCallbackToVerifiedCodeflyOrigin(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", nil)
	require.NoError(t, err)
	require.ErrorIs(t, policy.Validate("workos", "http://localhost:54321/auth/callback"), auth.ErrInvalidOAuthRequest)
	require.NoError(t, policy.ValidateForPublicOrigin(
		"workos",
		"http://localhost:54321/auth/callback",
		"http://localhost:54321",
	))

	for _, redirect := range []string{
		"http://localhost:54322/auth/callback",
		"http://localhost:54321/auth/callback/extra",
		"https://evil.example/auth/callback",
	} {
		require.ErrorIs(t, policy.ValidateForPublicOrigin(
			"workos",
			redirect,
			"http://localhost:54321",
		), auth.ErrInvalidOAuthRequest)
	}
}
