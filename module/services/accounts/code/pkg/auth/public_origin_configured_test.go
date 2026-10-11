package auth_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

// The gateway credential alone was not enough, and that is the whole of what this
// closes. The frontend held the internal token legitimately and derived the origin
// it stamped from the caller's own X-Forwarded-Host whenever its rendered endpoint
// was a loopback placeholder — which is what a render produces. So an authenticated
// hop forwarded a caller's choice and it was recorded as VERIFIED, which bound an
// OAuth redirect, the authenticator relying-party origin and every emailed link to
// a host the caller named.
func TestForwardedOriginIsRefusedUnlessItIsTheConfiguredOne(t *testing.T) {
	pin(t, "https://app.cell.example")

	_, err := auth.WithVerifiedPublicOrigin(context.Background(), "https://evil.example")
	require.ErrorIs(t, err, auth.ErrPublicOriginNotConfigured)

	ctx, err := auth.WithVerifiedPublicOrigin(context.Background(), "https://app.cell.example")
	require.NoError(t, err)
	origin, ok := auth.VerifiedPublicOrigin(ctx)
	require.True(t, ok)
	require.Equal(t, "https://app.cell.example", origin)
}

// A trailing slash and a differently-cased host are the same origin, so a
// deployment is not refused for spelling its own value two ways.
func TestConfiguredOriginComparisonIsOnTheOrigin(t *testing.T) {
	pin(t, "https://app.cell.example/")

	for _, candidate := range []string{
		"https://app.cell.example",
		"https://app.cell.example/",
		"https://APP.CELL.example",
	} {
		_, err := auth.WithVerifiedPublicOrigin(context.Background(), candidate)
		require.NoError(t, err, candidate)
	}
}

// A near-miss is a miss: a subdomain, a sibling host, a different scheme and a
// different port are each a different origin.
func TestConfiguredOriginRefusesNearMisses(t *testing.T) {
	pin(t, "https://app.cell.example")

	for _, candidate := range []string{
		"https://app.cell.example.evil.example",
		"https://evil.app.cell.example",
		"https://app.cell.example:8443",
		"http://localhost:3000",
	} {
		_, err := auth.WithVerifiedPublicOrigin(context.Background(), candidate)
		require.ErrorIs(t, err, auth.ErrPublicOriginNotConfigured, candidate)
	}
}

// With nothing pinned the candidate stands on the credential alone, which only a
// local runtime reaches: boot requires a pinned origin outside local development.
// Asserted so the comparison above is the pin and not a blanket refusal.
func TestUnpinnedDeploymentStillAcceptsAWellFormedForwardedOrigin(t *testing.T) {
	pin(t, "")

	ctx, err := auth.WithVerifiedPublicOrigin(context.Background(), "http://localhost:3000")
	require.NoError(t, err)
	origin, ok := auth.VerifiedPublicOrigin(ctx)
	require.True(t, ok)
	require.Equal(t, "http://localhost:3000", origin)
}

// Well-formedness is still checked first, so a pinned deployment does not accept a
// malformed value by matching it against anything.
func TestAMalformedOriginIsRefusedWhateverIsPinned(t *testing.T) {
	pin(t, "https://app.cell.example")

	for _, candidate := range []string{
		"https://app.cell.example/path",
		"https://user:secret@app.cell.example",
		"app.cell.example",
		"",
	} {
		_, err := auth.WithVerifiedPublicOrigin(context.Background(), candidate)
		require.Error(t, err, candidate)
	}
}

func pin(t *testing.T, origin string) {
	t.Helper()
	previous, _ := auth.ConfiguredPublicOrigin()
	t.Cleanup(func() { auth.SetConfiguredPublicOrigin(previous) })
	auth.SetConfiguredPublicOrigin(origin)
}

// An explicit DEFAULT port is the same origin as none, and the two sides spell it
// differently: a browser and the frontend both drop it, while a configured value may
// carry it. Retaining it made an operator's `https://app.example:443` refuse the
// equivalent origin the frontend forwards.
func TestR1019ConfiguredOriginDefaultPorts(t *testing.T) {
	for _, tc := range []struct{ configured, forwarded string }{
		{"https://app.cell.example:443", "https://app.cell.example"},
		{"https://app.cell.example", "https://app.cell.example:443"},
		{"http://localhost:80", "http://localhost"},
		{"http://localhost", "http://localhost:80"},
	} {
		t.Run(tc.configured+" vs "+tc.forwarded, func(t *testing.T) {
			pin(t, tc.configured)
			_, err := auth.WithVerifiedPublicOrigin(context.Background(), tc.forwarded)
			require.NoError(t, err, "equivalent origins must not disagree over a default port")
		})
	}
}

// A NON-default port is part of the origin and still distinguishes it, so the
// canonicalization above does not collapse genuinely different origins.
func TestR1019NonDefaultPortStillDistinguishesAnOrigin(t *testing.T) {
	pin(t, "https://app.cell.example")
	_, err := auth.WithVerifiedPublicOrigin(context.Background(), "https://app.cell.example:8443")
	require.ErrorIs(t, err, auth.ErrPublicOriginNotConfigured)

	pin(t, "https://app.cell.example:8443")
	_, err = auth.WithVerifiedPublicOrigin(context.Background(), "https://app.cell.example")
	require.ErrorIs(t, err, auth.ErrPublicOriginNotConfigured)
}

// CanonicalPublicOrigin returns the canonical spelling, so a caller that stores the
// result stores one form.
func TestR1019CanonicalOriginDropsOnlyTheDefaultPort(t *testing.T) {
	for candidate, want := range map[string]string{
		"https://app.example:443":  "https://app.example",
		"https://app.example":      "https://app.example",
		"https://app.example:8443": "https://app.example:8443",
		"http://127.0.0.1:80":      "http://127.0.0.1",
		"http://127.0.0.1:3000":    "http://127.0.0.1:3000",
		"https://app.example/":     "https://app.example",
	} {
		got, err := auth.CanonicalPublicOrigin(candidate)
		require.NoError(t, err, candidate)
		require.Equal(t, want, got, candidate)
	}
}
