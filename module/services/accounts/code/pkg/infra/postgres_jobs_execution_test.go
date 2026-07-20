package infra_test

import (
	"sync"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPostgresJobStoreRejectsInvalidGeneratedCommands(t *testing.T) {
	store := infra.NewPostgresJobStore(nil)

	var nilClaim *jobsv1.ClaimJobsRequest
	_, err := store.Claim(testCtx, nilClaim)
	require.ErrorIs(t, err, jobs.ErrInvalidCommand)

	_, err = store.Claim(testCtx, &jobsv1.ClaimJobsRequest{})
	require.ErrorIs(t, err, jobs.ErrInvalidCommand)

	err = store.Complete(testCtx, &jobsv1.CompleteJobRequest{})
	require.ErrorIs(t, err, jobs.ErrInvalidCommand)
}

func TestPostgresJobStoreClaimsScheduledAndConcurrentWorkExactlyOnce(t *testing.T) {
	pool, store := newJobExecutionHarness(t)
	queue := executionQueue("claim")

	scheduled := insertExecutionJob(t, pool, executionJob{
		queue: queue, availableAt: time.Now().Add(time.Hour),
	})
	require.Empty(t, claimExecutionJobs(t, store, queue, "worker-scheduled", 10).GetJobs())
	_, err := pool.Exec(testCtx, `
		UPDATE job_messages SET available_at = NOW() WHERE id = $1`, scheduled)
	require.NoError(t, err)
	claimed := claimExecutionJobs(t, store, queue, "worker-scheduled", 10).GetJobs()
	require.Len(t, claimed, 1)
	require.Equal(t, scheduled.String(), claimed[0].GetId())
	require.NotEmpty(t, claimed[0].GetLease().GetToken())
	require.EqualValues(t, 1, claimed[0].GetAttemptCount())
	require.Equal(t, jobsv1.JobState_JOB_STATE_PROCESSING, claimed[0].GetState())
	require.True(t, claimed[0].GetScope().GetGlobal())
	require.NoError(t, store.Complete(testCtx, completeExecutionRequest(claimed[0])))

	const jobCount = 8
	for range jobCount {
		insertExecutionJob(t, pool, executionJob{queue: queue})
	}

	start := make(chan struct{})
	responses := make(chan *jobsv1.ClaimJobsResponse, 2)
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for index := range 2 {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			response, err := store.Claim(testCtx, claimExecutionRequest(
				queue, "worker-concurrent-"+string(rune('a'+index)), 4,
			))
			if err != nil {
				errorsFound <- err
				return
			}
			responses <- response
		}(index)
	}
	close(start)
	workers.Wait()
	close(responses)
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}

	claimedIDs := make(map[string]struct{}, jobCount)
	leaseTokens := make(map[string]struct{}, jobCount)
	for response := range responses {
		require.Len(t, response.GetJobs(), jobCount/2)
		for _, job := range response.GetJobs() {
			_, duplicate := claimedIDs[job.GetId()]
			require.False(t, duplicate, "concurrent workers claimed the same fenced job")
			claimedIDs[job.GetId()] = struct{}{}
			_, duplicate = leaseTokens[job.GetLease().GetToken()]
			require.False(t, duplicate, "every claimed job requires its own fencing token")
			leaseTokens[job.GetLease().GetToken()] = struct{}{}
		}
	}
	require.Len(t, claimedIDs, jobCount)
	require.Len(t, leaseTokens, jobCount)
}

