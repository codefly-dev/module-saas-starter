//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// seedDatasourceSource inserts a minimal github source under the control plane
// (BYPASSRLS) and returns its id. boundary_node_id is NOT NULL and FKs to
// scope_nodes (migration 113), so it mints a collection scope node first. Only
// the NOT NULL columns are set; next_ordinal is left to its column DEFAULT of 1
// so the test also pins that the first allocation for a fresh source is 1.
func seedDatasourceSource(t *testing.T, orgID string) string {
	t.Helper()
	id := business.NewIDString()
	nodeID := business.NewIDString()
	scopePath := strings.ReplaceAll(nodeID, "-", "_")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithControlPlane
		if _, err := tx.Exec(ctx, `
			INSERT INTO scope_nodes (id, org_id, scope_path, kind, label)
			VALUES ($1, $2, $3::ltree, 'collection', 'docs')`,
			nodeID, orgID, scopePath); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO datasource_sources
				(id, org_id, provider, repo, boundary_node_id, credential_secret_ref)
			VALUES ($1, $2, 'github', 'acme/widgets', $3, 'cfs1:vault-transit:ref')`,
			id, orgID, nodeID)
		return err
	}))
	return id
}

// allocateOrdinal draws one ordinal for a source through the real store method,
// under the control-plane transaction the leased compiler uses (the UPDATE needs
// BYPASSRLS since the compiler has no tenant context).
func allocateOrdinal(t *testing.T, sourceID string) int64 {
	t.Helper()
	var ordinal int64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		ordinal, err = testStore.AllocateDatasourceOrdinal(ctx, sourceID)
		return err
	}))
	return ordinal
}

// TestPostgresAllocateDatasourceOrdinalIsPerSourceStrictlyIncreasing pins the
// issue #511 ordinal contract at the real column and UPDATE: a source's ordinals
// start at 1 and strictly increase by one per allocation, and each source has its
// own independent sequence, so a consumer can order one source's payload stream
// and reject a stale or out-of-order replay without another source's traffic
// perturbing it.
func TestPostgresAllocateDatasourceOrdinalIsPerSourceStrictlyIncreasing(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	sourceA := seedDatasourceSource(t, org)
	sourceB := seedDatasourceSource(t, org)

	// Source A: the first allocation is 1 and each subsequent one increases by
	// exactly one, in allocation order.
	require.EqualValues(t, 1, allocateOrdinal(t, sourceA))
	require.EqualValues(t, 2, allocateOrdinal(t, sourceA))
	require.EqualValues(t, 3, allocateOrdinal(t, sourceA))

	// Source B has its own sequence, unaffected by source A's allocations.
	require.EqualValues(t, 1, allocateOrdinal(t, sourceB))
	require.EqualValues(t, 2, allocateOrdinal(t, sourceB))

	// Interleaving the two sources keeps each sequence strictly increasing on its
	// own terms.
	require.EqualValues(t, 4, allocateOrdinal(t, sourceA))
	require.EqualValues(t, 3, allocateOrdinal(t, sourceB))
}
