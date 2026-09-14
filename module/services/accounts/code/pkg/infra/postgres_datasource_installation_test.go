//go:build !pure

package infra_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/business"

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

func sourcesForInstallation(t *testing.T, installationID string) []*business.DatasourceSource {
	t.Helper()
	var sources []*business.DatasourceSource
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		sources, err = testStore.ListDatasourceSourcesByGitHubInstallation(ctx, installationID)
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

func degradeSource(t *testing.T, sourceID, reason string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.MarkDatasourceSourceDegraded(ctx, sourceID, reason)
	}))
}

// One GitHub App installation can cover repositories belonging to different
// tenants, and an App-level delivery names no tenant at all, so the lookup that
// routes it has to span organizations — and still return only the sources bound
// to the installation that was delivered.
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

	bindInstallation(t, orgOne, first, delivered)
	bindInstallation(t, orgTwo, second, delivered)
	bindInstallation(t, orgOne, otherInstallation, other)

	found := sourcesForInstallation(t, delivered)
	ids := make([]string, 0, len(found))
	for _, source := range found {
		ids = append(ids, source.ID)
		require.Equal(t, delivered, source.GitHubInstallationID, "the projection must carry the binding back")
	}
	require.ElementsMatch(t, []string{first, second}, ids,
		"exactly the two sources bound to the delivered installation, across both tenants")
	require.NotContains(t, ids, otherInstallation)
	require.NotContains(t, ids, unbound, "a PAT-backed source has no binding and is never routed to")
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
	degradeSource(t, revoked, business.DatasourceReasonInstallationSuspended)
	degradeSource(t, oversized, compilerReason)

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
