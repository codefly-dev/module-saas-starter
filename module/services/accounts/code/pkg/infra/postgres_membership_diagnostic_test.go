//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// runMembershipDiagnostic re-runs the inventory the way an operator does, through
// the store method `membership-integrity-scan` calls — which assumes the control
// plane, the only boundary that can see every organization.
func runMembershipDiagnostic(t *testing.T) int {
	t.Helper()
	outstanding, err := testStore.RecordMembershipIntegrityFindings(testCtx)
	require.NoError(t, err)
	return outstanding
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

// An organization with no owner or admin membership is reported once. Its owner
// of record is trivially not an administrator too, but reporting that as well
// would count the same organization twice in a backlog somebody is trying to
// size — the findings are mutually exclusive, and the stronger one wins.
func TestMembershipDiagnosticReportsAnOrganizationWithNoAdministrator(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	runMembershipDiagnostic(t)

	require.Equal(t, []string{"organization_without_administrator"},
		membershipFindings(t, orgID))
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

// The owner's membership role survives on both findings, so an operator can
// tell "the owner holds no membership at all" from "the owner is a plain
// member" without going back to the membership table. Collapsing the duplicate
// finding must not take this with it.
func TestMembershipDiagnosticRecordsTheOwnersMembershipRole(t *testing.T) {
	ownerWithNoMembership := seedUser(t)
	orphaned := seedOrg(t, ownerWithNoMembership)

	demotedOwner := seedUser(t)
	administrator := seedUser(t)
	mismatched := seedOrg(t, demotedOwner)
	seedOrgMember(t, mismatched, demotedOwner)
	require.NoError(t, testStore.As(business.Identity{OrgID: mismatched}).AddOrgMember(
		testCtx, administrator, "admin",
	))

	runMembershipDiagnostic(t)

	require.Equal(t, "null", membershipFindingDetail(t, orphaned, "membership_role"),
		"an owner holding no membership at all must be distinguishable")
	require.Equal(t, `"member"`, membershipFindingDetail(t, mismatched, "membership_role"))
}

func membershipFindingDetail(t *testing.T, orgID, key string) string {
	t.Helper()
	var value string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx,
			`SELECT (detail -> $2)::text FROM membership_integrity_findings WHERE org_id = $1`,
			orgID, key).Scan(&value)
	}))
	return value
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

// The count the scan returns is the count an operator sizes the work from, so it
// has to be organizations rather than rows about organizations. Asserted as a
// whole-table invariant rather than a before/after delta: sibling test packages
// share this database and seed organizations concurrently, so a delta would be
// flaky where the invariant is not.
func TestMembershipDiagnosticCountsEachOrganizationOnce(t *testing.T) {
	owner := seedUser(t)
	seedOrg(t, owner)

	outstanding := runMembershipDiagnostic(t)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var rows, organizations int
		if err := tx.QueryRow(ctx,
			`SELECT count(*), count(DISTINCT org_id) FROM membership_integrity_findings`,
		).Scan(&rows, &organizations); err != nil {
			return err
		}
		require.Equal(t, organizations, rows,
			"the findings are mutually exclusive: no organization may hold two")
		require.Equal(t, rows, outstanding,
			"the returned count is the backlog an operator sizes the work from")
		return nil
	}))
}

// The operator's read path reports the rows that exist. Reading the same table
// as any role that does not span tenants — the store owner-connection
// included — returns silently empty instead, which is why this path exists at
// all.
func TestMembershipDiagnosticListsTheBacklogThroughTheControlPlane(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	runMembershipDiagnostic(t)

	findings, err := testStore.ListMembershipIntegrityFindings(testCtx)
	require.NoError(t, err)

	var mine []string
	for _, finding := range findings {
		if finding.OrgID == orgID {
			mine = append(mine, finding.Finding)
			require.False(t, finding.FoundAt.IsZero())
			require.Contains(t, string(finding.Detail), owner)
		}
	}
	require.Equal(t, []string{"organization_without_administrator"}, mine)
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

// setUserStatus moves an identity out of 'active' the way DeleteUser (a soft
// delete) and suspension do: the organization_members row is left standing, so
// the membership table still looks administrative.
func setUserStatus(t *testing.T, userID, status string) {
	t.Helper()
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx,
			`UPDATE users SET status = $2::user_status WHERE uuid = $1`, userID, status)
		return err
	}))
}

