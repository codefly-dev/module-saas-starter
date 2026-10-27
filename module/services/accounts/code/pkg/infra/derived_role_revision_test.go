//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
)

// Ensuring a derived role that is already as declared writes nothing, so it
// leaves the organization's authorization revision (what every capability is
// minted at) where it is; a changed description or permission set is written,
// and only what changed.
func TestEnsuringADerivedRoleAsDeclaredMovesNoRevision(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	revision := func() int64 {
		t.Helper()
		var r int64
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT revision FROM public.organization_authorization_revisions WHERE org_id = $1`, orgID).Scan(&r)
		}))
		return r
	}
	permissions := func(roleID string) []string {
		t.Helper()
		var out []string
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			rows, err := storetx.Tx(ctx).Query(ctx, `SELECT resource || ':' || action FROM role_permissions WHERE role_id = $1`, roleID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					return err
				}
				out = append(out, p)
			}
			return rows.Err()
		}))
		return out
	}
	ensure := func(description string, perms ...*gen.Permission) string {
		t.Helper()
		var id string
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			var err error
			id, err = testStore.EnsureDerivedRole(ctx, business.DerivedRole{OrgID: orgID, DerivedFrom: "installation=revision-test;role=viewer",
				Name: "solution role · revision-test", Description: description, Permissions: perms})
			return err
		}))
		return id
	}
	use, read, write := &gen.Permission{Resource: "solution", Action: "use"}, &gen.Permission{Resource: "doc", Action: "read"}, &gen.Permission{Resource: "doc", Action: "write"}

	id := ensure("the viewer role", use, read)
	settled := revision()
	for range 3 {
		require.Equal(t, id, ensure("the viewer role", use, read))
	}
	require.Equal(t, settled, revision(), "ensuring a role already as declared wrote it")

	require.Equal(t, id, ensure("the viewer role", use, write))
	require.ElementsMatch(t, []string{"solution:use", "doc:write"}, permissions(id))
	moved := revision()
	require.Greater(t, moved, settled, "a changed permission set is written")

	require.Equal(t, id, ensure("the viewer role, renamed", use, write))
	require.Greater(t, revision(), moved, "a changed description is written")
	require.ElementsMatch(t, []string{"solution:use", "doc:write"}, permissions(id))
}
