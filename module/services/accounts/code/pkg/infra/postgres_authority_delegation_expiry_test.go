//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/storetest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Exercise the actual SQL write and both read paths, including replacement and
// revocation. A lifetime dropped at this boundary would turn expiry into a grant
// that stands indefinitely when the core reads it on the next request.
func TestAuthorityDelegationExpirySurvivesSQLReadback(t *testing.T) {
	org, person := uuid.NewString(), uuid.NewString()
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: org, Name: "Example Organization"}},
		Members:       []storetest.Member{{Organization: org, Person: person, Administrator: true}},
	})
	state := testStore.AuthorityCore()
	now := time.Now().UTC().Truncate(time.Microsecond)
	d := domain.Delegation{ID: uuid.NewString(), Organization: org, Grantor: person, Module: "example-module", Purpose: "a scheduled task"}
	for _, tc := range []struct {
		name    string
		expiry  time.Time
		revoked bool
	}{
		{"until revoked", time.Time{}, false},
		{"finite", now.Add(time.Hour), false},
		{"expired", now.Add(-time.Hour), false},
		{"revoked finite", now.Add(time.Hour), true},
		{"explicitly until revoked", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.ExpiresAt, d.Revoked = tc.expiry, tc.revoked
			require.NoError(t, state.Write(testCtx, func(tx authority.Tx) error { return tx.PutDelegation(d) }))
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				var stored *time.Time
				err := storetx.Tx(ctx).QueryRow(ctx, `SELECT expires_at FROM public.authority_core_delegations WHERE id=$1`, d.ID).Scan(&stored)
				require.NoError(t, err)
				if d.ExpiresAt.IsZero() {
					require.Nil(t, stored)
				} else {
					require.NotNil(t, stored)
					require.True(t, d.ExpiresAt.Equal(*stored))
				}
				return err
			}))
			require.NoError(t, state.Read(testCtx, func(tx authority.Tx) error {
				got, err := tx.Delegation(d.ID)
				require.NoError(t, err)
				require.True(t, d.ExpiresAt.Equal(got.ExpiresAt))
				require.Equal(t, d.Revoked, got.Revoked)
				require.Equal(t, d.Active(now), got.Active(now))
				all, err := tx.Delegations()
				require.NoError(t, err)
				found := false
				for _, row := range all {
					if row.ID == d.ID {
						found = true
						require.True(t, d.ExpiresAt.Equal(row.ExpiresAt))
						require.Equal(t, d.Revoked, row.Revoked)
					}
				}
				require.True(t, found)
				return nil
			}))
		})
	}
}
