//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
)

var (
	solutionUse = &gen.Permission{Resource: "solution", Action: "use"}
	docRead     = &gen.Permission{Resource: "doc", Action: "read"}
	docWrite    = &gen.Permission{Resource: "doc", Action: "write"}
)

func derivedRole(orgID, cause, description string, perms ...*gen.Permission) business.DerivedRole {
	return business.DerivedRole{OrgID: orgID, DerivedFrom: cause,
		Name: "solution role · " + cause, Description: description, Permissions: perms}
}

// ensureDerivedRole ensures the role in its own committed organization
// transaction and returns its id.
func ensureDerivedRole(t *testing.T, role business.DerivedRole) string {
	t.Helper()
	var id string
	require.NoError(t, testStore.WithOrgTx(testCtx, role.OrgID, func(ctx context.Context) error {
		var err error
		id, err = testStore.EnsureDerivedRole(ctx, role)
		return err
	}))
	return id
}

func authorizationRevision(t *testing.T, orgID string) int64 {
	t.Helper()
	var r int64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT revision FROM public.organization_authorization_revisions WHERE org_id = $1`, orgID).Scan(&r)
	}))
	return r
}

func rolePermissions(t *testing.T, roleID string) []string {
	t.Helper()
	out := []string{}
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

// Ensuring a derived role that is already as declared writes nothing, so it
// leaves the organization's authorization revision (what every capability is
// minted at) where it is; a changed description or permission set is written,
// and only what changed.
func TestEnsuringADerivedRoleAsDeclaredMovesNoRevision(t *testing.T) {
	orgID := seedOrg(t, seedUser(t))
	const cause = "installation=revision-test;role=viewer"
	revision := func() int64 { return authorizationRevision(t, orgID) }

	id := ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role", solutionUse, docRead))
	settled := revision()
	for range 3 {
		require.Equal(t, id, ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role", solutionUse, docRead)))
	}
	require.Equal(t, settled, revision(), "ensuring a role already as declared wrote it")

	require.Equal(t, id, ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role", solutionUse, docWrite)))
	require.ElementsMatch(t, []string{"solution:use", "doc:write"}, rolePermissions(t, id))
	moved := revision()
	require.Greater(t, moved, settled, "a changed permission set is written")

	require.Equal(t, id, ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role, renamed", solutionUse, docWrite)))
	renamed := revision()
	require.Greater(t, renamed, moved, "a changed description is written")
	require.ElementsMatch(t, []string{"solution:use", "doc:write"}, rolePermissions(t, id))

	require.Equal(t, id, ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role, renamed", solutionUse)))
	require.ElementsMatch(t, []string{"solution:use"}, rolePermissions(t, id))
	removed := revision()
	require.Greater(t, removed, renamed, "a permission no longer declared is removed, and that is written")

	require.Equal(t, id, ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role, renamed", solutionUse)))
	require.Equal(t, removed, revision(), "re-ensuring the narrowed role wrote it")
}

// Two ensures of the same cause with different declarations serialize on the
// role row: the second waits for the first to commit, reads what it wrote, and
// ends at its own declaration (last writer wins). Without the lock the second
// would read before the first committed and the role would end at the union of
// both, keeping the permission the later declaration dropped.
func TestRacingEnsuresOfADerivedRoleEndAtTheLastDeclaration(t *testing.T) {
	orgID := seedOrg(t, seedUser(t))
	const cause = "installation=race-test;role=viewer"
	id := ensureDerivedRole(t, derivedRole(orgID, cause, "the viewer role", solutionUse))

	added, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			if _, err := testStore.EnsureDerivedRole(ctx, derivedRole(orgID, cause, "the viewer role", solutionUse, docWrite)); err != nil {
				return err
			}
			close(added)
			<-release
			return nil
		})
	}()
	select {
	case <-added:
	case err := <-first:
		t.Fatalf("the first ensure ended before it held the role: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		second <- testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			_, err := testStore.EnsureDerivedRole(ctx, derivedRole(orgID, cause, "the viewer role", solutionUse))
			return err
		})
	}()
	select {
	case err := <-second:
		close(release)
		<-first
		t.Fatalf("the second ensure did not wait for the first to commit (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	require.ElementsMatch(t, []string{"solution:use"}, rolePermissions(t, id),
		"the later declaration drops doc:write; the role must not end at the union")
}
