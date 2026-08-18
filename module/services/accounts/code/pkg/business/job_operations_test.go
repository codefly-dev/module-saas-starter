package business_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/stretchr/testify/require"
)

type jobOperationsBusinessStore struct {
	business.Store
	roles map[string]string
}

func (store *jobOperationsBusinessStore) GetPlatformRole(_ context.Context, userID string) (string, error) {
	return store.roles[userID], nil
}

type recordingJobOperations struct {
	mu sync.Mutex

	operationsCalls int
	listCalls       int
	getCalls        int
	replayCalls     int
	replayResponses []*jobsv1.ReplayJobResponse
	replayErr       error
}

func (operations *recordingJobOperations) GetJobOperations(context.Context, *jobsv1.GetJobOperationsRequest) (*jobsv1.GetJobOperationsResponse, error) {
	operations.mu.Lock()
	defer operations.mu.Unlock()
	operations.operationsCalls++
	return &jobsv1.GetJobOperationsResponse{}, nil
}

func (operations *recordingJobOperations) ListJobs(context.Context, *jobsv1.ListJobsRequest) (*jobsv1.ListJobsResponse, error) {
	operations.mu.Lock()
	defer operations.mu.Unlock()
	operations.listCalls++
	return &jobsv1.ListJobsResponse{}, nil
}

func (operations *recordingJobOperations) GetJob(context.Context, *jobsv1.GetJobRequest) (*jobsv1.GetJobResponse, error) {
	operations.mu.Lock()
	defer operations.mu.Unlock()
	operations.getCalls++
	return &jobsv1.GetJobResponse{}, nil
}

func (operations *recordingJobOperations) ReplayJob(context.Context, *jobsv1.ReplayJobRequest) (*jobsv1.ReplayJobResponse, error) {
	operations.mu.Lock()
	defer operations.mu.Unlock()
	operations.replayCalls++
	if operations.replayErr != nil {
		return nil, operations.replayErr
	}
	response := operations.replayResponses[0]
	operations.replayResponses = operations.replayResponses[1:]
	return response, nil
}

type recordingAuditEmitter struct {
	mu      sync.Mutex
	entries []business.AuditEntry
}

func (emitter *recordingAuditEmitter) Emit(_ context.Context, entry business.AuditEntry) {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.entries = append(emitter.entries, entry)
}

func newJobOperationsService(t *testing.T, roles map[string]string, operations *recordingJobOperations) *business.Service {
	t.Helper()
	service, err := business.NewService(&jobOperationsBusinessStore{roles: roles})
	require.NoError(t, err)
	if operations != nil {
		service.SetJobOperations(operations)
	}
	return service
}

func TestJobOperationsRequireSuperAdminBeforeCallingGlobalStore(t *testing.T) {
	operations := &recordingJobOperations{}
	service := newJobOperationsService(t, map[string]string{
		"support-user": "support",
		"billing-user": "billing",
	}, operations)

	_, err := service.GetJobOperations(context.Background(), "support-user", &jobsv1.GetJobOperationsRequest{})
	require.ErrorContains(t, err, "permission denied")
	_, err = service.ListJobs(context.Background(), "billing-user", &jobsv1.ListJobsRequest{})
	require.ErrorContains(t, err, "permission denied")
	_, err = service.GetJob(context.Background(), "ordinary-user", &jobsv1.GetJobRequest{})
	require.ErrorContains(t, err, "permission denied")
	_, err = service.ReplayJob(context.Background(), "ordinary-user", &jobsv1.ReplayJobRequest{})
	require.ErrorContains(t, err, "permission denied")

	require.Zero(t, operations.operationsCalls)
	require.Zero(t, operations.listCalls)
	require.Zero(t, operations.getCalls)
	require.Zero(t, operations.replayCalls)
}

func TestJobOperationsFailClosedWhenGlobalStoreIsNotWired(t *testing.T) {
	service := newJobOperationsService(t, map[string]string{"admin-user": "super_admin"}, nil)

	_, err := service.GetJobOperations(context.Background(), "admin-user", &jobsv1.GetJobOperationsRequest{})
	require.ErrorIs(t, err, business.ErrJobOperationsUnavailable)
	_, err = service.ListJobs(context.Background(), "admin-user", &jobsv1.ListJobsRequest{})
	require.ErrorIs(t, err, business.ErrJobOperationsUnavailable)
	_, err = service.GetJob(context.Background(), "admin-user", &jobsv1.GetJobRequest{})
	require.ErrorIs(t, err, business.ErrJobOperationsUnavailable)
	_, err = service.ReplayJob(context.Background(), "admin-user", &jobsv1.ReplayJobRequest{})
	require.ErrorIs(t, err, business.ErrJobOperationsUnavailable)
}

func TestReplayJobAuditsOnlyTheNewDurableReplay(t *testing.T) {
	operations := &recordingJobOperations{replayResponses: []*jobsv1.ReplayJobResponse{
		{
			JobId:       "65f68586-d5a9-447b-a1d5-66610d05d989",
			Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
		},
		{
			JobId:       "65f68586-d5a9-447b-a1d5-66610d05d989",
			Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE,
		},
	}}
	service := newJobOperationsService(t, map[string]string{"admin-user": "super_admin"}, operations)
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)
	request := &jobsv1.ReplayJobRequest{
		SourceJobId:    "ab5e3aa8-8fa7-4d99-b8ce-4118d3916af4",
		IdempotencyKey: "c1221aad-3668-4d5e-9886-217136544266", //gitleaks:allow -- deterministic non-secret fixture
	}

	inserted, err := service.ReplayJob(context.Background(), "admin-user", request)
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED, inserted.GetDisposition())
	duplicate, err := service.ReplayJob(context.Background(), "admin-user", request)
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE, duplicate.GetDisposition())

	require.Len(t, audit.entries, 1)
	require.Equal(t, business.AuditEntry{
		ActorID:    "admin-user",
		ActorType:  "user",
		EventType:  business.EventJobReplayed,
		Resource:   "job",
		ResourceID: "65f68586-d5a9-447b-a1d5-66610d05d989",
	}, audit.entries[0])
}

func TestReplayJobDoesNotAuditFailedOperation(t *testing.T) {
	operations := &recordingJobOperations{replayErr: errors.New("replay failed")}
	service := newJobOperationsService(t, map[string]string{"admin-user": "super_admin"}, operations)
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)

	_, err := service.ReplayJob(context.Background(), "admin-user", &jobsv1.ReplayJobRequest{})
	require.ErrorContains(t, err, "replay failed")
	require.Empty(t, audit.entries)
}
