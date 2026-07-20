package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestUsersVisibilityUsesSelfAndNarrowOrganizationOperation(t *testing.T) {
	userA := seedUser(t)
	userB := seedUser(t)
	outsider := seedUser(t)
	orgID := seedOrg(t, userA)
	seedOrgMember(t, orgID, userA)
	seedOrgMember(t, orgID, userB)

	var userBEmail string
	require.NoError(t, testStore.As(business.Identity{UserID: userB}).Within(testCtx, func(ctx context.Context) error {
		user, err := testStore.GetUser(ctx, userB)
		if err == nil {
			userBEmail = user.PrimaryEmail
		}
		return err
	}))

	// A bare app_tenant connection has no user identity and sees no user rows.
	_, err := testStore.GetUser(context.Background(), userA)
	require.Error(t, err)

	// A user can read itself but cannot substitute another user's UUID.
	require.NoError(t, testStore.As(business.Identity{UserID: userA}).Within(testCtx, func(ctx context.Context) error {
		self, err := testStore.GetUser(ctx, userA)
		require.NoError(t, err)
		require.Equal(t, userA, self.Uuid)

		_, err = testStore.GetUser(ctx, userB)
		require.Error(t, err)
		return nil
	}))

	// Organization scope alone does not expose the complete users relation.
	// It exposes only the primary-email operation for a current co-member.
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.GetUser(ctx, userB)
		require.Error(t, err)

		email, err := testStore.GetOrganizationMemberPrimaryEmail(ctx, userB)
		require.NoError(t, err)
		require.Equal(t, userBEmail, email)

		email, err = testStore.GetOrganizationMemberPrimaryEmail(ctx, outsider)
		require.NoError(t, err)
		require.Empty(t, email)
		return nil
	}))

	// Cross-user account administration remains available only through the
	// named control-plane identity.
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		user, err := testStore.GetUser(ctx, userB)
		require.NoError(t, err)
		require.Equal(t, userB, user.Uuid)
		return nil
	}))
}

func TestOrganizationMemberEmailFunctionHasPinnedAuthority(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var owner string
		var securityDefiner, tenantCanExecute, publicCanExecute bool
		var policyExpression string
		err := tx.QueryRow(ctx, `
			SELECT pg_get_userbyid(proc.proowner),
			       proc.prosecdef,
			       has_function_privilege(
			           'app_tenant',
			           'public.organization_member_primary_email(uuid)',
			           'EXECUTE'
			       ),
			       EXISTS (
			           SELECT 1
			           FROM aclexplode(proc.proacl) acl
			           WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
			       ),
			       pg_get_expr(policy.polqual, policy.polrelid)
			FROM pg_proc proc
			JOIN pg_namespace namespace ON namespace.oid = proc.pronamespace
			JOIN pg_policy policy
			  ON policy.polrelid = 'public.users'::regclass
			 AND policy.polname = 'users_select'
			WHERE namespace.nspname = 'public'
			  AND proc.proname = 'organization_member_primary_email'`,
		).Scan(&owner, &securityDefiner, &tenantCanExecute, &publicCanExecute, &policyExpression)
		require.NoError(t, err)
		require.Equal(t, "app_control_plane", owner)
		require.True(t, securityDefiner)
		require.True(t, tenantCanExecute)
		require.False(t, publicCanExecute)
		require.Equal(t,
			"((uuid)::text = current_setting('app.current_user_id'::text, true))",
			policyExpression,
		)
		return nil
	}))
}
