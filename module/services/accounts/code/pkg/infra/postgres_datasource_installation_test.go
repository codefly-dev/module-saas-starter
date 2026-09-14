//go:build !pure

package infra_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Installation ids are minted per test rather than fixed. The lookup under test
// spans tenants and the suite's database is a long-lived bind mount, so a shared
// constant would also return the rows an earlier run left behind.
var (
	installationRun      = time.Now().UnixNano()
	installationSequence atomic.Int64
)

func uniqueInstallationID() string {
	return strconv.FormatInt(installationRun+installationSequence.Add(1), 10)
}

// bindInstallation stamps a source's routing index through the real store
// method, under the control-plane transaction the reconciler uses.
func bindInstallation(t *testing.T, orgID, sourceID, installationID string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.SetDatasourceSourceGitHubInstallation(ctx, orgID, sourceID, installationID)
	}))
}

// setSourceState writes a status directly, standing in for the operator pause or
// the compiler degrade that this path must not overwrite.
func setSourceState(t *testing.T, sourceID, status, reason string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithControlPlane
		_, err := tx.Exec(ctx, `UPDATE datasource_sources SET status=$2, status_reason=NULLIF($3,'') WHERE id=$1`,
			sourceID, status, reason)
		return err
	}))
}

func setSourceProvider(t *testing.T, sourceID, provider string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithControlPlane
		_, err := tx.Exec(ctx, `UPDATE datasource_sources SET provider=$2 WHERE id=$1`, sourceID, provider)
		return err
	}))
}

func sourcesForInstallation(t *testing.T, installationID, afterID string, limit int) []*business.DatasourceSource {
	t.Helper()
	var sources []*business.DatasourceSource
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		sources, err = testStore.ListDatasourceSourcesByGitHubInstallation(ctx, installationID, afterID, limit)
		return err
	}))
	return sources
}

func installationSourceByID(t *testing.T, sourceID string) *business.DatasourceSource {
	t.Helper()
	var source *business.DatasourceSource
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		source, err = testStore.GetDatasourceSourceByID(ctx, sourceID)
		return err
	}))
	require.NotNil(t, source)
	return source
}

func parkForInstallation(t *testing.T, sourceID, reason string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.MarkDatasourceSourceInstallationDegraded(ctx, sourceID, reason)
	}))
}

// One GitHub App installation can cover repositories belonging to different
// tenants, and an App-level delivery names no tenant at all, so the lookup that
// routes it has to span organizations — and still return only the GitHub sources
// bound to the installation that was delivered.
func TestPostgresListDatasourceSourcesByGitHubInstallationSpansTenants(t *testing.T) {
	delivered := uniqueInstallationID()
	other := uniqueInstallationID()

	owner := seedUser(t)
	orgOne := seedOrg(t, owner)
	orgTwo := seedOrg(t, owner)

	first := seedDatasourceSource(t, orgOne)
	second := seedDatasourceSource(t, orgTwo)
	otherInstallation := seedDatasourceSource(t, orgOne)
	unbound := seedDatasourceSource(t, orgOne)
	notGitHub := seedDatasourceSource(t, orgOne)

	bindInstallation(t, orgOne, first, delivered)
	bindInstallation(t, orgTwo, second, delivered)
	bindInstallation(t, orgOne, otherInstallation, other)
	bindInstallation(t, orgOne, notGitHub, delivered)
	setSourceProvider(t, notGitHub, business.DatasourceProviderAPI)

	found := sourcesForInstallation(t, delivered, "", 100)
	ids := make([]string, 0, len(found))
	for _, source := range found {
		ids = append(ids, source.ID)
		require.Equal(t, delivered, source.GitHubInstallationID, "the projection must carry the binding back")
	}
	require.ElementsMatch(t, []string{first, second}, ids,
		"exactly the two GitHub sources bound to the delivered installation, across both tenants")
	require.NotContains(t, ids, otherInstallation)
	require.NotContains(t, ids, unbound, "a PAT-backed source has no binding and is never routed to")
	require.NotContains(t, ids, notGitHub, "another provider must never be parked for a GitHub reason")
}

// An installation can cover more repositories than one read should hold, so the
// listing pages by id and a caller walks it with the last id it saw.
func TestPostgresListDatasourceSourcesByGitHubInstallationPages(t *testing.T) {
	installation := uniqueInstallationID()
	owner := seedUser(t)
	org := seedOrg(t, owner)

	seeded := map[string]bool{}
	for range 3 {
		id := seedDatasourceSource(t, org)
		bindInstallation(t, org, id, installation)
		seeded[id] = true
	}

	walked := map[string]bool{}
	after := ""
	for range 5 {
		page := sourcesForInstallation(t, installation, after, 2)
		if len(page) == 0 {
			break
		}
		require.LessOrEqual(t, len(page), 2, "a page never exceeds the limit")
		for _, source := range page {
			require.Greater(t, source.ID, after, "each page starts strictly after the cursor")
			walked[source.ID] = true
			after = source.ID
		}
	}
	require.Equal(t, seeded, walked, "paging must visit every source exactly once")
}

