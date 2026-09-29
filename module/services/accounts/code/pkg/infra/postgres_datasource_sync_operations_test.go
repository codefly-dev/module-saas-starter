//go:build !pure

package infra_test

import (
	"testing"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestDatasourceSyncOperationIsBoundToOrganizationSourceAndRequestJob(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	orgID, sourceID, otherSourceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	requestID := enqueueDatasourceOperationJob(t, store, business.DatasourceDeliveryQueue,
		business.DatasourceReconcileTopic, business.DatasourceReconcileSource, orgID, sourceID, "")
	startDatasourceOperationJob(t, pool, requestID)

	deliveryID := enqueueDatasourceOperationJob(t, store, business.DatasourceIngestQueue,
		business.DatasourceSnapshotTopic, business.DatasourceSyncSource, orgID, sourceID, requestID)
	wrongSourceID := enqueueDatasourceOperationJob(t, store, business.DatasourceIngestQueue,
		business.DatasourceSnapshotTopic, business.DatasourceSyncSource, orgID, otherSourceID, requestID)
	for _, delivery := range []string{deliveryID, wrongSourceID} {
		execution := &jobsv1.JobExecutionReference{
			Owner: "other", Kind: "task", Id: "wrong-source",
		}
		if delivery == deliveryID {
			execution = &jobsv1.JobExecutionReference{
				Owner: "content", Kind: "task", Id: "task-42",
			}
		}
		require.NoError(t, store.Complete(testCtx, &jobsv1.CompleteJobRequest{
			Lease: startDatasourceOperationJob(t, pool, delivery), Execution: execution,
		}))
	}

	operation, err := store.GetDatasourceSyncOperation(testCtx, orgID, sourceID, requestID)
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobState_JOB_STATE_PROCESSING, operation.State)
	require.Len(t, operation.Deliveries, 1)
	require.Equal(t, deliveryID, operation.Deliveries[0].JobID)
	require.Equal(t, "task-42", operation.Deliveries[0].Execution.GetId())

	_, err = store.GetDatasourceSyncOperation(testCtx, orgID, otherSourceID, requestID)
	require.ErrorIs(t, err, business.ErrDatasourceSyncNotFound)
	_, err = store.GetDatasourceSyncOperation(testCtx, uuid.NewString(), sourceID, requestID)
	require.ErrorIs(t, err, business.ErrDatasourceSyncNotFound)
}

// This read-model fixture starts only the job it owns. Claiming the shared
// production queue can select a previous test's pending job, including one
// retained by Codefly across runs. Queue claiming has its own isolated tests.
func startDatasourceOperationJob(t *testing.T, pool *pgxpool.Pool, id string) *jobsv1.JobLeaseReference {
	t.Helper()
	lease := &jobsv1.JobLeaseReference{JobId: id, WorkerId: "sync-operation-test", LeaseToken: uuid.NewString()}
	result, err := pool.Exec(testCtx, `
		UPDATE job_messages SET state = 'processing', attempt_count = 1,
		    lease_owner = $2, lease_token = $3::uuid,
		    lease_expires_at = NOW() + INTERVAL '1 minute',
		    heartbeat_at = NOW(), last_attempt_at = NOW()
		WHERE id = $1::uuid AND state = 'pending'`, id, lease.WorkerId, lease.LeaseToken)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.RowsAffected())
	_, err = pool.Exec(testCtx, `
		INSERT INTO job_attempts (job_id, attempt_number, worker_id, lease_token)
		VALUES ($1::uuid, 1, $2, $3::uuid)`, id, lease.WorkerId, lease.LeaseToken)
	require.NoError(t, err)
	return lease
}

func enqueueDatasourceOperationJob(
	t *testing.T,
	store *infra.PostgresJobStore,
	queue, topic, source, orgID, sourceID, deliveryID string,
) string {
	t.Helper()
	attributes := map[string]string{
		"datasource.org_id": orgID, "datasource.source_id": sourceID,
	}
	if deliveryID != "" {
		attributes["datasource.delivery_id"] = deliveryID
	}
	response, err := store.EnqueueJob(testCtx, &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     queue, Topic: topic, Source: source,
		IdempotencyKey: uuid.NewString(), SchemaVersion: 1,
		Payload: []byte(`{}`), ContentType: "application/json",
		Attributes: attributes, MaxAttempts: 4,
	}})
	require.NoError(t, err)
	return response.GetJobId()
}

