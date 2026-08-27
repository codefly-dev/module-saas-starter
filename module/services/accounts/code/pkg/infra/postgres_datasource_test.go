package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// These tests rely on TestMain in postgres_webhooks_test.go to set up
// testStore + testCtx. NEVER mock per saas-starter rule.

func newTestDatasource(orgID string) *business.Datasource {
	return &business.Datasource{
		ID:                  business.NewIDString(),
		OrgID:               orgID,
		Kind:                business.DatasourceKindGitHub,
		Repo:                "octocat/hello-world",
		Paths:               []string{"docs", "README.md"},
		Collection:          "wiki",
		CredentialSecretRef: "cfs1:vault-transit:abc",
		SyncStatus:          business.DatasourceSyncStatusIdle,
	}
}

func TestCreateAndListDatasources(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	ds := newTestDatasource(orgID)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateDatasource(ctx, ds))
		list, err := testStore.ListDatasources(ctx, orgID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		got := list[0]
		require.Equal(t, ds.ID, got.ID)
		require.Equal(t, ds.Repo, got.Repo)
		require.Equal(t, ds.Paths, got.Paths)
		require.Equal(t, ds.Collection, got.Collection)
		require.Equal(t, business.DatasourceSyncStatusIdle, got.SyncStatus)
		require.True(t, got.LastSyncRequestedAt.IsZero())
		require.False(t, got.CreatedAt.IsZero())
		return nil
	}))
}

func TestMarkDatasourceSyncRequested(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	ds := newTestDatasource(orgID)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateDatasource(ctx, ds))

		updated, err := testStore.MarkDatasourceSyncRequested(ctx, ds.ID)
		require.NoError(t, err)
		require.NotNil(t, updated)
		require.Equal(t, business.DatasourceSyncStatusPending, updated.SyncStatus)
		require.False(t, updated.LastSyncRequestedAt.IsZero())

		// An unknown id resolves to not-found rather than an error.
		missing, err := testStore.MarkDatasourceSyncRequested(ctx, business.NewIDString())
		require.NoError(t, err)
		require.Nil(t, missing)
		return nil
	}))
}

// TestDatasourceTenantIsolation proves the migration's RLS confines a source to
// its owning org: another org neither lists nor syncs it.
func TestDatasourceTenantIsolation(t *testing.T) {
	ownerA := seedUser(t)
	orgA := seedOrg(t, ownerA)
	ownerB := seedUser(t)
	orgB := seedOrg(t, ownerB)

	ds := newTestDatasource(orgA)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		return testStore.CreateDatasource(ctx, ds)
	}))

	require.NoError(t, testStore.WithOrgTx(testCtx, orgB, func(ctx context.Context) error {
		list, err := testStore.ListDatasources(ctx, orgB)
		require.NoError(t, err)
		require.Empty(t, list)

		blocked, err := testStore.MarkDatasourceSyncRequested(ctx, ds.ID)
		require.NoError(t, err)
		require.Nil(t, blocked)
		return nil
	}))

	// The source is untouched from its own org's view.
	require.NoError(t, testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		list, err := testStore.ListDatasources(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, business.DatasourceSyncStatusIdle, list[0].SyncStatus)
		return nil
	}))
}
