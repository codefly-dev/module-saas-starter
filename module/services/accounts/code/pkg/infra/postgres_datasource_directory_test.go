//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func directoryFixture(t *testing.T) (orgID, userA, userB, teamID string) {
	t.Helper()
	userA, userB = seedUser(t), seedUser(t)
	orgID = seedOrg(t, userA)
	teamID = business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateTeam(ctx, &gen.Team{Id: teamID, OrgId: orgID, Name: "Team", Slug: "team-" + teamID[:8], Path: "team_" + strings.ReplaceAll(teamID, "-", "_")})
	}))
	return orgID, userA, userB, teamID
}

// TestDatasourceDirectoryRowsStayInTheirTenant links, binds and verifies in one
// organization and proves another organization's transaction sees none of it,
// and that one provider account names one person per organization.
func TestDatasourceDirectoryRowsStayInTheirTenant(t *testing.T) {
	orgID, userA, userB, teamID := directoryFixture(t)
	otherOrg := seedOrg(t, seedUser(t))

	var link *business.DatasourceAccountLink
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		link, err = testStore.UpsertDatasourceAccountLink(ctx, &business.DatasourceAccountLink{
			ID: business.NewIDString(), OrgID: orgID, UserID: userA, Connector: "github", ProviderAccountID: "1001", ProviderAccountLogin: "jane",
		})
		if err != nil {
			return err
		}
		if err := testStore.InsertDatasourceGroupBinding(ctx, &business.DatasourceGroupBinding{
			ID: business.NewIDString(), OrgID: orgID, Connector: "github", ProviderGroupID: "acme/platform", TeamID: teamID, CreatedBy: userA,
		}); err != nil {
			return err
		}
		d := &business.DatasourceDomain{ID: business.NewIDString(), OrgID: orgID, Domain: "example.com",
			VerificationToken: strings.Repeat("t", 43), CreatedBy: userA}
		if err := testStore.InsertDatasourceDomain(ctx, d); err != nil {
			return err
		}
		return testStore.MarkDatasourceDomainVerified(ctx, orgID, d.ID, time.Now().UTC())
	}))

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		users, err := testStore.LinkedDatasourceUsers(ctx, orgID, "github", []string{"1001", "1002"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"1001": userA}, users)
		teams, err := testStore.BoundDatasourceTeams(ctx, orgID, "github", []string{"acme/platform", "acme/other"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"acme/platform": teamID}, teams)
		verified, err := testStore.VerifiedDatasourceDomains(ctx, orgID, []string{"example.com", "example.org"})
		require.NoError(t, err)
		require.Equal(t, map[string]bool{"example.com": true}, verified)

		// The same person again keeps the link; someone else is refused.
		again, err := testStore.UpsertDatasourceAccountLink(ctx, &business.DatasourceAccountLink{
			ID: business.NewIDString(), OrgID: orgID, UserID: userA, Connector: "github", ProviderAccountID: "1001", ProviderAccountLogin: "jane-renamed",
		})
		require.NoError(t, err)
		require.Equal(t, link.ID, again.ID)
		require.Equal(t, "jane-renamed", again.ProviderAccountLogin)
		_, err = testStore.UpsertDatasourceAccountLink(ctx, &business.DatasourceAccountLink{
			ID: business.NewIDString(), OrgID: orgID, UserID: userB, Connector: "github", ProviderAccountID: "1001",
		})
		require.ErrorIs(t, err, business.ErrDatasourceAccountLinkedElsewhere)
		return nil
	}))
	// A second binding of the same group is refused; the refusal aborts its
	// transaction, as the service's own does.
	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.InsertDatasourceGroupBinding(ctx, &business.DatasourceGroupBinding{
			ID: business.NewIDString(), OrgID: orgID, Connector: "github", ProviderGroupID: "acme/platform", TeamID: teamID,
		})
	})
	require.ErrorIs(t, err, business.ErrDatasourceGroupAlreadyBound)

	// Another tenant's transaction sees nothing, even naming the org outright.
	require.NoError(t, testStore.WithOrgTx(testCtx, otherOrg, func(ctx context.Context) error {
		users, err := testStore.LinkedDatasourceUsers(ctx, orgID, "github", []string{"1001"})
		require.NoError(t, err)
		require.Empty(t, users)
		links, err := testStore.ListDatasourceAccountLinks(ctx, orgID, "")
		require.NoError(t, err)
		require.Empty(t, links)
		domains, err := testStore.ListDatasourceDomains(ctx, orgID)
		require.NoError(t, err)
		require.Empty(t, domains)
		gone, err := testStore.DeleteDatasourceGroupBinding(ctx, orgID, link.ID)
		require.NoError(t, err)
		require.Nil(t, gone)
		return nil
	}))
}

// TestDatasourceCredentialBudgetMetersAWindow proves the meter: it spends up to
// the limit, refuses past it, reopens the window once it is over, and honours a
// provider block until it lifts.
func TestDatasourceCredentialBudgetMetersAWindow(t *testing.T) {
	key := "test:budget:" + business.NewIDString()
	now := time.Now().UTC().Truncate(time.Second)
	spend := func(at time.Time, limit int) (bool, time.Time, time.Time) {
		var spent bool
		var reset, blocked time.Time
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			var err error
			spent, reset, blocked, err = testStore.SpendDatasourceBudget(ctx, key, limit, time.Hour, at)
			return err
		}))
		return spent, reset, blocked
	}
	for i := 0; i < 3; i++ {
		spent, _, _ := spend(now, 3)
		require.True(t, spent, "operation %d of 3", i+1)
	}
	spent, reset, _ := spend(now, 3)
	require.False(t, spent)
	require.True(t, reset.Equal(now.Add(time.Hour)))
	// A higher limit (an interactive caller) still has room in the same window.
	spent, _, _ = spend(now, 4)
	require.True(t, spent)
	spent, _, _ = spend(now.Add(time.Hour), 3)
	require.True(t, spent, "a new window opens once the old one is over")

	until := now.Add(3 * time.Hour)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.BlockDatasourceBudget(ctx, key, until)
	}))
	spent, _, blocked := spend(now.Add(time.Hour+time.Minute), 100)
	require.False(t, spent)
	require.True(t, blocked.Equal(until))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.BlockDatasourceBudget(ctx, key, now) // an earlier block never shortens it
	}))
	_, _, blocked = spend(now.Add(time.Hour+2*time.Minute), 100)
	require.True(t, blocked.Equal(until))
	spent, _, _ = spend(until.Add(time.Second), 100)
	require.True(t, spent, "the block lifts when the provider said")
}

// TestDatasourceCredentialBudgetsAreNotTenantReachable holds the platform
// relation to its grants: a tenant transaction cannot read or spend it.
func TestDatasourceCredentialBudgetsAreNotTenantReachable(t *testing.T) {
	orgID, _, _, _ := directoryFixture(t)
	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, _, _, err := testStore.SpendDatasourceBudget(ctx, "test:tenant:"+orgID, 1, time.Hour, time.Now())
		return err
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
}
