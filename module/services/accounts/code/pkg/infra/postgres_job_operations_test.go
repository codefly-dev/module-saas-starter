package infra_test

import (
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPostgresJobOperationsArePayloadFreeAndReplayIsIdempotent(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	queue := "ops." + uuid.NewString()

	deadSource := enqueueOperationsJob(t, store, queue, nil)
	claimed, err := store.Claim(testCtx, &jobsv1.ClaimJobsRequest{
		Queue: queue, WorkerId: "operations-test", Limit: 1,
		LeaseDuration: durationpb.New(time.Minute),
	})
	require.NoError(t, err)
	require.Len(t, claimed.GetJobs(), 1)
	require.NoError(t, store.DeadLetter(testCtx, &jobsv1.DeadLetterJobRequest{
		Lease:   leaseReference(claimed.GetJobs()[0]),
		Failure: &jobsv1.JobFailure{Code: "test.permanent", Message: "safe failure"},
	}))

	ready := enqueueOperationsJob(t, store, queue, nil)
	scheduledAt := time.Now().UTC().Add(time.Hour)
	scheduled := enqueueOperationsJob(t, store, queue, &scheduledAt)

	operations, err := store.GetJobOperations(testCtx, &jobsv1.GetJobOperationsRequest{Queue: queue})
	require.NoError(t, err)
	require.Len(t, operations.GetQueues(), 1)
	snapshot := operations.GetQueues()[0]
	require.EqualValues(t, 2, snapshot.GetPending())
	require.EqualValues(t, 1, snapshot.GetReady())
	require.EqualValues(t, 1, snapshot.GetScheduled())
	require.EqualValues(t, 1, snapshot.GetDeadLetter())
	require.NotNil(t, snapshot.GetOldestReadyAt())

	firstPage, err := store.ListJobs(testCtx, &jobsv1.ListJobsRequest{Queue: queue, PageSize: 2})
	require.NoError(t, err)
	require.Len(t, firstPage.GetJobs(), 2)
	require.NotEmpty(t, firstPage.GetNextPageToken())
	secondPage, err := store.ListJobs(testCtx, &jobsv1.ListJobsRequest{
		Queue: queue, PageSize: 2, PageToken: firstPage.GetNextPageToken(),
	})
	require.NoError(t, err)
	require.Len(t, secondPage.GetJobs(), 1)
	require.Empty(t, secondPage.GetNextPageToken())

	seen := map[string]bool{}
	for _, summary := range append(firstPage.GetJobs(), secondPage.GetJobs()...) {
		seen[summary.GetId()] = true
		require.NotEmpty(t, summary.GetContentType())
	}
	require.True(t, seen[deadSource])
	require.True(t, seen[ready])
	require.True(t, seen[scheduled])

	detail, err := store.GetJob(testCtx, &jobsv1.GetJobRequest{JobId: deadSource})
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_DEAD_LETTER, detail.GetJob().GetState())
	require.Len(t, detail.GetAttempts(), 1)
	require.GreaterOrEqual(t, len(detail.GetTransitions()), 3)
	require.Equal(t, "test.permanent", detail.GetJob().GetLastFailure().GetCode())

	replay := &jobsv1.ReplayJobRequest{
		SourceJobId:    deadSource,
		IdempotencyKey: "operator-replay-" + uuid.NewString(),
	}
	inserted, err := store.ReplayJob(testCtx, replay)
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED, inserted.GetDisposition())
	duplicate, err := store.ReplayJob(testCtx, proto.Clone(replay).(*jobsv1.ReplayJobRequest))
	require.NoError(t, err)
	require.Equal(t, inserted.GetJobId(), duplicate.GetJobId())
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE, duplicate.GetDisposition())

	conflict := proto.Clone(replay).(*jobsv1.ReplayJobRequest)
	conflict.AvailableAt = timestamppb.New(time.Now().UTC().Add(time.Hour))
	_, err = store.ReplayJob(testCtx, conflict)
	require.ErrorIs(t, err, jobs.ErrIdempotencyConflict)
	_, err = store.ReplayJob(testCtx, &jobsv1.ReplayJobRequest{
		SourceJobId: ready, IdempotencyKey: "not-dead-" + uuid.NewString(),
	})
	require.ErrorIs(t, err, jobs.ErrReplayNotAllowed)
	_, err = store.GetJob(testCtx, &jobsv1.GetJobRequest{JobId: uuid.NewString()})
	require.ErrorIs(t, err, jobs.ErrJobNotFound)

	var replayOf string
	var sourcePayload, replayPayload []byte
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT replay.replay_of::text, source.payload, replay.payload
		FROM job_messages replay
		JOIN job_messages source ON source.id = replay.replay_of
		WHERE replay.id = $1::uuid`, inserted.GetJobId(),
	).Scan(&replayOf, &sourcePayload, &replayPayload))
	require.Equal(t, deadSource, replayOf)
	require.Equal(t, sourcePayload, replayPayload)
}

func TestReplayCapabilityIsWorkerOnly(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var workerCanReplay bool
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT has_function_privilege(
			current_user,
			'public.replay_job_message(uuid,text,timestamptz,bytea)',
			'EXECUTE'
		)`,
	).Scan(&workerCanReplay))
	require.True(t, workerCanReplay)

	var tenantCanReplay bool
	require.NoError(t, testStore.Pool().QueryRow(testCtx, `
			SELECT has_function_privilege(
				current_user,
				'public.replay_job_message(uuid,text,timestamptz,bytea)',
				'EXECUTE'
			)`,
	).Scan(&tenantCanReplay))
	require.False(t, tenantCanReplay)
}

func enqueueOperationsJob(t *testing.T, store *infra.PostgresJobStore, queue string, availableAt *time.Time) string {
	t.Helper()
	job := &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     queue, Topic: "operations.test", Source: "operations-test",
		IdempotencyKey: uuid.NewString(), SchemaVersion: 1,
		Payload:     []byte(`{"credential":"never expose me"}`),
		ContentType: "application/json", MaxAttempts: 3,
	}
	if availableAt != nil {
		job.AvailableAt = timestamppb.New(*availableAt)
	}
	response, err := store.EnqueueJob(testCtx, &jobsv1.EnqueueJobRequest{Job: job})
	require.NoError(t, err)
	return response.GetJobId()
}

func leaseReference(envelope *jobsv1.JobEnvelope) *jobsv1.JobLeaseReference {
	return &jobsv1.JobLeaseReference{
		JobId: envelope.GetId(), WorkerId: envelope.GetLease().GetOwner(),
		LeaseToken: envelope.GetLease().GetToken(),
	}
}