func TestPostgresJobStorePreservesOrderingAndRunsRetryLifecycle(t *testing.T) {
	pool, store := newJobExecutionHarness(t)
	queue := executionQueue("ordering")
	orderingKey := "account-42"
	first := insertExecutionJob(t, pool, executionJob{queue: queue, orderingKey: orderingKey})
	second := insertExecutionJob(t, pool, executionJob{queue: queue, orderingKey: orderingKey})

	firstClaim := claimExecutionJobs(t, store, queue, "worker-ordering", 10).GetJobs()
	require.Len(t, firstClaim, 1)
	require.Equal(t, first.String(), firstClaim[0].GetId())
	require.Empty(t, claimExecutionJobs(t, store, queue, "worker-other", 10).GetJobs(),
		"a live predecessor must fence the rest of its ordering key")

	oldExpiry := firstClaim[0].GetLease().GetExpiresAt().AsTime()
	heartbeat, err := store.Heartbeat(testCtx, &jobsv1.HeartbeatJobRequest{
		Lease:     executionLease(firstClaim[0]),
		Extension: durationpb.New(5 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, heartbeat.GetLease().GetExpiresAt().AsTime().After(oldExpiry))

	wrongLease := executionLease(firstClaim[0])
	wrongLease.LeaseToken = uuid.NewString()
	_, err = store.Heartbeat(testCtx, &jobsv1.HeartbeatJobRequest{
		Lease: wrongLease, Extension: durationpb.New(time.Minute),
	})
	require.ErrorIs(t, err, jobs.ErrLeaseLost)

	retryAt := time.Now().Add(time.Hour).UTC()
	retry, err := store.Retry(testCtx, &jobsv1.RetryJobRequest{
		Lease: executionLease(firstClaim[0]),
		Failure: &jobsv1.JobFailure{
			Code: "test.transient", Message: "dependency unavailable",
		},
		RetryAt: timestamppb.New(retryAt),
	})
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_RETRYING, retry.GetState())
	require.Empty(t, claimExecutionJobs(t, store, queue, "worker-too-early", 10).GetJobs(),
		"a scheduled predecessor must also block its ordering key")

	_, err = pool.Exec(testCtx, `UPDATE job_messages SET available_at = NOW() WHERE id = $1`, first)
	require.NoError(t, err)
	secondAttempt := claimExecutionJobs(t, store, queue, "worker-retry", 10).GetJobs()
	require.Len(t, secondAttempt, 1)
	require.Equal(t, first.String(), secondAttempt[0].GetId())
	require.NotEqual(t, firstClaim[0].GetLease().GetToken(), secondAttempt[0].GetLease().GetToken())
	require.EqualValues(t, 2, secondAttempt[0].GetAttemptCount())
	require.NoError(t, store.Complete(testCtx, completeExecutionRequest(secondAttempt[0])))

	next := claimExecutionJobs(t, store, queue, "worker-next", 10).GetJobs()
	require.Len(t, next, 1)
	require.Equal(t, second.String(), next[0].GetId())

	rows, err := pool.Query(testCtx, `
		SELECT outcome FROM job_attempts WHERE job_id = $1 ORDER BY attempt_number`, first)
	require.NoError(t, err)
	defer rows.Close()
	var outcomes []string
	for rows.Next() {
		var outcome string
		require.NoError(t, rows.Scan(&outcome))
		outcomes = append(outcomes, outcome)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"retryable_failure", "succeeded"}, outcomes)
}

func TestPostgresJobStoreRecoversExpiredLeasesAndFencesLateWorkers(t *testing.T) {
	pool, store := newJobExecutionHarness(t)
	queue := executionQueue("recovery")
	jobID := insertExecutionJob(t, pool, executionJob{queue: queue, maxAttempts: 2})
	first := claimExecutionJobs(t, store, queue, "worker-stale", 1).GetJobs()
	require.Len(t, first, 1)

	_, err := pool.Exec(testCtx, `
		UPDATE job_messages
		SET lease_expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, jobID)
	require.NoError(t, err)
	_, err = store.Heartbeat(testCtx, &jobsv1.HeartbeatJobRequest{
		Lease: executionLease(first[0]), Extension: durationpb.New(time.Minute),
	})
	require.ErrorIs(t, err, jobs.ErrLeaseLost, "an expired lease cannot be revived")
	require.ErrorIs(t,
		store.Complete(testCtx, completeExecutionRequest(first[0])),
		jobs.ErrLeaseLost,
	)

	reclaimed := claimExecutionJobs(t, store, queue, "worker-recovery", 1).GetJobs()
	require.Len(t, reclaimed, 1)
	require.Equal(t, jobID.String(), reclaimed[0].GetId())
	require.NotEqual(t, first[0].GetLease().GetToken(), reclaimed[0].GetLease().GetToken())
	require.EqualValues(t, 2, reclaimed[0].GetAttemptCount())
	require.ErrorIs(t,
		store.Complete(testCtx, completeExecutionRequest(first[0])),
		jobs.ErrLeaseLost,
		"an old fencing token must stay invalid after another worker reclaims the job",
	)

	require.NoError(t, store.DeadLetter(testCtx, &jobsv1.DeadLetterJobRequest{
		Lease: executionLease(reclaimed[0]),
		Failure: &jobsv1.JobFailure{
			Code: "test.permanent", Message: "invalid external record",
		},
	}))

	var state string
	var deadLetteredAt *time.Time
	var outcomes []string
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT state, dead_lettered_at,
		       ARRAY(SELECT outcome FROM job_attempts
		             WHERE job_id = job_messages.id ORDER BY attempt_number)
		FROM job_messages WHERE id = $1`, jobID,
	).Scan(&state, &deadLetteredAt, &outcomes))
	require.Equal(t, "dead_letter", state)
	require.NotNil(t, deadLetteredAt)
	require.Equal(t, []string{"lease_expired", "permanent_failure"}, outcomes)

	exhaustedID := insertExecutionJob(t, pool, executionJob{queue: queue, maxAttempts: 1})
	exhausted := claimExecutionJobs(t, store, queue, "worker-expire-final", 1).GetJobs()
	require.Len(t, exhausted, 1)
	require.Equal(t, exhaustedID.String(), exhausted[0].GetId())
	_, err = pool.Exec(testCtx, `
		UPDATE job_messages
		SET lease_expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, exhaustedID)
	require.NoError(t, err)
	require.Empty(t, claimExecutionJobs(t, store, queue, "worker-after-expiry", 1).GetJobs())
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT state FROM job_messages WHERE id = $1`, exhaustedID,
	).Scan(&state))
	require.Equal(t, "dead_letter", state, "an expired final attempt exhausts the budget")
}

