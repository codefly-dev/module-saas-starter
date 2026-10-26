//go:build !pure

package pgauth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/infra/storetx"
)

func TestLookupIdentityUsesLoginSelectionWithoutLoginEffects(t *testing.T) {
	resetAuthTables(t)
	ctx := t.Context()
	r := pgauth.NewResolver(testStore)
	r.SetBootstrapAdminEmail("")
	c := claims("reader@example.com", "fixture-reader")
	id, err := r.Resolve(ctx, c, auth.SignupIntent{})
	require.NoError(t, err)
	first := seedOrg(t, id.UserID, "First", "")
	second := seedOrg(t, id.UserID, "Second", "example-provider-org")
	addMember(t, first, id.UserID, "member", time.Now().Add(-time.Hour))
	addMember(t, second, id.UserID, "admin", time.Now())

	for _, tc := range []struct {
		name, providerOrg, role string
		defaultOrg, wantOrg     uuid.UUID
	}{
		{name: "ambiguous is orgless"},
		{name: "persisted default overrides oldest membership", defaultOrg: second, wantOrg: second, role: "admin"},
		{name: "provider overrides default", defaultOrg: first, providerOrg: "example-provider-org", wantOrg: second, role: "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
				_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE users SET default_org_id = $2 WHERE uuid = $1`, id.UserID, nullableUUID(tc.defaultOrg))
				return err
			}))
			c.ProviderOrgID = tc.providerOrg
			r.SetBootstrapAdminEmail(c.Email)
			require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
				_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE user_identities SET last_used = '2000-01-01' WHERE user_uuid = $1`, id.UserID)
				return err
			}))
			got, err := r.LookupIdentity(ctx, c)
			require.NoError(t, err)
			require.Equal(t, id.UserID, got.UserID)
			require.Equal(t, tc.wantOrg, got.OrgID)
			require.Equal(t, tc.role, got.OrgRole)
			require.Equal(t, uuid.Nil, got.SessionID)
			var unchanged bool
			scanControlPlane(t, &unchanged, `SELECT
				NOT EXISTS (SELECT 1 FROM sessions)
				AND NOT EXISTS (SELECT 1 FROM platform_admins)
				AND NOT EXISTS (SELECT 1 FROM bootstrap_state WHERE bootstrapped_at IS NOT NULL)
				AND (SELECT last_used = '2000-01-01' FROM user_identities WHERE user_uuid = $1)
				AND (SELECT count(*) = 2 FROM organization_members WHERE user_id = $1)`, id.UserID)
			require.True(t, unchanged, "discovery must not become a login or bootstrap")
			r.SetBootstrapAdminEmail("")
			login, err := r.Resolve(ctx, c, auth.LoginIntent{})
			require.NoError(t, err)
			require.Equal(t, got.UserID, login.UserID)
			require.Equal(t, got.OrgID, login.OrgID)
			require.Equal(t, got.OrgRole, login.OrgRole)
		})
	}
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func TestLookupIdentityRefusesMissingAndInactivePeople(t *testing.T) {
	resetAuthTables(t)
	r := pgauth.NewResolver(testStore)
	r.SetBootstrapAdminEmail("")
	c := claims("missing@example.com", "missing-subject")
	got, err := r.LookupIdentity(t.Context(), c)
	require.ErrorIs(t, err, auth.ErrNoAccount)
	require.Nil(t, got)
	var count int
	scanControlPlane(t, &count, `SELECT count(*) FROM users`)
	require.Zero(t, count, "a lookup cannot provision the missing person")
	id, err := r.Resolve(t.Context(), c, auth.SignupIntent{})
	require.NoError(t, err)
	require.NoError(t, testStore.WithControlPlane(t.Context(), func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE users SET status = 'suspended' WHERE uuid = $1`, id.UserID)
		return err
	}))
	got, err = r.LookupIdentity(t.Context(), c)
	require.ErrorIs(t, err, auth.ErrAccountInactive)
	require.Nil(t, got)
}
