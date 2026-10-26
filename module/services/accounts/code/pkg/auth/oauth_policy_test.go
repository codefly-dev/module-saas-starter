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

// The refusal is the one generic sentinel a public handler maps its answer on, so
// a caller cannot tell which part of the allowlist was wrong. Its wrapped text is
// for the refuser's log (TestOAuthRequestPolicyRefusalNamesTheFailedCheck), and
// no handler returns it: BeginOAuth answers "provider or redirect_uri not
// allowed" on errors.Is, and Authenticate collapses it to "invalid credentials".
func TestOAuthRequestPolicyUsesGenericRuntimeError(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", []string{"https://app.example.com/auth/callback"})
	require.NoError(t, err)
	err = policy.Validate("workos", "https://evil.example/auth/callback")
	require.True(t, errors.Is(err, auth.ErrInvalidOAuthRequest))
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

// Every refusal is still the one sentinel a public handler maps on, and its text
// now says which check failed with the values it compared, so the refuser's log
// can tell a provider mismatch from a redirect mismatch. Before, all four cases
// below logged the same "auth: invalid oauth request" and nothing else.
func TestOAuthRequestPolicyRefusalNamesTheFailedCheck(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", []string{"https://app.example.com/auth/callback"})
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "provider mismatch",
			err:  policy.Validate("auth0", "https://app.example.com/auth/callback"),
			want: []string{`provider "auth0" is not the configured provider "workos"`},
		},
		{
			name: "static allowlist miss",
			err:  policy.Validate("workos", "https://other.example.com/auth/callback"),
			want: []string{`redirect_uri "https://other.example.com/auth/callback" is not in the static redirect allowlist`, "1 entries"},
		},
		{
			name: "redirect is not the verified origin's callback",
			err:  policy.ValidateForPublicOrigin("workos", "https://other.example.com/auth/callback", "https://app.example.com"),
			want: []string{`redirect_uri "https://other.example.com/auth/callback" is not the verified public origin's callback "https://app.example.com/auth/callback"`},
		},
		{
			name: "verified origin is not canonical",
			err:  policy.ValidateForPublicOrigin("workos", "http://frontend.ns.svc.cluster.local:8080/auth/callback", "http://frontend.ns.svc.cluster.local:8080"),
			want: []string{`verified public origin "http://frontend.ns.svc.cluster.local:8080" is not canonical`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.err, auth.ErrInvalidOAuthRequest)
			for _, want := range tc.want {
				require.Contains(t, tc.err.Error(), want)
			}
		})
	}
}