func TestPostgresJobStoreDeadLettersExhaustedRetryBudget(t *testing.T) {
	pool, store := newJobExecutionHarness(t)
	queue := executionQueue("budget")
	insertExecutionJob(t, pool, executionJob{queue: queue, maxAttempts: 1})
	claimed := claimExecutionJobs(t, store, queue, "worker-budget", 1).GetJobs()
	require.Len(t, claimed, 1)

	response, err := store.Retry(testCtx, &jobsv1.RetryJobRequest{
		Lease:   executionLease(claimed[0]),
		Failure: &jobsv1.JobFailure{Code: "test.exhausted"},
		RetryAt: timestamppb.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_DEAD_LETTER, response.GetState())
	require.Empty(t, claimExecutionJobs(t, store, queue, "worker-after-budget", 1).GetJobs())
}

type executionJob struct {
	queue       string
	orderingKey string
	availableAt time.Time
	maxAttempts int
}

func newJobExecutionHarness(t *testing.T) (*pgxpool.Pool, *infra.PostgresJobStore) {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, infra.NewPostgresJobStore(pool)
}

func insertExecutionJob(t *testing.T, pool *pgxpool.Pool, job executionJob) uuid.UUID {
	t.Helper()
	if job.availableAt.IsZero() {
		job.availableAt = time.Now().Add(-time.Second).UTC()
	}
	if job.maxAttempts == 0 {
		job.maxAttempts = 4
	}
	id := uuid.New()
	_, err := pool.Exec(testCtx, `
		INSERT INTO job_messages (
			id, direction, scope_kind, queue, topic, source,
			idempotency_key, request_fingerprint, ordering_key, schema_version, payload,
			content_type, attributes, max_attempts, available_at
		) VALUES (
			$1, 'inbox', 'global', $2, 'test.execution.v1', 'infra-test',
			$3, decode(repeat('00', 32), 'hex'), NULLIF($4, ''), 1, $5,
			'application/json', $6, $7, $8
		)`,
		id, job.queue, uuid.NewString(), job.orderingKey,
		[]byte(`{"event":"execute"}`), `{"trace_id":"test-trace"}`,
		job.maxAttempts, job.availableAt,
	)
	require.NoError(t, err)
	return id
}

func executionQueue(prefix string) string {
	return "test." + prefix + "." + uuid.NewString()
}

func claimExecutionRequest(
	queue, worker string,
	limit uint32,
) *jobsv1.ClaimJobsRequest {
	return &jobsv1.ClaimJobsRequest{
		Queue: queue, WorkerId: worker, Limit: limit,
		LeaseDuration: durationpb.New(time.Minute),
	}
}

func claimExecutionJobs(
	t *testing.T,
	store *infra.PostgresJobStore,
	queue, worker string,
	limit uint32,
) *jobsv1.ClaimJobsResponse {
	t.Helper()
	response, err := store.Claim(testCtx, claimExecutionRequest(queue, worker, limit))
	require.NoError(t, err)
	return response
}

func executionLease(job *jobsv1.JobEnvelope) *jobsv1.JobLeaseReference {
	return &jobsv1.JobLeaseReference{
		JobId: job.GetId(), WorkerId: job.GetLease().GetOwner(),
		LeaseToken: job.GetLease().GetToken(),
	}
}

func completeExecutionRequest(job *jobsv1.JobEnvelope) *jobsv1.CompleteJobRequest {
	return &jobsv1.CompleteJobRequest{Lease: executionLease(job)}
}
