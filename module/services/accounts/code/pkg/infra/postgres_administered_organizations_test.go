package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func seedOrgAdministrator(t *testing.T, orgID, userID string) {
	t.Helper()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, userID, "admin"))
}

// The self-service deactivation path runs in a transaction scoped to the
// identity being deactivated and to no organization, where organization_members
// and a co-member's users row are both invisible. The operation has to resolve
// the administration anyway, and the SECURITY DEFINER function is what makes
// that possible without widening either policy.
func TestAdministeredOrganizationsResolveUnderTheIdentitysOwnScope(t *testing.T) {
	administrator := seedUser(t)
	peer := seedUser(t)
	orgID := seedOrg(t, administrator)
	seedOrgAdministrator(t, orgID, administrator)
	seedOrgAdministrator(t, orgID, peer)
	seedOrgMember(t, orgID, seedUser(t))

	require.NoError(t, testStore.As(business.Identity{UserID: administrator}).Within(testCtx, func(ctx context.Context) error {
		administered, err := testStore.ListAdministeredOrganizations(ctx, administrator)
		require.NoError(t, err)
		require.Len(t, administered, 1)
		require.Equal(t, orgID, administered[0].OrgID)
		require.Equal(t, 2, administered[0].EligibleAdministrators)
		require.Equal(t, 2, administered[0].OtherActiveMembers,
			"the peer administrator and the ordinary member")
		return nil
	}))
}

// A deactivated administrator is not one, so it neither administers an
// organization nor counts towards anybody else's.
func TestAdministeredOrganizationsExcludeIdentitiesThatCannotAuthenticate(t *testing.T) {
	administrator := seedUser(t)
	ghost := seedUser(t)
	orgID := seedOrg(t, administrator)
	seedOrgAdministrator(t, orgID, administrator)
	seedOrgAdministrator(t, orgID, ghost)
	setInfraUserStatus(t, ghost, "deleted")

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		administered, err := testStore.ListAdministeredOrganizations(ctx, administrator)
		require.NoError(t, err)
		require.Len(t, administered, 1)
		require.Equal(t, 1, administered[0].EligibleAdministrators)
		require.Zero(t, administered[0].OtherActiveMembers,
			"a member who cannot authenticate is nobody to strand")

		administered, err = testStore.ListAdministeredOrganizations(ctx, ghost)
		require.NoError(t, err)
		require.Empty(t, administered)
		return nil
	}))
}

// The dangerous answer here is an empty one: a transaction that can resolve
// neither its own identity's administration nor anybody else's would report a
// clean identity and let the deactivation through. Refuse instead.
func TestAdministeredOrganizationsRefuseATransactionThatCanAnswerNeitherWay(t *testing.T) {
	administrator := seedUser(t)
	outsider := seedUser(t)
	orgID := seedOrg(t, administrator)
	seedOrgAdministrator(t, orgID, administrator)

	require.NoError(t, testStore.As(business.Identity{UserID: outsider}).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.ListAdministeredOrganizations(ctx, administrator)
		require.Error(t, err, "an identity-scoped transaction must not answer for another identity")
		return nil
	}))
}

func TestAdministeredOrganizationsFunctionHasPinnedAuthority(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var owner string
		var securityDefiner, tenantCanExecute, publicCanExecute bool
		err := tx.QueryRow(ctx, `
			SELECT pg_get_userbyid(proc.proowner),
			       proc.prosecdef,
			       has_function_privilege(
			           'app_tenant',
			           'public.identity_administered_organizations(uuid)',
			           'EXECUTE'
			       ),
			       EXISTS (
			           SELECT 1
			           FROM aclexplode(proc.proacl) acl
			           WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
			       )
			FROM pg_proc proc
			JOIN pg_namespace namespace ON namespace.oid = proc.pronamespace
			WHERE namespace.nspname = 'public'
			  AND proc.proname = 'identity_administered_organizations'`,
		).Scan(&owner, &securityDefiner, &tenantCanExecute, &publicCanExecute)
		require.NoError(t, err)
		require.Equal(t, "app_control_plane", owner)
		require.True(t, securityDefiner)
		require.True(t, tenantCanExecute)
		require.False(t, publicCanExecute)
		return nil
	}))
}

func setInfraUserStatus(t *testing.T, userID, status string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET status = $2 WHERE uuid = $1`, userID, status)
		return err
	}))
}