// With no job id the read is the source's latest sync, and its hand-off is
// aggregated from the change-set jobs keyed to it: a snapshot's stamped counts,
// or incremental file jobs counted by change type.
func TestDatasourceLatestSyncReadsTheNewestJobAndAggregatesItsHandoff(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	orgID, sourceID, otherSourceID := uuid.NewString(), uuid.NewString(), uuid.NewString()

	_ = enqueueDatasourceOperationJob(t, store, business.DatasourceDeliveryQueue,
		business.DatasourceReconcileTopic, business.DatasourceReconcileSource, orgID, sourceID, "")
	latest := enqueueDatasourceJobWithAttributes(t, store, business.DatasourceDeliveryQueue,
		business.DatasourceReconcileTopic, business.DatasourceReconcileSource, map[string]string{
			"datasource.org_id": orgID, "datasource.source_id": sourceID, "datasource.reconcile_mode": "force",
		})
	_ = enqueueDatasourceOperationJob(t, store, business.DatasourceDeliveryQueue,
		business.DatasourceReconcileTopic, business.DatasourceReconcileSource, orgID, otherSourceID, "")

	operation, err := store.GetDatasourceSyncOperation(testCtx, orgID, sourceID, "")
	require.NoError(t, err)
	require.Equal(t, latest, operation.JobID)
	require.Equal(t, jobsv1.JobState_JOB_STATE_PENDING, operation.Record.State)
	require.Equal(t, "force", operation.Record.ReconcileMode)
	require.False(t, operation.Record.CreatedAt.IsZero())
	require.Zero(t, operation.Handoff.Jobs)

	handoff := func(topic string, extra map[string]string) {
		attributes := map[string]string{
			"datasource.org_id": orgID, "datasource.source_id": sourceID, "datasource.delivery_id": latest,
			"github.commit": "c1",
		}
		for k, v := range extra {
			attributes[k] = v
		}
		enqueueDatasourceJobWithAttributes(t, store, business.DatasourceIngestQueue, topic, business.DatasourceSyncSource, attributes)
	}
	handoff(business.DatasourceSnapshotTopic, map[string]string{
		"datasource.changes.files": "3", "datasource.changes.added": "1", "datasource.changes.modified": "1",
		"datasource.changes.deleted": "1", "datasource.changes.split_known": "true",
	})
	handoff(business.DatasourceChangeSetFileTopic, map[string]string{"github.change_type": "added"})
	handoff(business.DatasourceChangeSetFileTopic, map[string]string{"github.change_type": "renamed"})

	operation, err = store.GetDatasourceSyncOperation(testCtx, orgID, sourceID, "")
	require.NoError(t, err)
	got := operation.Handoff
	require.Equal(t, 3, got.Jobs)
	require.True(t, got.Snapshot)
	require.Equal(t, "c1", got.Commit)
	require.Equal(t, [5]string{"3", "1", "1", "1", "true"},
		[5]string{got.SnapshotFiles, got.SnapshotAdded, got.SnapshotModified, got.SnapshotDeleted, got.SnapshotSplitKnown})
	require.Equal(t, [3]int{1, 1, 0}, [3]int{got.Added, got.Modified, got.Deleted})
	require.NotNil(t, got.FirstAt)
	require.Len(t, operation.Deliveries, 1, "deliveries list the snapshot only, as before")

	_, err = store.GetDatasourceSyncOperation(testCtx, uuid.NewString(), sourceID, "")
	require.ErrorIs(t, err, business.ErrDatasourceSyncNotFound)
}

func enqueueDatasourceJobWithAttributes(
	t *testing.T,
	store *infra.PostgresJobStore,
	queue, topic, source string,
	attributes map[string]string,
) string {
	t.Helper()
	response, err := store.EnqueueJob(testCtx, &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     queue, Topic: topic, Source: source,
		IdempotencyKey: uuid.NewString(), SchemaVersion: 1,
		Payload: []byte(`{}`), ContentType: "application/json",
		Attributes: attributes, MaxAttempts: 4,
	}})
	require.NoError(t, err)
	return response.GetJobId()
}
