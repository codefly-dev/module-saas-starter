package adapters

import (
	"context"
	"strconv"
	"testing"
	"time"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

func TestAssuranceTransportProjectionAndContextStamp(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	projected := assuranceFromTransport(
		"oauth, otp",
		unixString(now.Add(-2*time.Minute)),
		auth.AssuranceLevelAAL2,
		unixString(now.Add(-time.Minute)),
	)
	ctx := stampVerifiedIdentity(context.Background(), "user-id", "org-id", projected)
	got := auth.AssuranceFromContext(ctx)
	require.Equal(t, []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodOTP}, got.AuthenticationMethods)
	require.Equal(t, now.Add(-2*time.Minute), got.AuthenticatedAt)
	require.Equal(t, auth.AssuranceLevelAAL2, got.Level)
	require.Equal(t, now.Add(-time.Minute), got.MFAVerifiedAt)
	require.True(t, got.HasRecentMFA(now, auth.DefaultRecentStepUpMaxAge))
}

func TestAssuranceTransportRejectsMalformedTimestamps(t *testing.T) {
	got := assuranceFromTransport("otp", "not-a-time", auth.AssuranceLevelAAL2, "-1")
	require.True(t, got.AuthenticatedAt.IsZero())
	require.True(t, got.MFAVerifiedAt.IsZero())
	require.False(t, got.HasRecentMFA(time.Now(), auth.DefaultRecentStepUpMaxAge))
}

func unixString(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}
