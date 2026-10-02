//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"
)

// A tenant identity asked to run inside a WithControlPlane closure is refused,
// not run: joining that transaction would execute the tenant's work as
// app_control_plane with no org or user bound, across every tenant.
func TestTenantIdentityCannotJoinAControlPlaneTransaction(t *testing.T) {
	var role string
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.As(business.Identity{OrgID: uuid.NewString()}).Within(ctx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT current_user::text`).Scan(&role)
		})
	})
	require.ErrorContains(t, err, "cannot join the control-plane transaction")
	require.Empty(t, role, "the tenant identity's work ran as %q", role)
}

// System() asked to run inside a request transaction is refused as well: the
// work expects app_control_plane and would silently run as app_tenant.
func TestSystemCannotJoinARequestTransaction(t *testing.T) {
	var role string
	err := testStore.WithOrgTx(testCtx, uuid.NewString(), func(ctx context.Context) error {
		return testStore.As(business.System()).Within(ctx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT current_user::text`).Scan(&role)
		})
	})
	require.ErrorContains(t, err, "cannot join the request transaction")
	require.Empty(t, role, "System()'s work ran as %q", role)
}

// A tenant identity asked to run inside a request transaction bound for another
// org is refused, not run: GetPrincipal filters by nothing but the
// transaction's own binding, so joining would hand org A's agent to code acting
// for org B. The same lookup for org A's own identity joins the transaction.
func TestTenantIdentityCannotJoinAnotherOrgsRequestTransaction(t *testing.T) {
	owner := seedUser(t)
	orgA := seedOrg(t, owner)
	orgB := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgA, "test.codefly.dev/ambient-scope:0.1.0")

	var leaked *business.Principal
	err := testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		var err error
		leaked, err = testStore.As(business.Identity{OrgID: orgB}).GetPrincipal(ctx, agent.ID)
		return err
	})
	require.ErrorContains(t, err, "bound for a different org or user")
	require.Nil(t, leaked, "org B's identity read org A's agent")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		got, err := testStore.As(business.Identity{OrgID: orgA}).GetPrincipal(ctx, agent.ID)
		require.NoError(t, err)
		require.Equal(t, agent.ID, got.ID)
		return nil
	}))
}

// Store methods called inside a WithControlPlane closure still run on its
// transaction, as app_control_plane: a user row no request transaction without
// that user bound can read is visible there, and a nested As(System()) joins the
// same transaction rather than opening a second one.
func TestStoreMethodsInsideWithControlPlaneRunOnItsTransaction(t *testing.T) {
	userID := seedUser(t)

	_, err := testStore.GetUser(testCtx, userID)
	require.Error(t, err, "a request connection with no user bound must not read the user")

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		outer := storetx.Tx(ctx)
		var role string
		require.NoError(t, outer.QueryRow(ctx, `SELECT current_user::text`).Scan(&role))
		require.Equal(t, "app_control_plane", role)

		user, err := testStore.GetUser(ctx, userID)
		require.NoError(t, err, "a Store method inside WithControlPlane must run on the control-plane transaction")
		require.Equal(t, userID, user.Uuid)

		return testStore.As(business.System()).Within(ctx, func(ctx context.Context) error {
			require.Equal(t, outer, storetx.Tx(ctx), "System() joins the ambient control-plane transaction")
			return nil
		})
	}))
}
