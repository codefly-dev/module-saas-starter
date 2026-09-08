package infra_test

import (
	"testing"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestStory_HOST_JOB_001 — "Work is never lost".
//
// As a module, I want a job I claim to be redelivered if I fail or crash, and
// dead-lettered with my reason if I reject it permanently, so that no change is
// lost and no poison job loops.
//
// The two failure modes have to stay distinguishable at the storage layer: a
// crash spends an attempt and returns the work, a permanent rejection ends the
// work on the attempt it was rejected on without spending the remaining budget,
// and only an operator's replay revives it.
func TestStory_HOST_JOB_001(t *testing.T) {
	pool, store := newJobExecutionHarness(t)
	queue := executionQueue("story-host-job-001")
	module, caller := moduleJobSurface(t, store, queue)

	// Given a job leased by a module, through the surface a module actually
	// calls — the guards on that surface are part of the story.
	jobID := insertExecutionJob(t, pool, executionJob{queue: queue, maxAttempts: 5})
	leased := claimModuleJobs(t, module, caller, queue, "module-worker").GetJobs()
	require.Len(t, leased, 1)
	require.Equal(t, jobID.String(), leased[0].GetId())
	require.EqualValues(t, 1, leased[0].GetAttemptCount())

	// When the module crashes before acknowledging, its lease runs out.
	_, err := pool.Exec(testCtx, `
		UPDATE job_messages
		SET lease_expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, jobID)
	require.NoError(t, err)

	// Then the job is redelivered, and the crashed worker can no longer act on it.
	redelivered := claimModuleJobs(t, module, caller, queue, "module-worker-restarted").GetJobs()
	require.Len(t, redelivered, 1, "an expired lease returns the work to the queue")
	require.Equal(t, jobID.String(), redelivered[0].GetId())
	require.EqualValues(t, 2, redelivered[0].GetAttemptCount())
	require.ErrorIs(t,
		store.Complete(testCtx, completeExecutionRequest(leased[0])),
		jobs.ErrLeaseLost,
	)

	// When the module rejects the job as permanent. This is the module-facing
	// nack, not the storage primitive underneath it: a non-retryable nack that
	// was quietly routed into the retry path would still dead-letter the job
	// eventually, and only this call distinguishes the two.
	require.NoError(t, module.ModuleNackJob(
		testCtx,
		caller,
		executionLease(redelivered[0]),
		&jobsv1.JobFailure{
			Code:    "schema.unsupported",
			Message: "schema_version 1 unsupported",
		},
		false,
		nil,
	))

	// Then it is dead-lettered on that attempt with the reason, without looping
	// through the attempts the budget still had left.
	detail, err := store.GetJob(testCtx, &jobsv1.GetJobRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_DEAD_LETTER, detail.GetJob().GetState())
	require.EqualValues(t, 2, detail.GetJob().GetAttemptCount(),
		"a permanent rejection ends the job on its own attempt, not on the budget",
	)
	require.Equal(t, "schema.unsupported", detail.GetJob().GetLastFailure().GetCode())
	require.Equal(t, "schema_version 1 unsupported", detail.GetJob().GetLastFailure().GetMessage())
	require.Empty(t,
		claimModuleJobs(t, module, caller, queue, "module-worker-poison").GetJobs(),
		"a dead-lettered job never loops back to a worker",
	)

	// And an operator can replay it.
	replayed, err := store.ReplayJob(testCtx, &jobsv1.ReplayJobRequest{
		SourceJobId:    jobID.String(),
		IdempotencyKey: "operator-replay-" + uuid.NewString(),
	})
	require.NoError(t, err)
	require.Equal(t,
		jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
		replayed.GetDisposition(),
	)
	revived := claimModuleJobs(t, module, caller, queue, "module-worker-replay").GetJobs()
	require.Len(t, revived, 1)
	require.Equal(t, replayed.GetJobId(), revived[0].GetId())
	require.NoError(t, module.ModuleAckJob(testCtx, caller, &jobsv1.CompleteJobRequest{
		Lease: executionLease(revived[0]),
	}))

	// The original stays dead-lettered: a replay is new work, not a resurrection.
	source, err := store.GetJob(testCtx, &jobsv1.GetJobRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_DEAD_LETTER, source.GetJob().GetState())
	require.WithinDuration(t, time.Now(), source.GetJob().GetDeadLetteredAt().AsTime(), time.Hour)
}

// moduleJobSurface wires the module-facing capability surface over the real job
// store, granting the caller principal exactly the queue under test.
func moduleJobSurface(
	t *testing.T,
	store *infra.PostgresJobStore,
	queue string,
) (*business.Service, business.ModuleCaller) {
	t.Helper()
	service, err := business.NewService(nil)
	require.NoError(t, err)
	principal := uuid.NewString()
	// Claiming reads across tenants, so the surface requires a cross-tenant
	// grant for it — a claim without one is refused before the store is reached.
	service.SetModuleCapabilities(store, store, business.ModulePrincipalRegistry{
		principal: {Queues: []string{queue}, CrossTenant: true},
	})
	return service, business.ModuleCaller{PrincipalID: principal}
}

func claimModuleJobs(
	t *testing.T,
	service *business.Service,
	caller business.ModuleCaller,
	queue, worker string,
) *jobsv1.ClaimJobsResponse {
	t.Helper()
	response, err := service.ModuleClaimJobs(testCtx, caller, &jobsv1.ClaimJobsRequest{
		Queue: queue, WorkerId: worker, Limit: 1, LeaseDuration: durationpb.New(time.Minute),
	})
	require.NoError(t, err)
	return response
}
