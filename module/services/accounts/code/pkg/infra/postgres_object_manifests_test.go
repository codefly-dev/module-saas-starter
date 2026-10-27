//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// TestPostgresDatasourceSnapshotCountRoundTrips pins the host's record of
// whether an object-storage source has files, which the empty-listing
// invariant reads: unknown until a closing snapshot is recorded, then the count
// that snapshot named — zero included — and the sync time with it; another
// organization reads nothing of it.
func TestPostgresDatasourceSnapshotCountRoundTrips(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	sourceID := seedDatasourceSource(t, orgID)
	last := func(org string) (int, bool, error) {
		var items int
		var known bool
		err := testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			var err error
			items, known, err = testStore.DatasourceSourceLastSnapshot(ctx, orgID, sourceID)
			return err
		})
		return items, known, err
	}
	record := func(items int) {
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			return testStore.RecordDatasourceSnapshotSent(ctx, orgID, sourceID, time.Now().UTC(), items)
		}))
	}

	_, known, err := last(orgID)
	require.NoError(t, err)
	require.False(t, known, "a source that never sent a snapshot has no recorded count")

	record(3)
	items, known, err := last(orgID)
	require.NoError(t, err)
	require.True(t, known)
	require.Equal(t, 3, items)
	var synced *time.Time
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		source, err := testStore.GetDatasourceSource(ctx, orgID, sourceID)
		if err == nil && source != nil {
			synced = source.LastSyncedAt
		}
		return err
	}))
	require.NotNil(t, synced, "recording a snapshot records the sync time")

	record(0)
	items, known, err = last(orgID)
	require.NoError(t, err)
	require.True(t, known)
	require.Equal(t, 0, items, "an empty snapshot is recorded as zero, not as unknown")

	otherOrg := seedOrg(t, seedUser(t))
	_, known, err = last(otherOrg)
	require.False(t, known && err == nil, "another organization must not read a source's snapshot count")
}
