//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"accounts/pkg/datasource/objectstore"

	"github.com/stretchr/testify/require"
)

func manifestVersion(i int) string {
	return fmt.Sprintf("%016x.%064x", 0xabc, i)
}

// TestPostgresDatasourceObjectManifestsRetainTheNewest pins the retained
// listings an object-storage source diffs against: a listing round-trips under
// its version, a source keeps only its newest few, a re-put of a version makes
// it newest again, and another organization reads none of them.
func TestPostgresDatasourceObjectManifestsRetainTheNewest(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	sourceID := seedDatasourceSource(t, orgID)
	listing := []objectstore.Item{{Key: "kb/a.md", ETag: `"e1"`, Size: 5}, {Key: "kb/b.md", ETag: `"e2"`, Size: 7}}

	put := func(i int) {
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			return testStore.PutDatasourceObjectManifest(ctx, orgID, sourceID, manifestVersion(i), listing)
		}))
	}
	get := func(org string, i int) ([]objectstore.Item, bool) {
		var items []objectstore.Item
		var ok bool
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			var err error
			items, ok, err = testStore.GetDatasourceObjectManifest(ctx, orgID, sourceID, manifestVersion(i))
			return err
		}))
		return items, ok
	}

	put(0)
	items, ok := get(orgID, 0)
	require.True(t, ok)
	require.Equal(t, listing, items)

	for i := 1; i <= 3; i++ {
		put(i)
	}
	put(0) // re-listed: newest again, so version 1 is now the oldest
	put(4)
	_, ok = get(orgID, 1)
	require.False(t, ok, "the oldest listing past the retained count is pruned")
	for _, i := range []int{0, 2, 3, 4} {
		_, ok = get(orgID, i)
		require.True(t, ok, "listing %d is among the newest and must be retained", i)
	}

	otherOrg := seedOrg(t, seedUser(t))
	_, ok = get(otherOrg, 4)
	require.False(t, ok, "another organization must not read a source's listings")
}

// TestPostgresDatasourceObjectManifestRefusesAMalformedVersion: the version is
// the connector's digest, never a free string.
func TestPostgresDatasourceObjectManifestRefusesAMalformedVersion(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	sourceID := seedDatasourceSource(t, orgID)
	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.PutDatasourceObjectManifest(ctx, orgID, sourceID, "not-a-version", nil)
	})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "check"), "want the version CHECK to refuse it, got %v", err)
}
