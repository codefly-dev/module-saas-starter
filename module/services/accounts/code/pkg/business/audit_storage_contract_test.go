//go:build !pure

package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/business/auditstoragetest"

	"github.com/stretchr/testify/require"
)

// TestPostgresAuditStorageContract runs the AuditStorage contract suite against
// the Postgres implementation, the reference every other storage is held to.
// Entries are appended the way the emitter writes them: inside the
// organization's transaction, or the control plane's for an entry with no
// organization.
func TestPostgresAuditStorageContract(t *testing.T) {
	auditstoragetest.Run(t, auditstoragetest.Harness{
		New: func(t *testing.T) *auditstoragetest.Subject {
			clearData(t)
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				if err := testStore.SyncAuditEventTypes(ctx, business.AuditEventCatalog()); err != nil {
					return err
				}
				return testStore.EnsureAuditPartitions(ctx, 1)
			}))
			storage, err := business.NewPostgresAuditStorage(testStore)
			require.NoError(t, err)
			return &auditstoragetest.Subject{
				Storage: storage,
				Append: func(ctx context.Context, entry business.AuditEntry) error {
					write := func(ctx context.Context) error { return storage.Append(ctx, entry) }
					if entry.OrgID == "" {
						return testStore.WithControlPlane(ctx, write)
					}
					return testStore.WithOrgTx(ctx, entry.OrgID, write)
				},
			}
		},
	})
}
