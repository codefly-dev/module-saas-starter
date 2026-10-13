//go:build !pure

package infra_test

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Existing authority fixtures require an acknowledged installation. Drive the
// actual durable queue and outcome projection, as a setup consumer would; no
// fixture writes active directly. These database suites run sequentially.
func acknowledgeInstallation(t *testing.T, i *gen.Installation) {
	t.Helper()
	if i.Status != gen.InstallationStatus_INSTALLATION_STATUS_PENDING {
		return
	}
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	for n := 0; n < 1000; n++ {
		batch, err := worker.Claim(testCtx, &jobsv1.ClaimJobsRequest{Queue: business.InstallationStepQueue, WorkerId: "fixture-ack", Limit: 1, LeaseDuration: durationpb.New(time.Minute)})
		require.NoError(t, err)
		require.Len(t, batch.Jobs, 1, "expected an enqueued installation step")
		job := batch.Jobs[0]
		require.NoError(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: &jobsv1.JobLeaseReference{JobId: job.Id, WorkerId: job.Lease.Owner, LeaseToken: job.Lease.Token}}))
		if job.Attributes["installation_step.id"] == i.Id {
			require.NoError(t, testStore.WithOrgTx(testCtx, i.OrgId, func(ctx context.Context) error {
				current, _, err := testStore.GetInstallation(ctx, i.OrgId, i.Id)
				if err == nil {
					i.Status = current.Status
					i.FailureReason = current.FailureReason
				}
				return err
			}))
			require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, i.Status)
			return
		}
	}
	t.Fatal("installation step not found")
}