// The park re-tests the status inside the UPDATE. The reconciler decides what to
// park from a read taken before it called GitHub, so an operator pause or a
// compiler degrade landing in between must survive — otherwise a webhook erases
// an operator's deliberate stop, and a later re-check flips that source back to
// active.
func TestPostgresMarkDatasourceSourceInstallationDegradedParksOnlyActive(t *testing.T) {
	const compilerReason = "snapshot manifest is 1048576 bytes, over the 983040-byte ingest limit"
	owner := seedUser(t)
	org := seedOrg(t, owner)
	paused := seedDatasourceSource(t, org)
	compilerParked := seedDatasourceSource(t, org)
	active := seedDatasourceSource(t, org)

	setSourceState(t, paused, business.DatasourceStatusPaused, "")
	setSourceState(t, compilerParked, business.DatasourceStatusDegraded, compilerReason)

	for _, id := range []string{paused, compilerParked, active} {
		parkForInstallation(t, id, business.DatasourceReasonInstallationSuspended)
	}

	stillPaused := installationSourceByID(t, paused)
	require.Equal(t, business.DatasourceStatusPaused, stillPaused.Status, "an operator pause outranks a webhook")
	require.Empty(t, stillPaused.StatusReason)

	stillCompiler := installationSourceByID(t, compilerParked)
	require.Equal(t, compilerReason, stillCompiler.StatusReason, "the compiler's reason must not be overwritten")

	parked := installationSourceByID(t, active)
	require.Equal(t, business.DatasourceStatusDegraded, parked.Status)
	require.Equal(t, business.DatasourceReasonInstallationSuspended, parked.StatusReason)
	require.Nil(t, parked.NextReconcileAt, "a parked source leaves the reconcile sweep")
}

// A parked source is out of the reconcile sweep, so the recheck sweep is the
// only pull-side route back to active. It must find exactly the installations
// that still hold a source this path parked.
func TestPostgresListGitHubInstallationsPendingRecheck(t *testing.T) {
	const compilerReason = "snapshot manifest is 1048576 bytes, over the 983040-byte ingest limit"
	parkedInstallation := uniqueInstallationID()
	healthyInstallation := uniqueInstallationID()
	compilerInstallation := uniqueInstallationID()

	owner := seedUser(t)
	org := seedOrg(t, owner)
	parked := seedDatasourceSource(t, org)
	healthy := seedDatasourceSource(t, org)
	compilerParked := seedDatasourceSource(t, org)

	bindInstallation(t, org, parked, parkedInstallation)
	bindInstallation(t, org, healthy, healthyInstallation)
	bindInstallation(t, org, compilerParked, compilerInstallation)
	parkForInstallation(t, parked, business.DatasourceReasonInstallationSuspended)
	setSourceState(t, compilerParked, business.DatasourceStatusDegraded, compilerReason)

	var pending []string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		pending, err = testStore.ListGitHubInstallationsPendingRecheck(ctx, []string{
			business.DatasourceReasonInstallationSuspended,
			business.DatasourceReasonInstallationRepositoryUnavailable,
		}, 100)
		return err
	}))

	require.Contains(t, pending, parkedInstallation)
	require.NotContains(t, pending, healthyInstallation, "an active source needs no re-check")
	require.NotContains(t, pending, compilerInstallation, "another path's degrade is not this sweep's to lift")
}

// Restoring App access revives only what the App-level reconciler parked. A
// source the change-set compiler degraded for a fault of its own keeps both its
// status and its reason, so a webhook cannot clear an unrelated problem.
func TestPostgresClearDatasourceSourceInstallationDegradedMatchesTheReason(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	revoked := seedDatasourceSource(t, org)
	oversized := seedDatasourceSource(t, org)

	const compilerReason = "snapshot manifest is 1048576 bytes, over the 983040-byte ingest limit"
	reasons := []string{
		business.DatasourceReasonInstallationSuspended,
		business.DatasourceReasonInstallationRepositoryUnavailable,
	}
	parkForInstallation(t, revoked, business.DatasourceReasonInstallationSuspended)
	setSourceState(t, oversized, business.DatasourceStatusDegraded, compilerReason)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		if err := testStore.ClearDatasourceSourceInstallationDegraded(ctx, revoked, reasons); err != nil {
			return err
		}
		return testStore.ClearDatasourceSourceInstallationDegraded(ctx, oversized, reasons)
	}))

	restored := installationSourceByID(t, revoked)
	require.Equal(t, business.DatasourceStatusActive, restored.Status)
	require.Empty(t, restored.StatusReason)
	require.NotNil(t, restored.NextReconcileAt, "a restored source rejoins the reconcile sweep")

	untouched := installationSourceByID(t, oversized)
	require.Equal(t, business.DatasourceStatusDegraded, untouched.Status)
	require.Equal(t, compilerReason, untouched.StatusReason)
}

// The binding is scoped to its owning organization: another tenant's id must
// not be able to re-point a source it does not own.
func TestPostgresSetDatasourceSourceGitHubInstallationIsOrgScoped(t *testing.T) {
	installation := uniqueInstallationID()

	owner := seedUser(t)
	orgOne := seedOrg(t, owner)
	orgTwo := seedOrg(t, owner)
	source := seedDatasourceSource(t, orgOne)

	bindInstallation(t, orgTwo, source, installation)
	require.Empty(t, installationSourceByID(t, source).GitHubInstallationID)

	bindInstallation(t, orgOne, source, installation)
	require.Equal(t, installation, installationSourceByID(t, source).GitHubInstallationID)
}
