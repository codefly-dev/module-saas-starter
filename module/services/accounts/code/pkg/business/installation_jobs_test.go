package business_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
)

// HOST-INST-002, HOST-INST-005: retry runs are distinct jobs, but the solution
// sees exactly the same installation/revision identity and no stored credential.
func TestInstallationStepJobIdentity(t *testing.T) {
	step := business.InstallationStep{InstallationID: moduleUserA, OrganizationID: moduleTenantA, Revision: 7}
	initial, err := business.NewInstallationStepJob(step, "initial")
	require.NoError(t, err)
	retry, err := business.NewInstallationStepJob(step, "retry-one")
	require.NoError(t, err)
	require.NotEqual(t, initial.Job.IdempotencyKey, retry.Job.IdempotencyKey)
	require.Equal(t, initial.Job.Payload, retry.Job.Payload)
	require.Equal(t, uint32(business.InstallationStepMaxAttempts), initial.Job.MaxAttempts)
	require.NoError(t, jobs.ValidateCommand(initial))
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(initial.Job.Payload, &decoded))
	require.Len(t, decoded, 3)
	require.Equal(t, step.InstallationID, decoded["installation_id"])
	require.Equal(t, step.OrganizationID, initial.Job.Scope.GetOrganizationId())
	require.Equal(t, step.InstallationID, initial.Job.Attributes["codefly.installation_id"])
	require.Equal(t, step.OrganizationID, initial.Job.Attributes["codefly.organization_id"])
	require.Equal(t, "7", initial.Job.Attributes["codefly.installation_revision"])
}

// HOST-INST-004: only an explicit refusal is permanent, and its reason is
// bounded in bytes without corrupting UTF-8. Transport text is never persisted.
func TestInstallationStepOutcomes(t *testing.T) {
	for _, status := range []int{204, 409, 503, 0, 200, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			backend := &fakeJobBackend{}
			svc := newModuleService(t, backend)
			lease := &jobsv1.JobLeaseReference{JobId: moduleUserA, WorkerId: "example-worker", LeaseToken: moduleUserB}
			before := time.Now()
			require.NoError(t, svc.FinishInstallationStep(context.Background(), lease, 3, status, "Example safe reason"))
			switch status {
			case 204:
				require.Len(t, backend.completed, 1)
				require.Empty(t, backend.retried)
				require.Empty(t, backend.deadLetter)
			case 409:
				require.Len(t, backend.deadLetter, 1)
				require.Equal(t, "Example safe reason", backend.deadLetter[0].Failure.Message)
			default:
				require.Len(t, backend.retried, 1)
				require.Equal(t, business.InstallationStepFailure, backend.retried[0].Failure.Message)
				require.WithinDuration(t, before.Add(20*time.Second), backend.retried[0].RetryAt.AsTime(), time.Second)
			}
		})
	}
	for _, input := range []string{"", "\xff", strings.Repeat("é", 512)} {
		reason := business.BoundedInstallationReason(input)
		require.NotEmpty(t, reason)
		require.True(t, utf8.ValidString(reason))
		require.LessOrEqual(t, len(reason), business.InstallationFailureLimit)
	}
}
