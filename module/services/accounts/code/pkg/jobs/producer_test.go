package jobs_test

import (
	"strings"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCanonicalOrderingKeyIsStableAndCollisionFree(t *testing.T) {
	key := &jobsv1.JobOrderingKey{
		Namespace: "account", Components: []string{"tenant/a", "resource.1"},
	}
	first, err := jobs.CanonicalOrderingKey(key)
	require.NoError(t, err)
	second, err := jobs.CanonicalOrderingKey(proto.Clone(key).(*jobsv1.JobOrderingKey))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, "account:dGVuYW50L2E.cmVzb3VyY2UuMQ", first)

	joined, err := jobs.CanonicalOrderingKey(&jobsv1.JobOrderingKey{
		Namespace: "account", Components: []string{"a.b"},
	})
	require.NoError(t, err)
	separate, err := jobs.CanonicalOrderingKey(&jobsv1.JobOrderingKey{
		Namespace: "account", Components: []string{"a", "b"},
	})
	require.NoError(t, err)
	require.NotEqual(t, joined, separate)

	none, err := jobs.CanonicalOrderingKey(nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestCanonicalOrderingKeyRejectsInvalidOrOversizedInput(t *testing.T) {
	_, err := jobs.CanonicalOrderingKey(&jobsv1.JobOrderingKey{
		Namespace: "Not Canonical", Components: []string{"resource"},
	})
	require.ErrorIs(t, err, jobs.ErrInvalidCommand)

	components := make([]string, 8)
	for index := range components {
		components[index] = strings.Repeat("x", 128)
	}
	_, err = jobs.CanonicalOrderingKey(&jobsv1.JobOrderingKey{
		Namespace: "ordering", Components: components,
	})
	require.ErrorIs(t, err, jobs.ErrOrderingKeyTooLong)
}

func TestEnqueueFingerprintIsDeterministicAndSemantic(t *testing.T) {
	request := validEnqueueRequest()
	request.Job.Attributes = map[string]string{"trace_id": "trace", "region": "us-east"}
	first, err := jobs.EnqueueFingerprint(request)
	require.NoError(t, err)

	reordered := proto.Clone(request).(*jobsv1.EnqueueJobRequest)
	reordered.Job.Attributes = map[string]string{"region": "us-east", "trace_id": "trace"}
	second, err := jobs.EnqueueFingerprint(reordered)
	require.NoError(t, err)
	require.Equal(t, first, second, "map insertion order is not semantic")

	changed := proto.Clone(request).(*jobsv1.EnqueueJobRequest)
	changed.Job.Payload = []byte(`{"different":true}`)
	different, err := jobs.EnqueueFingerprint(changed)
	require.NoError(t, err)
	require.NotEqual(t, first, different)

	_, err = jobs.EnqueueFingerprint(&jobsv1.EnqueueJobRequest{})
	require.ErrorIs(t, err, jobs.ErrInvalidCommand)
}

func TestReplayFingerprintIsTypeSeparatedAndSemantic(t *testing.T) {
	request := &jobsv1.ReplayJobRequest{
		SourceJobId: uuid.NewString(), IdempotencyKey: "operator-retry-1",
	}
	first, err := jobs.ReplayFingerprint(request)
	require.NoError(t, err)
	second, err := jobs.ReplayFingerprint(proto.Clone(request).(*jobsv1.ReplayJobRequest))
	require.NoError(t, err)
	require.Equal(t, first, second)

	changed := proto.Clone(request).(*jobsv1.ReplayJobRequest)
	changed.IdempotencyKey = "operator-retry-2"
	different, err := jobs.ReplayFingerprint(changed)
	require.NoError(t, err)
	require.NotEqual(t, first, different)

	_, err = jobs.ReplayFingerprint(&jobsv1.ReplayJobRequest{})
	require.ErrorIs(t, err, jobs.ErrInvalidCommand)
}

func TestPayloadIdentityMatchesOriginalAndExactReplay(t *testing.T) {
	original := &jobsv1.JobEnvelope{IdempotencyKey: "product-event-1"}
	require.True(t, jobs.PayloadIdentityMatches(original, "product-event-1"))
	require.False(t, jobs.PayloadIdentityMatches(original, "different-event"))

	replay := &jobsv1.JobEnvelope{
		IdempotencyKey: "operator-replay-1",
		ReplayOf:       uuid.NewString(),
	}
	require.True(t, jobs.PayloadIdentityMatches(replay, "product-event-1"))
	require.False(t, jobs.PayloadIdentityMatches(replay, ""))
	require.False(t, jobs.PayloadIdentityMatches(nil, "product-event-1"))
}

func validEnqueueRequest() *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
			OrganizationId: uuid.NewString(),
		}},
		Queue: "email.transactional", Topic: "email.welcome.v1",
		Source: "accounts", IdempotencyKey: uuid.NewString(),
		Ordering: &jobsv1.JobOrderingKey{
			Namespace: "account", Components: []string{uuid.NewString()},
		},
		SchemaVersion: 1, Payload: []byte(`{"template":"welcome"}`),
		ContentType: "application/json", MaxAttempts: 8,
	}}
}
