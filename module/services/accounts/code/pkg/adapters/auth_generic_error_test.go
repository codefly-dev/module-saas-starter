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

// TestAuthenticateStatusError_IndistinguishableIdentityFailures locks the M4
// contract: no-account, inactive, and not-invited must be identical to the
// client so an unauthenticated caller gets no enumeration/account-state oracle.
func TestAuthenticateStatusError_IndistinguishableIdentityFailures(t *testing.T) {
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
