//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// Exercise real persistence, not a config JSON round-trip in memory. The same
// source is reloaded by the tenant reader and the background compiler.
func TestPostgresDatasourceFileFilterPersistsAcrossReaders(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	otherOrg := seedOrg(t, owner)
	legacyID := seedDatasourceSource(t, org)
	legacy := installationSourceByID(t, legacyID)
	require.Empty(t, legacy.FileExtensions, "legacy NULL config must mean all types")

	source := *legacy
	source.ID = business.NewIDString()
	source.FileExtensions = []string{".md", ".mdx"}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.InsertDatasourceSource(ctx, &source)
	}))
	// A separate transaction must reconstruct the persisted filter.
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		got, err := testStore.GetDatasourceSource(ctx, org, source.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, source.FileExtensions, got.FileExtensions)
		all, err := testStore.ListDatasourceSources(ctx, org)
		require.NoError(t, err)
		found := false
		for _, item := range all {
			if item.ID == source.ID {
				found = true
				require.Equal(t, source.FileExtensions, item.FileExtensions)
			}
		}
		require.True(t, found)
		return nil
	}))
	require.Equal(t, source.FileExtensions, installationSourceByID(t, source.ID).FileExtensions)
	require.NoError(t, testStore.WithOrgTx(testCtx, otherOrg, func(ctx context.Context) error {
		got, err := testStore.GetDatasourceSource(ctx, otherOrg, source.ID)
		require.NoError(t, err)
		require.Nil(t, got, "the filter does not relax source tenant isolation")
		return nil
	}))
}
