package infra_test

import (
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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

	// Given a job leased by a module.
	jobID := insertExecutionJob(t, pool, executionJob{queue: queue, maxAttempts: 5})
	leased := claimExecutionJobs(t, store, queue, "module-worker", 1).GetJobs()
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
	redelivered := claimExecutionJobs(t, store, queue, "module-worker-restarted", 1).GetJobs()
	require.Len(t, redelivered, 1, "an expired lease returns the work to the queue")
	require.Equal(t, jobID.String(), redelivered[0].GetId())
	require.EqualValues(t, 2, redelivered[0].GetAttemptCount())
	require.ErrorIs(t,
		store.Complete(testCtx, completeExecutionRequest(leased[0])),
		jobs.ErrLeaseLost,
	)

	// When the module rejects the job as permanent.
	require.NoError(t, store.DeadLetter(testCtx, &jobsv1.DeadLetterJobRequest{
		Lease: executionLease(redelivered[0]),
		Failure: &jobsv1.JobFailure{
			Code:    "schema.unsupported",
			Message: "schema_version 1 unsupported",
		},
	}))

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
		claimExecutionJobs(t, store, queue, "module-worker-poison", 1).GetJobs(),
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
	revived := claimExecutionJobs(t, store, queue, "module-worker-replay", 1).GetJobs()
	require.Len(t, revived, 1)
	require.Equal(t, replayed.GetJobId(), revived[0].GetId())
	require.NoError(t, store.Complete(testCtx, completeExecutionRequest(revived[0])))

	// The original stays dead-lettered: a replay is new work, not a resurrection.
	source, err := store.GetJob(testCtx, &jobsv1.GetJobRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_DEAD_LETTER, source.GetJob().GetState())
	require.WithinDuration(t, time.Now(), source.GetJob().GetDeadLetteredAt().AsTime(), time.Hour)
}
