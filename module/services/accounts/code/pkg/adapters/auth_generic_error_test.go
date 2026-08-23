package adapters

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
)

// setExposeAuthErrorDetail flips the package-level verbose-error toggle for the
// duration of a test and restores it afterwards, so dev- and prod-mode cases
// don't leak state into each other.
func setExposeAuthErrorDetail(t *testing.T, v bool) {
	t.Helper()
	prev := exposeAuthErrorDetail
	t.Cleanup(func() { exposeAuthErrorDetail = prev })
	SetExposeAuthErrorDetail(v)
}

// TestAuthenticateStatusError_IndistinguishableIdentityFailures locks the M4
// contract: no-account, inactive, and not-invited must be identical to the
// client so an unauthenticated caller gets no enumeration/account-state oracle.
func TestAuthenticateStatusError_IndistinguishableIdentityFailures(t *testing.T) {
	setExposeAuthErrorDetail(t, false) // deployed-environment behaviour
	ctx := context.Background()

	// The three acceptance-criteria cases plus the invitation-mismatch variants
	// of "not invited" — every one wrapped the way the business layer wraps it.
	oracleInputs := map[string]error{
		"no account":            fmt.Errorf("identity resolution: %w", auth.ErrNoAccount),
		"inactive":              fmt.Errorf("identity resolution: %w", auth.ErrAccountInactive),
		"signup not allowed":    fmt.Errorf("identity resolution: %w", auth.ErrSignupNotAllowed),
		"invite email mismatch": fmt.Errorf("identity resolution: %w", business.ErrInvitationEmailMismatch),
		"invite expired":        fmt.Errorf("identity resolution: %w", business.ErrInvitationExpired),
		"unknown identity":      fmt.Errorf("header-jwt authentication: %w", auth.ErrUnknownIdentity),
		// SSO org-bound login where the asserted org exists but the identity is
		// not a member: an org-state oracle unless it collapses with the rest.
		"org membership denied": fmt.Errorf("identity resolution: %w", auth.ErrOrganizationAccessDenied),
	}

	var canonical *status.Status
	for name, in := range oracleInputs {
		st, ok := status.FromError(authenticateStatusError(ctx, in))
		require.True(t, ok, "%s: must be a gRPC status error", name)
		require.Equal(t, codes.Unauthenticated, st.Code(), "%s: code", name)
		require.Equal(t, "invalid credentials", st.Message(), "%s: message", name)

		if canonical == nil {
			canonical = st
			continue
		}
		require.Equal(t, canonical.Code(), st.Code(), "%s: code differs from the others", name)
		require.Equal(t, canonical.Message(), st.Message(), "%s: message differs from the others", name)
	}
}

func TestAuthenticateStatusError_GroupNotAllowedStaysDistinct(t *testing.T) {
	st, ok := status.FromError(authenticateStatusError(context.Background(),
		fmt.Errorf("identity resolution: %w", auth.ErrGroupNotAllowed)))
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Equal(t, "access not granted", st.Message())
}

// TestAuthenticateStatusError_JWKSUnavailableIsRetryableNotCredential proves an
// operator-side key-set outage maps to a retryable Unavailable with a generic
// message — never the verbatim sentinel and never miscast as invalid credentials.
func TestAuthenticateStatusError_JWKSUnavailableIsRetryableNotCredential(t *testing.T) {
	setExposeAuthErrorDetail(t, false)
	st, ok := status.FromError(authenticateStatusError(context.Background(),
		fmt.Errorf("header-jwt authentication: %w", auth.ErrJWKSUnavailable)))
	require.True(t, ok)
	require.Equal(t, codes.Unavailable, st.Code())
	require.Equal(t, "authentication temporarily unavailable", st.Message())
	require.NotContains(t, st.Message(), "jwks")
}

// TestAuthenticateStatusError_LocalDevelopmentRevealsDetail proves that, in a
// local development environment, the status code is unchanged (so clients behave
// identically) but the message carries the underlying reason for debugging.
func TestAuthenticateStatusError_LocalDevelopmentRevealsDetail(t *testing.T) {
	setExposeAuthErrorDetail(t, true)

	noAccount := fmt.Errorf("identity resolution: %w", auth.ErrNoAccount)
	inactive := fmt.Errorf("identity resolution: %w", auth.ErrAccountInactive)

	stNoAccount, ok := status.FromError(authenticateStatusError(context.Background(), noAccount))
	require.True(t, ok)
	stInactive, _ := status.FromError(authenticateStatusError(context.Background(), inactive))

	// Code is stable across environments; only the message differs.
	require.Equal(t, codes.Unauthenticated, stNoAccount.Code())
	require.Equal(t, codes.Unauthenticated, stInactive.Code())
	require.Equal(t, noAccount.Error(), stNoAccount.Message())
	require.NotEqual(t, stNoAccount.Message(), stInactive.Message(), "detail is exposed in dev")
}

func TestAuthenticateStatusError_NilPassesThrough(t *testing.T) {
	require.NoError(t, authenticateStatusError(context.Background(), nil))
}

// TestAuthenticateStatusError_InternalErrorPassesThrough proves the collapse is
// scoped to credential/identity sentinels: a genuine server-side failure is not
// masked as an authentication failure.
func TestAuthenticateStatusError_InternalErrorPassesThrough(t *testing.T) {
	internal := fmt.Errorf("mint tokens: signer unavailable")
	require.Equal(t, internal, authenticateStatusError(context.Background(), internal))
}
