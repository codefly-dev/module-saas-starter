package adapters

import (
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

func TestDelegationProjectionUsesTheAuthorityClockAndExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		expiry          time.Time
		revoked, active bool
	}{
		{"until revoked", time.Time{}, false, true},
		{"future", now.Add(time.Hour), false, true},
		{"at expiry", now, false, false},
		{"expired", now.Add(-time.Second), false, false},
		{"revoked unexpired", now.Add(time.Hour), true, false},
		{"revoked without expiry", time.Time{}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := domain.Delegation{ID: "example-grant", Organization: "example-org", Grantor: "example-person", Module: "example-module", ExpiresAt: tc.expiry, Revoked: tc.revoked}
			got := delegationToWire(d, now)
			require.Equal(t, tc.active, got.Active)
			if tc.expiry.IsZero() {
				require.Zero(t, got.ExpiresAtUnix)
			} else {
				require.Equal(t, tc.expiry.Unix(), got.ExpiresAtUnix)
			}
			require.Equal(t, d.ID, got.DelegationId)
			require.Equal(t, d.Organization, got.OrganizationId)
		})
	}
}