// The eligibility gap this inventory shipped with, reported from the #594
// deactivation-continuity work. An organization whose only administrator has
// been deleted or suspended keeps the membership row, so a scan counting
// administrative rows regardless of identity status calls it healthy — while
// nobody can sign in to administer it. findIdentity admits only active
// identities, so that organization is exactly the backlog this exists to size.
//
// Reported as its own finding rather than folded into
// organization_without_administrator: the rows are there and reactivating one
// identity may be the whole repair, which is a different and usually cheaper
// operator decision than choosing someone to grant authority to.
func TestMembershipDiagnosticReportsAnOrganizationWhoseAdministratorsAreAllInactive(t *testing.T) {
	for _, status := range []string{"deleted", "suspended", "inactive"} {
		t.Run(status, func(t *testing.T) {
			owner := seedUser(t)
			orgID := seedOrg(t, owner)
			require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
				testCtx, owner, "owner",
			))

			runMembershipDiagnostic(t)
			require.Empty(t, membershipFindings(t, orgID),
				"precondition: an active owner-administrator is healthy")

			setUserStatus(t, owner, status)
			runMembershipDiagnostic(t)

			require.Equal(t, []string{"organization_without_an_eligible_administrator"},
				membershipFindings(t, orgID),
				"an administrator who cannot sign in cannot administer")
			require.Equal(t, "1", membershipFindingDetail(t, orgID, "administrative_members"),
				"the membership row is still there — that is the point")
			require.Equal(t, "0", membershipFindingDetail(t, orgID, "eligible_administrators"))
		})
	}
}

// The same question for the owner of record: a membership that IS
// administrative, held by an identity that is not active. As long as somebody
// else can still administer the organization, that is the owner-mismatch
// finding, and detail has to make clear it is the identity rather than the
// membership that is wrong.
func TestMembershipDiagnosticReportsAnInactiveOwnerWhileAnotherAdministratorRemains(t *testing.T) {
	owner := seedUser(t)
	administrator := seedUser(t)
	orgID := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, owner, "owner",
	))
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, administrator, "admin",
	))

	runMembershipDiagnostic(t)
	require.Empty(t, membershipFindings(t, orgID))

	setUserStatus(t, owner, "suspended")
	runMembershipDiagnostic(t)

	require.Equal(t, []string{"owner_of_record_is_not_an_administrator"},
		membershipFindings(t, orgID))
	require.Equal(t, `"suspended"`, membershipFindingDetail(t, orgID, "owner_status"))
	require.Equal(t, `"owner"`, membershipFindingDetail(t, orgID, "membership_role"),
		"the membership is administrative; it is the identity that is not")
	require.Equal(t, "1", membershipFindingDetail(t, orgID, "eligible_administrators"),
		"the remaining administrator is why this is a mismatch and not an empty organization")
}

// Regression test for the defect this diagnostic shipped with.
//
// The migration ran the scan directly under the store owner-connection. That
// principal is a plain table owner, and organizations / organization_members are
// FORCE ROW LEVEL SECURITY — which binds the owner too — with policies scoped to
// app.current_org_id and no bypass clause. On managed Postgres, where the owner
// is neither superuser nor BYPASSRLS, the scan therefore saw zero organizations,
// recorded nothing, and returned 0: the same answer a healthy platform gives.
//
// No test that simply runs the scan as the owner-connection can catch this,
// because the local owner-connection role IS a superuser and so bypasses RLS
// outright. Build the production role shape explicitly instead: a principal that
// may execute the function and read both tables, but cannot span organizations.
// It must be refused, not answered with a reassuring zero.
func TestMembershipDiagnosticRefusesACallerThatCannotSpanOrganizations(t *testing.T) {
	role := "diag_narrow_" + strings.ReplaceAll(business.NewIDString(), "-", "")

	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, fmt.Sprintf(
			`CREATE ROLE %s NOSUPERUSER NOBYPASSRLS NOLOGIN`, role))
		defer func() {
			_, _ = conn.Exec(ctx, `RESET ROLE`)
			_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %s`, role))
			_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP ROLE %s`, role))
		}()
		// Grant everything except the ability to span organizations, so the
		// refusal cannot be mistaken for a missing privilege.
		mustExec(t, ctx, conn, fmt.Sprintf(
			`GRANT EXECUTE ON FUNCTION public.record_membership_integrity_findings() TO %s`, role))
		mustExec(t, ctx, conn, fmt.Sprintf(
			`GRANT SELECT ON organizations, organization_members, users TO %s`, role))
		mustExec(t, ctx, conn, fmt.Sprintf(
			`GRANT INSERT, UPDATE, DELETE, SELECT ON membership_integrity_findings TO %s`, role))
		mustExec(t, ctx, conn, fmt.Sprintf(`SET ROLE %s`, role))

		var outstanding int
		err := conn.QueryRow(ctx,
			`SELECT public.record_membership_integrity_findings()`).Scan(&outstanding)
		require.Error(t, err,
			"a caller that cannot span organizations must be refused, not answered with an empty backlog")
		require.Contains(t, err.Error(), "spans every organization")
	})
}

// The deploy path itself: the migration assumes app_control_plane before
// scanning, which only works if the principal migrations run under is a member
// of that role. That is an environment fact, not a code fact, so assert it here
// rather than discovering it during a release.
func TestMigrationOwnerCanAssumeTheControlPlaneToScan(t *testing.T) {
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, `SET ROLE app_control_plane`)
		defer func() { _, _ = conn.Exec(ctx, `RESET ROLE`) }()

		var outstanding int
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT public.record_membership_integrity_findings()`).Scan(&outstanding))
	})
}
