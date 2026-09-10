package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// fakePrivacyWorkflow is a configurable adapter. Each test supplies the
// behavior it needs and counts, in its own closure, how many times an external
// effect was actually produced versus how many attempts ran.
type fakePrivacyWorkflow struct {
	steps    []string
	export   func(ctx context.Context, op *business.PrivacyOperation) (business.PrivacyExportArtifact, error)
	remove   func(ctx context.Context, op *business.PrivacyOperation) error
	cleaned  []string
	cleanErr error
}

func (f *fakePrivacyWorkflow) RequiredSteps(business.GDPRRequestType) []string { return f.steps }

func (f *fakePrivacyWorkflow) Export(
	ctx context.Context,
	op *business.PrivacyOperation,
) (business.PrivacyExportArtifact, error) {
	return f.export(ctx, op)
}

func (f *fakePrivacyWorkflow) Delete(ctx context.Context, op *business.PrivacyOperation) error {
	return f.remove(ctx, op)
}

func (f *fakePrivacyWorkflow) DeleteExportArtifact(_ context.Context, requestID string) error {
	if f.cleanErr != nil {
		return f.cleanErr
	}
	f.cleaned = append(f.cleaned, requestID)
	return nil
}

// exportingWorkflow completes in one step, recording its receipt the way an
// adapter is expected to: the effect first, then the evidence for it.
func exportingWorkflow(url string, expiresAt time.Time) *fakePrivacyWorkflow {
	return &fakePrivacyWorkflow{
		steps: []string{"package"},
		export: func(ctx context.Context, op *business.PrivacyOperation) (business.PrivacyExportArtifact, error) {
			if err := op.RecordReceipt(ctx, "package", op.IdempotencyKey("package")); err != nil {
				return business.PrivacyExportArtifact{}, err
			}
			return business.PrivacyExportArtifact{DownloadURL: url, ExpiresAt: expiresAt}, nil
		},
		remove: func(context.Context, *business.PrivacyOperation) error { return nil },
	}
}

// usePrivacyWorkflow wires an adapter for one test and restores the fail-closed
// default afterwards.
func usePrivacyWorkflow(t *testing.T, workflow business.PrivacyWorkflow) {
	t.Helper()
	testService.SetPrivacyWorkflow(testStore, workflow)
	t.Cleanup(func() { testService.SetPrivacyWorkflow(nil, nil) })
}

type privacyWorkerHarness struct {
	pool   *pgxpool.Pool
	store  jobs.Store
	worker *jobs.Worker
}

func newPrivacyWorkerHarness(t *testing.T, workerID string) *privacyWorkerHarness {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	store := infra.NewPostgresJobStore(pool)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:         store,
		Queue:         business.PrivacyWorkflowQueue,
		Handler:       testService.NewPrivacyJobHandler(),
		WorkerID:      workerID,
		LeaseDuration: 30 * time.Second,
		// Deterministic tests re-poll immediately instead of waiting out the
		// production backoff. The delay is negative so a retry is claimable
		// even when this process's clock runs slightly ahead of the database's.
		RetryDelay: func(uint32) time.Duration { return -time.Minute },
	})
	require.NoError(t, err)
	return &privacyWorkerHarness{pool: pool, store: store, worker: worker}
}

func (h *privacyWorkerHarness) runOnce(t *testing.T) {
	t.Helper()
	// Handler failures are the point of several tests; the durable request and
	// job state carry the assertions, not the iteration's error.
	_, _ = h.worker.RunOnce(testCtx)
}

func (h *privacyWorkerHarness) jobState(t *testing.T, jobID string) string {
	t.Helper()
	var state string
	require.NoError(t, h.pool.QueryRow(testCtx,
		`SELECT state FROM job_messages WHERE id = $1`, jobID).Scan(&state))
	return state
}

