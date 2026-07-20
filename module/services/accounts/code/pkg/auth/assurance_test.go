package auth_test

import (
	"testing"
	"time"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

func TestAssuranceHasRecentMFA(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		assurance auth.Assurance
		want      bool
	}{
		{
			name: "recent aal2",
			assurance: auth.Assurance{
				AuthenticationMethods: []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodOTP},
				Level:                 auth.AssuranceLevelAAL2,
				MFAVerifiedAt:         now.Add(-5 * time.Minute),
			},
			want: true,
		},
		{
			name: "stale aal2",
			assurance: auth.Assurance{
				AuthenticationMethods: []string{auth.AuthenticationMethodOTP},
				Level:                 auth.AssuranceLevelAAL2,
				MFAVerifiedAt:         now.Add(-auth.DefaultRecentStepUpMaxAge - time.Second),
			},
		},
		{
			name: "aal1 cannot become mfa by timestamp alone",
			assurance: auth.Assurance{
				AuthenticationMethods: []string{auth.AuthenticationMethodOTP},
				Level:                 auth.AssuranceLevelAAL1,
				MFAVerifiedAt:         now,
			},
		},
		{
			name: "missing explicit mfa time",
			assurance: auth.Assurance{
				Level: auth.AssuranceLevelAAL2,
			},
		},
		{
			name: "future timestamp rejected",
			assurance: auth.Assurance{
				AuthenticationMethods: []string{auth.AuthenticationMethodOTP},
				Level:                 auth.AssuranceLevelAAL2,
				MFAVerifiedAt:         now.Add(2 * time.Minute),
			},
		},
		{
			name: "aal2 without a recognized factor method",
			assurance: auth.Assurance{
				AuthenticationMethods: []string{auth.AuthenticationMethodOAuth},
				Level:                 auth.AssuranceLevelAAL2,
				MFAVerifiedAt:         now,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.assurance.HasRecentMFA(now, auth.DefaultRecentStepUpMaxAge))
		})
	}
}

func TestAssuranceHasMFAEvidence(t *testing.T) {
	verifiedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	require.True(t, auth.Assurance{
		AuthenticationMethods: []string{auth.AuthenticationMethodWebAuthn},
		Level:                 auth.AssuranceLevelAAL2,
		MFAVerifiedAt:         verifiedAt,
	}.HasMFAEvidence())
	require.False(t, auth.Assurance{
		AuthenticationMethods: []string{auth.AuthenticationMethodOAuth},
		Level:                 auth.AssuranceLevelAAL2,
		MFAVerifiedAt:         verifiedAt,
	}.HasMFAEvidence())
}
