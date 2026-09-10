package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// runMembershipDiagnostic re-runs the inventory the way an operator would,
// through the one role granted EXECUTE on it.
func runMembershipDiagnostic(t *testing.T) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var outstanding int
		return tx.QueryRow(ctx,
			`SELECT public.record_membership_integrity_findings()`).Scan(&outstanding)
	}))
}

// membershipFindings returns the finding kinds recorded against one
// organization, so assertions never depend on what the rest of the suite left
// in the shared database.
func membershipFindings(t *testing.T, orgID string) []string {
	t.Helper()
	var found []string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		rows, err := tx.Query(ctx, `
			SELECT finding FROM membership_integrity_findings
			WHERE org_id = $1 ORDER BY finding`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var finding string
			if err := rows.Scan(&finding); err != nil {
				return err
			}
			found = append(found, finding)
		}
		return rows.Err()
	}))
	return found
}

// An organization with no owner or admin membership is reported, and its owner
// of record is reported as non-administrative in the same pass — the two
// findings are independent, and this organization is in both states at once.
func TestMembershipDiagnosticReportsAnOrganizationWithNoAdministrator(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	runMembershipDiagnostic(t)

	require.Equal(t, []string{
		"organization_without_administrator",
		"owner_of_record_is_not_an_administrator",
	}, membershipFindings(t, orgID))
}

// An organization whose owner of record holds only an ordinary membership is
// reported for that alone: somebody can administer it, so the first finding
// does not apply and the two are not collapsed into one.
func TestMembershipDiagnosticSeparatesOwnerMismatchFromMissingAdministrator(t *testing.T) {
	owner := seedUser(t)
	administrator := seedUser(t)
	orgID := seedOrg(t, owner)
	seedOrgMember(t, orgID, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, administrator, "admin",
	))

	runMembershipDiagnostic(t)

	require.Equal(t, []string{"owner_of_record_is_not_an_administrator"},
		membershipFindings(t, orgID))
}

// A healthy organization — the owner of record is also an administrative
// member — produces nothing. Without this the diagnostic could be reporting
// every organization and still pass the tests above.
func TestMembershipDiagnosticReportsNothingForAConsistentOrganization(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, owner, "owner",
	))

	runMembershipDiagnostic(t)

	require.Empty(t, membershipFindings(t, orgID))
}

// The point of a re-runnable scan: after an operator repairs an organization,
// the next run drops its finding rather than leaving a resolved row behind for
// somebody to work through again.
func TestMembershipDiagnosticClearsARepairedOrganization(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	runMembershipDiagnostic(t)
	require.NotEmpty(t, membershipFindings(t, orgID))

	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, owner, "owner",
	))
	runMembershipDiagnostic(t)

	require.Empty(t, membershipFindings(t, orgID),
		"a repaired organization must leave the backlog on the next scan")
}

// The findings are operator evidence, not product data: request traffic has no
// authority over them at all, so a tenant transaction cannot read another
// organization's backlog or forge one of its own.
func TestMembershipDiagnosticIsNotReachableByRequestTraffic(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	runMembershipDiagnostic(t)

	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var count int
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM membership_integrity_findings`).Scan(&count)
	})
	require.Error(t, err, "app_tenant holds no grant on the findings table")

	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, execErr := tx.Exec(ctx,
			`SELECT public.record_membership_integrity_findings()`)
		return execErr
	})
	require.Error(t, err, "app_tenant may not run a cross-organization inventory")
}