func (h *privacyWorkerHarness) claim(t *testing.T, workerID string) []*jobsv1.JobEnvelope {
	t.Helper()
	response, err := h.store.Claim(testCtx, &jobsv1.ClaimJobsRequest{
		Queue:         business.PrivacyWorkflowQueue,
		WorkerId:      workerID,
		Limit:         10,
		LeaseDuration: durationpb.New(30 * time.Second),
	})
	require.NoError(t, err)
	return response.GetJobs()
}

func (h *privacyWorkerHarness) expireLease(t *testing.T, jobID string) {
	t.Helper()
	_, err := h.pool.Exec(testCtx, `
		UPDATE job_messages
		SET lease_expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, jobID)
	require.NoError(t, err)
}

func privacyRequest(t *testing.T, userID, requestID string) *business.GDPRRequest {
	t.Helper()
	var request *business.GDPRRequest
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		var err error
		request, err = business.GDPRStore(testStore).GetGDPRRequest(ctx, requestID)
		return err
	}))
	require.Equal(t, userID, request.UserID)
	return request
}

// A request is accepted only together with the job that will execute it: one
// commit holds both, so no accepted request is left without an owner and no
// job refers to a request that was never accepted.
func TestPrivacyRequestAndItsJobCommitTogether(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-atomic@test.com", "privacy-atomic", "Privacy atomic")
	usePrivacyWorkflow(t, exportingWorkflow("https://storage.example.com/export.zip", time.Now().Add(time.Hour)))
	harness := newPrivacyWorkerHarness(t, "worker-atomic")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, business.GDPRPending, accepted.Status)
	require.NotEmpty(t, accepted.JobID)

	stored := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, accepted.JobID, stored.JobID)

	var queue, topic, source, scopeKind, subjectID, idempotencyKey, state string
	require.NoError(t, harness.pool.QueryRow(testCtx, `
		SELECT queue, topic, source, scope_kind, subject_id::text, idempotency_key, state
		FROM job_messages WHERE id = $1`, accepted.JobID,
	).Scan(&queue, &topic, &source, &scopeKind, &subjectID, &idempotencyKey, &state))
	require.Equal(t, business.PrivacyWorkflowQueue, queue)
	require.Equal(t, business.PrivacyExportTopic, topic)
	require.Equal(t, business.PrivacyWorkflowSource, source)
	require.Equal(t, "subject", scopeKind)
	require.Equal(t, userID, subjectID)
	require.Equal(t, accepted.ID, idempotencyKey,
		"the request UUID is the job's idempotency key, so the pair is one logical operation")
	require.Equal(t, "pending", state)
}

// The commit boundary holds in the other direction too: a request whose job
// cannot be enqueued leaves nothing behind for a worker to find or a caller to
// poll.
func TestPrivacyRequestIsNotAcceptedWhenItsJobCannotBeEnqueued(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-rollback@test.com", "privacy-rollback", "Privacy rollback")
	testService.SetPrivacyWorkflow(
		failingPrivacyProducer{},
		exportingWorkflow("https://storage.example.com/export.zip", time.Now().Add(time.Hour)),
	)
	t.Cleanup(func() { testService.SetPrivacyWorkflow(nil, nil) })

	_, err := testService.RequestExport(testCtx, userID)
	require.Error(t, err)

	var requests []*business.GDPRRequest
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		var listErr error
		requests, listErr = business.GDPRStore(testStore).GetUserGDPRRequests(ctx, userID)
		return listErr
	}))
	require.Empty(t, requests)
}

type failingPrivacyProducer struct{}

func (failingPrivacyProducer) EnqueueJob(
	context.Context,
	*jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	return nil, errors.New("durable enqueue unavailable")
}

func TestPrivacyExportCompletesThroughTheLeasedWorker(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-export@test.com", "privacy-export", "Privacy export")
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	usePrivacyWorkflow(t, exportingWorkflow("https://storage.example.com/export.zip", expiresAt))
	harness := newPrivacyWorkerHarness(t, "worker-export")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)
	harness.runOnce(t)

	status, err := testService.GetExportStatus(testCtx, userID, accepted.ID)
	require.NoError(t, err)
	require.Equal(t, business.GDPRCompleted, status.Status)
	require.Equal(t, "https://storage.example.com/export.zip", status.DownloadURL)
	require.NotNil(t, status.ExpiresAt)
	require.WithinDuration(t, expiresAt, *status.ExpiresAt, time.Second)
	require.NotNil(t, status.CompletedAt)
	require.Empty(t, status.LeaseToken, "a finished request holds no lease")
	require.Equal(t, "succeeded", harness.jobState(t, accepted.JobID))

	stored := privacyRequest(t, userID, accepted.ID)
	require.Len(t, stored.StepReceipts, 1)
	require.NotEmpty(t, stored.StepReceipts["package"])
}

// A crash between an external effect and the record of it must not repeat the
// effect. The adapter's receipt is what a later attempt reads to know the step
// is done; the deterministic idempotency key is what makes repeating it safe
// when no receipt was written.
func TestPrivacyRetryReadsReceiptsInsteadOfRepeatingEffects(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-receipts@test.com", "privacy-receipts", "Privacy receipts")

	effects := 0
	var keys []string
	attempts := 0
	workflow := &fakePrivacyWorkflow{
		steps: []string{"purge_provider", "confirm"},
		remove: func(ctx context.Context, op *business.PrivacyOperation) error {
			attempts++
			keys = append(keys, op.IdempotencyKey("purge_provider"))
			if _, done := op.Receipt("purge_provider"); !done {
				effects++
				if err := op.RecordReceipt(ctx, "purge_provider", "provider-ack"); err != nil {
					return err
				}
				// The provider effect is recorded, then the attempt dies before
				// the request could be completed.
				return business.NewPrivacyFailure(
					"privacy.provider_unavailable", "provider went away mid-workflow", false)
			}
			return op.RecordReceipt(ctx, "confirm", "confirmed")
		},
	}
	usePrivacyWorkflow(t, workflow)
	harness := newPrivacyWorkerHarness(t, "worker-receipts")

	accepted, err := testService.RequestDeletion(testCtx, userID)
	require.NoError(t, err)

	harness.runOnce(t)
	afterFailure := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRRetrying, afterFailure.Status)
	require.Equal(t, "privacy.provider_unavailable", afterFailure.FailureCode)
	require.Equal(t, "provider-ack", afterFailure.StepReceipts["purge_provider"])
	require.Equal(t, "retrying", harness.jobState(t, accepted.JobID))

	harness.runOnce(t)
	completed := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRCompleted, completed.Status)
	require.Empty(t, completed.FailureCode)
	require.Equal(t, 2, attempts)
	require.Equal(t, 1, effects, "the recorded step must not be executed again")
	require.Equal(t, keys[0], keys[1],
		"the operation id is stable across attempts, so an unrecorded effect can be repeated safely")
	require.Equal(t, "succeeded", harness.jobState(t, accepted.JobID))
}

// Success is not the adapter's word alone: a workflow that reports done without
// a receipt for every step it declared required is a failure needing attention,
// not a completed request.
func TestPrivacyCompletionRequiresEveryDeclaredReceipt(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-partial@test.com", "privacy-partial", "Privacy partial")
	usePrivacyWorkflow(t, &fakePrivacyWorkflow{
		steps: []string{"package", "publish"},
		export: func(ctx context.Context, op *business.PrivacyOperation) (business.PrivacyExportArtifact, error) {
			require.NoError(t, op.RecordReceipt(ctx, "package", "packaged"))
			return business.PrivacyExportArtifact{
				DownloadURL: "https://storage.example.com/partial.zip",
				ExpiresAt:   time.Now().Add(time.Hour),
			}, nil
		},
		remove: func(context.Context, *business.PrivacyOperation) error { return nil },
	})
	harness := newPrivacyWorkerHarness(t, "worker-partial")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)
	harness.runOnce(t)

	stored := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRFailed, stored.Status)
	require.Equal(t, "privacy.incomplete_receipts", stored.FailureCode)
	require.Empty(t, stored.DownloadURL, "an incomplete export publishes no artifact")
	require.Equal(t, "dead_letter", harness.jobState(t, accepted.JobID),
		"an adapter that under-reports needs attention, not another attempt")
}

// A permanent failure spends no further attempts, leaves an operator-visible
// terminal state, and stays recoverable: replaying the dead-lettered job runs
// the same logical operation again. The replayed job's attempts restart at one,
// which is why a request that already failed on a later attempt must still be
// claimable by it.
func TestPermanentPrivacyFailureIsTerminalAndRecoverable(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-permanent@test.com", "privacy-permanent", "Privacy permanent")
	attempts := 0
	recovered := false
	workflow := &fakePrivacyWorkflow{
		steps: []string{"package"},
		export: func(ctx context.Context, op *business.PrivacyOperation) (business.PrivacyExportArtifact, error) {
			attempts++
			switch {
			case recovered:
				if err := op.RecordReceipt(ctx, "package", "packaged"); err != nil {
					return business.PrivacyExportArtifact{}, err
				}
				return business.PrivacyExportArtifact{
					DownloadURL: "https://storage.example.com/recovered.zip",
					ExpiresAt:   time.Now().Add(time.Hour),
				}, nil
			case attempts == 1:
				return business.PrivacyExportArtifact{}, business.NewPrivacyFailure(
					"privacy.provider_unavailable", "provider is briefly down", false)
			default:
				return business.PrivacyExportArtifact{}, business.NewPrivacyFailure(
					"privacy.dataset_inventory_incomplete", "a required dataset has no adapter", true)
			}
		},
		remove: func(context.Context, *business.PrivacyOperation) error { return nil },
	}
	usePrivacyWorkflow(t, workflow)
	harness := newPrivacyWorkerHarness(t, "worker-permanent")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)
	harness.runOnce(t)
	require.Equal(t, business.GDPRRetrying, privacyRequest(t, userID, accepted.ID).Status)
	harness.runOnce(t)

	failed := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRFailed, failed.Status)
	require.Equal(t, "privacy.dataset_inventory_incomplete", failed.FailureCode)
	require.EqualValues(t, 2, failed.Attempt)
	require.Equal(t, "dead_letter", harness.jobState(t, accepted.JobID),
		"a permanent failure ends the job instead of spending the remaining budget")
	require.Equal(t, 2, attempts)

	// Administrative recovery: the operator fixes the adapter and replays.
	recovered = true
	replay, err := infra.NewPostgresJobStore(harness.pool).ReplayJob(testCtx, &jobsv1.ReplayJobRequest{
		SourceJobId: accepted.JobID, IdempotencyKey: business.NewIDString(),
	})
	require.NoError(t, err)
	harness.runOnce(t)

	completed := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRCompleted, completed.Status)
	require.Equal(t, "https://storage.example.com/recovered.zip", completed.DownloadURL)
	require.Empty(t, completed.FailureCode)
	require.Equal(t, replay.GetJobId(), completed.JobID,
		"the request now names the job that owns it")
	require.Equal(t, "succeeded", harness.jobState(t, replay.GetJobId()))
}

// Retries are bounded: when the last attempt fails the request reaches a
// terminal state naming the failure instead of sitting in retrying forever.
func TestExhaustedPrivacyRetriesLeaveATerminalRequest(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-exhausted@test.com", "privacy-exhausted", "Privacy exhausted")
	usePrivacyWorkflow(t, &fakePrivacyWorkflow{
		steps: []string{"package"},
		export: func(context.Context, *business.PrivacyOperation) (business.PrivacyExportArtifact, error) {
			return business.PrivacyExportArtifact{}, business.NewPrivacyFailure(
				"privacy.provider_unavailable", "provider is down", false)
		},
		remove: func(context.Context, *business.PrivacyOperation) error { return nil },
	})
	harness := newPrivacyWorkerHarness(t, "worker-exhausted")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)
	for range business.PrivacyWorkflowMaxAttempts {
		harness.runOnce(t)
	}

	stored := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRFailed, stored.Status)
	require.Equal(t, "privacy.provider_unavailable", stored.FailureCode)
	require.EqualValues(t, business.PrivacyWorkflowMaxAttempts, stored.Attempt)
	require.Equal(t, "dead_letter", harness.jobState(t, accepted.JobID))
}

// A worker whose lease ran out cannot act on the request the next attempt has
// taken over: it is refused before the adapter runs, so it can neither produce
// effects nor finalize work it no longer holds.
func TestStalePrivacyWorkerCannotActOnAReclaimedRequest(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-fencing@test.com", "privacy-fencing", "Privacy fencing")
	runs := 0
	usePrivacyWorkflow(t, &fakePrivacyWorkflow{
		steps: []string{"package"},
		export: func(ctx context.Context, op *business.PrivacyOperation) (business.PrivacyExportArtifact, error) {
			runs++
			if runs == 1 {
				return business.PrivacyExportArtifact{}, business.NewPrivacyFailure(
					"privacy.provider_unavailable", "provider is briefly down", false)
			}
			if err := op.RecordReceipt(ctx, "package", "packaged"); err != nil {
				return business.PrivacyExportArtifact{}, err
			}
			return business.PrivacyExportArtifact{
				DownloadURL: "https://storage.example.com/fenced.zip",
				ExpiresAt:   time.Now().Add(time.Hour),
			}, nil
		},
		remove: func(context.Context, *business.PrivacyOperation) error { return nil },
	})
	harness := newPrivacyWorkerHarness(t, "worker-fencing")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)

	stale := harness.claim(t, "worker-stale")
	require.Len(t, stale, 1)
	harness.expireLease(t, accepted.JobID)
	current := harness.claim(t, "worker-current")
	require.Len(t, current, 1)
	require.EqualValues(t, 2, current[0].GetAttemptCount())

	// The attempt that took the request over records itself on it.
	handler := testService.NewPrivacyJobHandler()
	require.Error(t, handler(testCtx, current[0]))
	reclaimed := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRRetrying, reclaimed.Status)
	require.EqualValues(t, 2, reclaimed.Attempt)
	require.Equal(t, 1, runs)

	require.ErrorIs(t, handler(testCtx, stale[0]), business.ErrPrivacyLeaseLost)
	require.Equal(t, 1, runs, "a stale worker must not reach the adapter at all")
	require.Equal(t, business.GDPRRetrying, privacyRequest(t, userID, accepted.ID).Status)

	// The queue then finishes the work on its next attempt.
	harness.expireLease(t, accepted.JobID)
	harness.runOnce(t)
	completed := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRCompleted, completed.Status)
	require.Equal(t, "https://storage.example.com/fenced.zip", completed.DownloadURL)
	require.Equal(t, 2, runs)

	// And the stale attempt still cannot rewrite the completed request.
	require.NoError(t, handler(testCtx, stale[0]))
	require.Equal(t, business.GDPRCompleted, privacyRequest(t, userID, accepted.ID).Status)
	require.Equal(t, 2, runs)
}

// A job that reaches a runtime with no adapter is unexecutable. It must say so
// on the request and dead-letter, not sit in processing forever.
func TestPrivacyJobWithoutAnAdapterFailsClosed(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-unset@test.com", "privacy-unset", "Privacy unset")
	usePrivacyWorkflow(t, exportingWorkflow("https://storage.example.com/export.zip", time.Now().Add(time.Hour)))
	harness := newPrivacyWorkerHarness(t, "worker-unset")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)

	// The adapter is removed between acceptance and execution.
	testService.SetPrivacyWorkflow(nil, nil)
	harness.runOnce(t)

	stored := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRFailed, stored.Status)
	require.Equal(t, "privacy.workflow_unavailable", stored.FailureCode)
	require.Equal(t, "dead_letter", harness.jobState(t, accepted.JobID))
}

// A download reference is only handed out inside its authorized window, and the
// sweep asks the adapter to delete the stored artifact once that window closes.
func TestLapsedExportArtifactIsHiddenThenDeleted(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-expiry@test.com", "privacy-expiry", "Privacy expiry")
	workflow := exportingWorkflow("https://storage.example.com/lapsed.zip", time.Now().Add(-time.Minute))
	usePrivacyWorkflow(t, workflow)
	harness := newPrivacyWorkerHarness(t, "worker-expiry")

	accepted, err := testService.RequestExport(testCtx, userID)
	require.NoError(t, err)
	harness.runOnce(t)

	status, err := testService.GetExportStatus(testCtx, userID, accepted.ID)
	require.NoError(t, err)
	require.Equal(t, business.GDPRCompleted, status.Status)
	require.Empty(t, status.DownloadURL, "a lapsed window hands out no reference")

	// A refused cleanup leaves the reference in place so the next sweep retries
	// rather than losing track of an artifact that still exists.
	workflow.cleanErr = errors.New("object store unreachable")
	purged, err := testService.PurgeExpiredPrivacyArtifacts(testCtx)
	require.NoError(t, err)
	require.Zero(t, purged)
	require.NotEmpty(t, privacyRequest(t, userID, accepted.ID).DownloadURL)

	workflow.cleanErr = nil
	purged, err = testService.PurgeExpiredPrivacyArtifacts(testCtx)
	require.NoError(t, err)
	require.Equal(t, 1, purged)
	require.Equal(t, []string{accepted.ID}, workflow.cleaned)
	require.Empty(t, privacyRequest(t, userID, accepted.ID).DownloadURL)
}

// removeSubjectCredentials is what a deletion adapter does to the subject's own
// authentication state before it reports completion.
func removeSubjectCredentials(ctx context.Context, userID string) error {
	tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
	for _, statement := range []string{
		`DELETE FROM sessions WHERE user_id = $1`,
		`DELETE FROM user_identities WHERE user_uuid = $1`,
	} {
		if _, err := tx.Exec(ctx, statement, userID); err != nil {
			return err
		}
	}
	return nil
}

// Deletion has to be able to report what it did after the subject's own
// credentials are gone, so the workflow's durable state cannot depend on the
// identity it just removed.
func TestPrivacyDeletionFinalizesAfterTheSubjectsCredentialsAreRemoved(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(t, testCtx, "privacy-erasure@test.com", "privacy-erasure", "Privacy erasure")
	usePrivacyWorkflow(t, &fakePrivacyWorkflow{
		steps: []string{"erase_identities"},
		remove: func(ctx context.Context, op *business.PrivacyOperation) error {
			if err := testStore.WithControlPlane(ctx, func(ctx context.Context) error {
				return removeSubjectCredentials(ctx, op.UserID)
			}); err != nil {
				return err
			}
			return op.RecordReceipt(ctx, "erase_identities", "identities-removed")
		},
		export: func(context.Context, *business.PrivacyOperation) (business.PrivacyExportArtifact, error) {
			return business.PrivacyExportArtifact{}, errors.New("unused")
		},
	})
	harness := newPrivacyWorkerHarness(t, "worker-erasure")

	accepted, err := testService.RequestDeletion(testCtx, userID)
	require.NoError(t, err)
	harness.runOnce(t)

	stored := privacyRequest(t, userID, accepted.ID)
	require.Equal(t, business.GDPRCompleted, stored.Status)
	require.NotNil(t, stored.CompletedAt)
	require.Equal(t, "identities-removed", stored.StepReceipts["erase_identities"])
	require.Equal(t, "succeeded", harness.jobState(t, accepted.JobID))

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	events, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		EventType: string(business.EventGDPRDeletionDone), From: &from, To: &to, PageSize: 100,
	})
	require.NoError(t, err)
	recorded := 0
	for _, event := range events {
		if event.ResourceID == accepted.ID {
			recorded++
		}
	}
	require.Equal(t, 1, recorded,
		"completion is recorded exactly once, in the transaction that completes it")
}
