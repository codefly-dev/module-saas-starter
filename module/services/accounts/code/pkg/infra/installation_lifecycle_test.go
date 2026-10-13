//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

func claimInstallationFor(t *testing.T, worker *infra.PostgresJobStore, id string) *jobsv1.JobEnvelope {
	t.Helper()
	for n := 0; n < 1000; n++ {
		out, err := worker.Claim(testCtx, &jobsv1.ClaimJobsRequest{Queue: business.InstallationStepQueue, WorkerId: "example-install-consumer", Limit: 1, LeaseDuration: durationpb.New(time.Minute)})
		require.NoError(t, err)
		require.Len(t, out.Jobs, 1)
		job := out.Jobs[0]
		if job.Attributes["installation_step.id"] == id {
			return job
		}
		require.NoError(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: installationLease(job)}))
	}
	t.Fatal("installation job not found")
	return nil
}
func installationLease(job *jobsv1.JobEnvelope) *jobsv1.JobLeaseReference {
	return &jobsv1.JobLeaseReference{JobId: job.Id, WorkerId: job.Lease.Owner, LeaseToken: job.Lease.Token}
}
func readLifecycle(t *testing.T, i dynamicInstall) (*gen.Installation, uint64, int) {
	t.Helper()
	var current *gen.Installation
	var revision uint64
	var attempts int
	require.NoError(t, testStore.WithOrgTx(testCtx, i.org, func(ctx context.Context) error {
		var err error
		current, _, err = testStore.GetInstallation(ctx, i.org, i.installation.Id)
		if err != nil {
			return err
		}
		summaries, err := testStore.ListInstallations(ctx, i.org, current.Status, "", 100)
		if err != nil {
			return err
		}
		require.Len(t, summaries, 1)
		require.Equal(t, current.Status, summaries[0].Installation.Status)
		require.Equal(t, current.FailureReason, summaries[0].Installation.FailureReason)
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT revision,install_attempts FROM installations WHERE id=$1 AND org_id=$2`, i.installation.Id, i.org).Scan(&revision, &attempts)
	}))
	return current, revision, attempts
}

// HOST-INST-002..005: actual queue leases, pending-only mint admission, atomic
// refusal/ack projection and a manual retry at the same consent revision.
func TestInstallationLifecycleRefusalRetryAndAcknowledgement(t *testing.T) {
	i := newDynamicInstall(t, "", "", "", true)
	current, revision, attempts := readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, current.Status)
	require.Equal(t, uint64(1), revision)
	require.Zero(t, attempts)
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetModuleCapabilities(testStore, worker, nil)
	rows, _, err := svc.ListOrganizationInstallations(testCtx, i.org, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, rows[0].Summary.Installation.Status)
	require.Empty(t, rows[0].Exposure.GrantedTeams)
	job := claimInstallationFor(t, worker, i.installation.Id)
	lease := installationLease(job)
	_, err = testStore.ResolveInstallationAuthority(testCtx, i.org, i.installation.Id, nil)
	require.Error(t, err)
	require.NoError(t, testStore.WithInstallationStepAuthority(testCtx, lease, i.envelope, func(_ context.Context, a *business.InstallationStepAuthority) error {
		require.Equal(t, i.installation.Id, a.InstallationID)
		require.Equal(t, i.org, a.OrganizationID)
		require.Equal(t, i.installation.AgentPrincipalId, a.Facts.Actor.ID)
		require.Equal(t, i.installation.RootScopeNodeId, a.RootID)
		return nil
	}))
	wrong := &jobsv1.JobLeaseReference{JobId: lease.JobId, WorkerId: lease.WorkerId, LeaseToken: business.NewIDString()}
	require.Error(t, testStore.WithInstallationStepAuthority(testCtx, wrong, i.envelope, func(context.Context, *business.InstallationStepAuthority) error {
		t.Fatal("wrong lease minted")
		return nil
	}))
	require.NoError(t, svc.FinishInstallationStep(testCtx, lease, job.AttemptCount, 409, "Example refusal"))
	current, revision, attempts = readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_FAILED, current.Status)
	require.Equal(t, "Example refusal", current.FailureReason)
	require.Equal(t, 1, attempts)
	err = testStore.WithOrgTx(testCtx, i.org, func(ctx context.Context) error {
		_, err := testStore.InstallSolution(ctx, &business.InstallSolutionParams{OrgID: i.org, TargetID: i.target, OwnerPrincipalID: i.owner})
		return err
	})
	require.ErrorContains(t, err, "already exists")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		retried, rev, err := testStore.RetryInstallation(ctx, i.org, i.installation.Id)
		if err == nil {
			require.Equal(t, revision, rev)
			require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, retried.Status)
			require.Empty(t, retried.FailureReason)
			var role, org string
			err = storetx.Tx(ctx).QueryRow(ctx, `SELECT current_user, coalesce(current_setting('app.current_org_id',true),'')`).Scan(&role, &org)
			require.Equal(t, "app_control_plane", role, "enqueue restores the enclosing transaction's role")
			require.Empty(t, org, "enqueue does not leave tenant state on the control plane")
		}
		return err
	}))
	require.Error(t, svc.FinishInstallationStep(testCtx, lease, job.AttemptCount, 204, ""), "old lease cannot acknowledge a retry")
	job = claimInstallationFor(t, worker, i.installation.Id)
	require.NotEqual(t, lease.JobId, job.Id)
	require.NoError(t, svc.FinishInstallationStep(testCtx, installationLease(job), job.AttemptCount, 204, ""))
	current, revision, attempts = readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, current.Status)
	require.Equal(t, uint64(1), revision)
	// An active narrowing gets a fresh revision/run without losing availability.
	scope := business.FullInstallationScope(i.grant)
	scope.Queues = nil
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.NarrowInstallationScope(ctx, i.org, i.installation.Id, revision, scope, business.ModulePrincipalRegistry{i.principal: i.grant}, i.envelope)
	}))
	current, revision, _ = readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, current.Status)
	require.Equal(t, uint64(2), revision)
	job = claimInstallationFor(t, worker, i.installation.Id)
	require.Equal(t, "2", job.Attributes["installation_step.revision"])
	require.NoError(t, svc.FinishInstallationStep(testCtx, installationLease(job), job.AttemptCount, 409, "Replay refused"))
	current, _, _ = readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, current.Status)
	require.Empty(t, current.FailureReason)
}

func TestInstallationStepCancellationAndAtomicEnqueue(t *testing.T) {
	for _, state := range []string{"pending", "processing", "retrying"} {
		t.Run(state, func(t *testing.T) {
			i := newDynamicInstall(t, "", "", "", true)
			pool, err := infra.NewJobWorkerPool(testCtx)
			require.NoError(t, err)
			defer pool.Close()
			worker := infra.NewPostgresJobStore(pool)
			svc, err := business.NewService(testStore)
			require.NoError(t, err)
			svc.SetModuleCapabilities(testStore, worker, nil)
			var job *jobsv1.JobEnvelope
			if state != "pending" {
				job = claimInstallationFor(t, worker, i.installation.Id)
				if state == "retrying" {
					require.NoError(t, svc.FinishInstallationStep(testCtx, installationLease(job), job.AttemptCount, 503, ""))
				}
			}
			other := newDynamicInstall(t, i.target, i.binding, i.principal, true)
			var jobID string
			require.NoError(t, pool.QueryRow(testCtx, `SELECT id::text FROM job_messages
			 WHERE attributes->>'codefly.installation_id'=$1 AND attributes->>'codefly.organization_id'=$2`, i.installation.Id, i.org).Scan(&jobID))
			rollback := errors.New("roll back the lifecycle transaction")
			err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				if err := testStore.EnqueueInstallationStep(ctx, i.org, i.installation.Id, "rolled-back"); err != nil {
					return err
				}
				return rollback
			})
			require.ErrorIs(t, err, rollback)
			var count int
			require.NoError(t, pool.QueryRow(testCtx, `SELECT count(*) FROM job_messages
			 WHERE attributes->>'codefly.installation_id'=$1`, i.installation.Id).Scan(&count))
			require.Equal(t, 1, count, "rolled-back enqueue leaves no orphan job")
			var current, foreign bool
			require.NoError(t, pool.QueryRow(testCtx, `SELECT public.installation_step_job_current(id,attributes),
			 public.installation_step_job_current(id,attributes || jsonb_build_object('codefly.organization_id',$2::text))
			 FROM job_messages WHERE id=$1`, jobID, other.org).Scan(&current, &foreign))
			require.True(t, current, "rollback preserves the exact current run")
			require.False(t, foreign, "setup authority is bound to the installation's organization")
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				_, _, err := testStore.UninstallSolution(ctx, i.org, i.installation.Id)
				return err
			}))
			var got string
			require.NoError(t, pool.QueryRow(testCtx, `SELECT state FROM job_messages WHERE id=$1`, jobID).Scan(&got))
			require.Equal(t, "canceled", got)
			require.NoError(t, pool.QueryRow(testCtx, `SELECT state FROM job_messages
			 WHERE attributes->>'codefly.installation_id'=$1`, other.installation.Id).Scan(&got))
			require.Equal(t, "pending", got, "revocation preserves another organization's setup")
			if job != nil {
				require.Error(t, svc.FinishInstallationStep(testCtx, installationLease(job), job.AttemptCount, 204, ""))
			}
		})
	}
}

func TestInstallationStepNarrowingFencesActiveReplay(t *testing.T) {
	i := newDynamicInstall(t, "", "", "")
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	narrow := func(scope business.InstallationScope, revision uint64) {
		t.Helper()
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.NarrowInstallationScope(ctx, i.org, i.installation.Id, revision, scope,
				business.ModulePrincipalRegistry{i.principal: i.grant}, i.envelope)
		}))
	}
	scope := business.FullInstallationScope(i.grant)
	scope.Queues = nil
	narrow(scope, 1)
	old := claimInstallationFor(t, worker, i.installation.Id)
	scope.Resources = nil
	narrow(scope, 2)
	var state string
	require.NoError(t, pool.QueryRow(testCtx, `SELECT state FROM job_messages WHERE id=$1`, old.Id).Scan(&state))
	require.Equal(t, "canceled", state)
	require.Error(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: installationLease(old)}))
	job := claimInstallationFor(t, worker, i.installation.Id)
	require.NotEqual(t, old.Id, job.Id)
	require.Equal(t, "3", job.Attributes["codefly.installation_revision"])
	require.NoError(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: installationLease(job)}))
	current, revision, _ := readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, current.Status)
	require.Equal(t, uint64(3), revision)
}

// HOST-INST-004: retryable outages exhaust the declared budget, with a fixed
// public reason. The test advances availability, never sleeps through backoff.
func TestInstallationLifecycleExhaustion(t *testing.T) {
	i := newDynamicInstall(t, "", "", "", true)
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetModuleCapabilities(testStore, worker, nil)
	for attempt := 1; attempt <= business.InstallationStepMaxAttempts; attempt++ {
		job := claimInstallationFor(t, worker, i.installation.Id)
		require.Equal(t, uint32(attempt), job.AttemptCount)
		require.NoError(t, svc.FinishInstallationStep(testCtx, installationLease(job), job.AttemptCount, 503, "private diagnostic"))
		if attempt < business.InstallationStepMaxAttempts {
			_, err := pool.Exec(testCtx, `UPDATE job_messages SET available_at=clock_timestamp() WHERE id=$1 AND state='retrying'`, job.Id)
			require.NoError(t, err)
		}
	}
	current, revision, attempts := readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_FAILED, current.Status)
	require.Equal(t, uint64(1), revision)
	require.Equal(t, business.InstallationStepMaxAttempts, attempts)
	require.Equal(t, business.InstallationStepFailure, current.FailureReason)
}

// HOST-INST-004: losing the final worker is an exhausted attempt too. The
// common queue's recovery must project failure without a finish callback.
func TestInstallationLifecycleFinalLeaseExpiry(t *testing.T) {
	i := newDynamicInstall(t, "", "", "", true)
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	for attempt := 1; attempt <= business.InstallationStepMaxAttempts; attempt++ {
		job := claimInstallationFor(t, worker, i.installation.Id)
		require.Equal(t, uint32(attempt), job.AttemptCount)
		_, err := pool.Exec(testCtx, `UPDATE job_messages SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, job.Id)
		require.NoError(t, err)
	}
	// A claim pass recovers the exhausted lease even though it cannot claim it.
	_, err = worker.Claim(testCtx, &jobsv1.ClaimJobsRequest{Queue: business.InstallationStepQueue,
		WorkerId: "example-recovery", Limit: 1, LeaseDuration: durationpb.New(time.Minute)})
	require.NoError(t, err)
	current, revision, attempts := readLifecycle(t, i)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_FAILED, current.Status)
	require.Equal(t, uint64(1), revision)
	require.Equal(t, business.InstallationStepMaxAttempts, attempts)
	require.Equal(t, business.InstallationStepFailure, current.FailureReason)
}
