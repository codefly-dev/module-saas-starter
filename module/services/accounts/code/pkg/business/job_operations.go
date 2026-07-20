package business

import (
	"context"
	"errors"
	"fmt"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

var ErrJobOperationsUnavailable = errors.New("job operations are not configured")

func (s *Service) GetJobOperations(
	ctx context.Context,
	actorID string,
	request *jobsv1.GetJobOperationsRequest,
) (*jobsv1.GetJobOperationsResponse, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, fmt.Errorf("permission denied: %w", err)
	}
	if s.jobOperations == nil {
		return nil, ErrJobOperationsUnavailable
	}
	return s.jobOperations.GetJobOperations(ctx, request)
}

func (s *Service) ListJobs(
	ctx context.Context,
	actorID string,
	request *jobsv1.ListJobsRequest,
) (*jobsv1.ListJobsResponse, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, fmt.Errorf("permission denied: %w", err)
	}
	if s.jobOperations == nil {
		return nil, ErrJobOperationsUnavailable
	}
	return s.jobOperations.ListJobs(ctx, request)
}

func (s *Service) GetJob(
	ctx context.Context,
	actorID string,
	request *jobsv1.GetJobRequest,
) (*jobsv1.GetJobResponse, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, fmt.Errorf("permission denied: %w", err)
	}
	if s.jobOperations == nil {
		return nil, ErrJobOperationsUnavailable
	}
	return s.jobOperations.GetJob(ctx, request)
}

func (s *Service) ReplayJob(
	ctx context.Context,
	actorID string,
	request *jobsv1.ReplayJobRequest,
) (*jobsv1.ReplayJobResponse, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, fmt.Errorf("permission denied: %w", err)
	}
	if s.jobOperations == nil {
		return nil, ErrJobOperationsUnavailable
	}
	response, err := s.jobOperations.ReplayJob(ctx, request)
	if err != nil {
		return nil, err
	}
	// Exact retries resolve to the original replay and must not duplicate the
	// success audit record.
	if response.GetDisposition() == jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED {
		s.emit(ctx, actorID, "user", "job.replayed", "job", response.GetJobId(), "")
	}
	return response, nil
}
