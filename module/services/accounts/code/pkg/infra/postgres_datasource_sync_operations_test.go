//go:build !pure

package infra_test

import (
	"testing"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"

	"github.com/google/uuid"
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
	claimedRequest := claimExecutionJobs(t, store, business.DatasourceDeliveryQueue, "reconcile-worker", 1).GetJobs()
	require.Len(t, claimedRequest, 1)
	require.Equal(t, requestID, claimedRequest[0].GetId())

	deliveryID := enqueueDatasourceOperationJob(t, store, business.DatasourceIngestQueue,
		business.DatasourceSnapshotTopic, business.DatasourceSyncSource, orgID, sourceID, requestID)
	wrongSourceID := enqueueDatasourceOperationJob(t, store, business.DatasourceIngestQueue,
		business.DatasourceSnapshotTopic, business.DatasourceSyncSource, orgID, otherSourceID, requestID)
	deliveries := claimExecutionJobs(t, store, business.DatasourceIngestQueue, "content-worker", 2).GetJobs()
	require.Len(t, deliveries, 2)
	for _, delivery := range deliveries {
		execution := &jobsv1.JobExecutionReference{
			Owner: "other", Kind: "task", Id: "wrong-source",
		}
		if delivery.GetId() == deliveryID {
			execution = &jobsv1.JobExecutionReference{
				Owner: "content", Kind: "task", Id: "task-42",
			}
		} else {
			require.Equal(t, wrongSourceID, delivery.GetId())
		}
		require.NoError(t, store.Complete(testCtx, &jobsv1.CompleteJobRequest{
			Lease: executionLease(delivery), Execution: execution,
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
