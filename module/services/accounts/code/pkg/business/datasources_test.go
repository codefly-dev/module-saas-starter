package business_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func githubSource(orgID string) *business.DatasourceSource {
	return &business.DatasourceSource{
		OrgID:            orgID,
		Connector:        business.DatasourceConnectorGitHub,
		DisplayName:      "Docs",
		TargetCollection: "docs",
		Config: business.DatasourceConfig{GitHub: &business.GitHubSourceConfig{
			Repository: "codefly-dev/module-saas-starter",
			Branch:     "main",
			Paths:      []string{"docs/"},
		}},
	}
}

func TestDatasourceSource_CRUDLifecycle(t *testing.T) {
	clearData(t)
	ctx := testCtx

	actorID, orgID := mustUserAndOrg(t, ctx, "ds-owner@test.com", "ds-owner", "Datasource Co")

	created, err := testService.CreateDatasourceSource(ctx, actorID, githubSource(orgID))
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, orgID, created.OrgID)
	require.Equal(t, business.DatasourceConnectorGitHub, created.Connector)
	require.Equal(t, business.DatasourceSourceStatusPending, created.Status)
	require.Equal(t, "docs", created.TargetCollection)
	require.NotNil(t, created.Config.GitHub)
	require.Equal(t, "codefly-dev/module-saas-starter", created.Config.GitHub.Repository)
	require.Equal(t, []string{"docs/"}, created.Config.GitHub.Paths)
	require.False(t, created.CreatedAt.IsZero())

	got, err := testService.GetDatasourceSource(ctx, orgID, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, "codefly-dev/module-saas-starter", got.Config.GitHub.Repository)

	list, next, err := testService.ListDatasourceSources(ctx, orgID, 100, "")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, created.ID, list[0].ID)
	require.Empty(t, next)

	synced, err := testService.RequestDatasourceSync(ctx, actorID, orgID, created.ID)
	require.NoError(t, err)
	require.NotNil(t, synced.LastSyncRequestedAt)

	require.NoError(t, testService.DeleteDatasourceSource(ctx, actorID, orgID, created.ID))
	_, err = testService.GetDatasourceSource(ctx, orgID, created.ID)
	require.Error(t, err)

	// Syncing a source that no longer exists is a clean not-found, never a
	// nil dereference.
	_, err = testService.RequestDatasourceSync(ctx, actorID, orgID, created.ID)
	require.Error(t, err)
}

func TestDatasourceSource_ListPagination(t *testing.T) {
	clearData(t)
	ctx := testCtx

	actorID, orgID := mustUserAndOrg(t, ctx, "ds-page@test.com", "ds-page", "Page Co")

	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		created, err := testService.CreateDatasourceSource(ctx, actorID, githubSource(orgID))
		require.NoError(t, err)
		want[created.ID] = true
	}

	page1, next, err := testService.ListDatasourceSources(ctx, orgID, 2, "")
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotEmpty(t, next, "a full page must yield a continuation token")

	page2, next2, err := testService.ListDatasourceSources(ctx, orgID, 2, next)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Empty(t, next2, "the final short page must not yield a token")

	got := map[string]bool{}
	for _, s := range append(page1, page2...) {
		got[s.ID] = true
	}
	require.Equal(t, want, got, "paging must cover every source exactly once")
}

func TestDatasourceSource_RejectsUnsupportedConnector(t *testing.T) {
	clearData(t)
	ctx := testCtx

	actorID, orgID := mustUserAndOrg(t, ctx, "ds-bad@test.com", "ds-bad", "Bad Co")

	source := githubSource(orgID)
	source.Connector = "gitlab"
	_, err := testService.CreateDatasourceSource(ctx, actorID, source)
	require.Error(t, err)
}

// TestDatasourceSource_TenantIsolation confirms RLS keeps one org's sources
// invisible to another: a cross-tenant id resolves to not-found on read and
// affects zero rows on delete, never leaking or mutating the owner's row.
func TestDatasourceSource_TenantIsolation(t *testing.T) {
	clearData(t)
	ctx := testCtx

	actorA, orgA := mustUserAndOrg(t, ctx, "ds-a@test.com", "ds-a", "Org A")
	actorB, orgB := mustUserAndOrg(t, ctx, "ds-b@test.com", "ds-b", "Org B")

	created, err := testService.CreateDatasourceSource(ctx, actorA, githubSource(orgA))
	require.NoError(t, err)

	_, err = testService.GetDatasourceSource(ctx, orgB, created.ID)
	require.Error(t, err)

	listB, _, err := testService.ListDatasourceSources(ctx, orgB, 100, "")
	require.NoError(t, err)
	require.Empty(t, listB)

	require.Error(t, testService.DeleteDatasourceSource(ctx, actorB, orgB, created.ID))

	// The owner still sees an untouched row.
	stillThere, err := testService.GetDatasourceSource(ctx, orgA, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, stillThere.ID)
}
