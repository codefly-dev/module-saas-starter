//go:build pure

package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkloadPrefixLogsItsUnderlyingRefusalWithoutCredential(t *testing.T) {
	const credential = "test-workload-credential-not-for-logs"
	for name, tc := range map[string]struct {
		reviewer   ExecutionReviewer
		credential string
		reason     string
	}{
		"no reviewer":         {reason: "no execution reviewer wired"},
		"no credential":       {reviewer: &fakeExecutionReviewer{}, reason: "no credential presented"},
		"review failed":       {reviewer: &fakeExecutionReviewer{reviewErr: errors.New("native attestation expired: " + credential)}, credential: credential, reason: "native attestation expired"},
		"empty review":        {reviewer: &fakeExecutionReviewer{}, credential: credential, reason: "reviewer returned no workload identity"},
		"undeclared workload": {reviewer: &fakeExecutionReviewer{reviewed: &ReviewedExecutionToken{Namespace: "example", ServiceAccount: "api"}}, credential: credential, reason: "example/api is declared by 0 principals"},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureBusinessLogs(t)
			svc := reviewService()
			svc.executionReviewer = tc.reviewer
			_, err := svc.ModulePrefixOfWorkload(context.Background(), tc.credential)
			require.ErrorContains(t, err, tc.reason)
			require.Contains(t, logs(), "workload credential refused")
			require.Contains(t, logs(), tc.reason)
			require.NotContains(t, logs(), credential)
		})
	}
}
