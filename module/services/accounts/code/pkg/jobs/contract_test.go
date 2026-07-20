package jobs_test

import (
	"bytes"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGeneratedJobEnvelopeValidation(t *testing.T) {
	validator, err := protovalidate.New()
	require.NoError(t, err)

	valid := func() *jobsv1.JobEnvelope {
		return &jobsv1.JobEnvelope{
			Id:        uuid.NewString(),
			Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
				OrganizationId: uuid.NewString(),
			}},
			Queue:          "email.transactional",
			Topic:          "email.welcome.v1",
			Source:         "saas-starter/accounts",
			IdempotencyKey: uuid.NewString(),
			SchemaVersion:  1,
			Payload:        []byte(`{"template":"welcome"}`),
			ContentType:    "application/json",
			Attributes:     map[string]string{"trace_id": "trace-1"},
			State:          jobsv1.JobState_JOB_STATE_PENDING,
			MaxAttempts:    8,
		}
	}

	require.NoError(t, validator.Validate(valid()))

	tests := []struct {
		name   string
		mutate func(*jobsv1.JobEnvelope)
	}{
		{name: "missing direction", mutate: func(job *jobsv1.JobEnvelope) {
			job.Direction = jobsv1.JobDirection_JOB_DIRECTION_UNSPECIFIED
		}},
		{name: "missing scope", mutate: func(job *jobsv1.JobEnvelope) {
			job.Scope = nil
		}},
		{name: "unset scope oneof", mutate: func(job *jobsv1.JobEnvelope) {
			job.Scope = &jobsv1.JobScope{}
		}},
		{name: "false global scope", mutate: func(job *jobsv1.JobEnvelope) {
			job.Scope = &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: false}}
		}},
		{name: "unspecified state", mutate: func(job *jobsv1.JobEnvelope) {
			job.State = jobsv1.JobState_JOB_STATE_UNSPECIFIED
		}},
		{name: "oversized payload", mutate: func(job *jobsv1.JobEnvelope) {
			job.Payload = bytes.Repeat([]byte{'x'}, 1048577)
		}},
		{name: "invalid replay id", mutate: func(job *jobsv1.JobEnvelope) {
			job.ReplayOf = "not-a-uuid"
		}},
		{name: "invalid attribute key", mutate: func(job *jobsv1.JobEnvelope) {
			job.Attributes = map[string]string{"Not Canonical": "value"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := valid()
			test.mutate(job)
			require.Error(t, validator.Validate(job))
		})
	}
}

func TestGeneratedJobExecutionCommandValidation(t *testing.T) {
	validator, err := protovalidate.New()
	require.NoError(t, err)

	validLease := func() *jobsv1.JobLeaseReference {
		return &jobsv1.JobLeaseReference{
			JobId: uuid.NewString(), WorkerId: "worker-a", LeaseToken: uuid.NewString(),
		}
	}

	require.NoError(t, validator.Validate(&jobsv1.ClaimJobsRequest{
		Queue: "email.transactional", WorkerId: "worker-a", Limit: 25,
		LeaseDuration: durationpb.New(time.Minute),
	}))
	require.NoError(t, validator.Validate(&jobsv1.HeartbeatJobRequest{
		Lease: validLease(), Extension: durationpb.New(time.Minute),
	}))
	require.NoError(t, validator.Validate(&jobsv1.RetryJobRequest{
		Lease: validLease(), Failure: &jobsv1.JobFailure{Code: "email.transient"},
		RetryAt: timestamppb.Now(),
	}))
	require.NoError(t, validator.Validate(&jobsv1.DeadLetterJobRequest{
		Lease: validLease(), Failure: &jobsv1.JobFailure{Code: "email.permanent"},
	}))

	require.Error(t, validator.Validate(&jobsv1.ClaimJobsRequest{
		Queue: "Not Canonical", WorkerId: "worker-a", Limit: 1,
		LeaseDuration: durationpb.New(time.Minute),
	}))
	require.Error(t, validator.Validate(&jobsv1.ClaimJobsRequest{
		Queue: "email.transactional", WorkerId: "worker-a", Limit: 101,
		LeaseDuration: durationpb.New(time.Minute),
	}))
	require.Error(t, validator.Validate(&jobsv1.ClaimJobsRequest{
		Queue: "email.transactional", WorkerId: "worker-a", Limit: 1,
		LeaseDuration: durationpb.New(2 * time.Hour),
	}))
	require.Error(t, validator.Validate(&jobsv1.ClaimJobsRequest{
		Queue: "email.transactional", WorkerId: "worker-a", Limit: 1,
		LeaseDuration: durationpb.New(time.Nanosecond),
	}))
	require.Error(t, validator.Validate(&jobsv1.CompleteJobRequest{}))
	require.Error(t, validator.Validate(&jobsv1.RetryJobRequest{
		Lease: validLease(), RetryAt: timestamppb.Now(),
	}))
}
