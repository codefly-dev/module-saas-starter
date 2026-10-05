//go:build !pure

package infra_test

import (
	"accounts/pkg/auditops"
	"accounts/pkg/business"
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAuditQualificationPrepareUsesRuntimeAuthorityAndTransactionalQueue(t *testing.T) {
	orgA, orgB := seedOrg(t, seedUser(t)), seedOrg(t, seedUser(t))
	values := map[string]string{"AUDIT_SINK": "bigquery", "AUDIT_BIGQUERY_PROJECT": "example-project", "AUDIT_BIGQUERY_DATASET": "audit", "AUDIT_ARCHIVE_URL": "gs://example-audit-archive", "AUDIT_DEPLOYMENT_ID": "deployment-1", "AUDIT_CONTENT_RETENTION_DAYS": "90"}
	getenv := func(key string) string { return values[key] }
	var out, stderr bytes.Buffer
	args := []string{"-json", "-prepare", "-run-id", uuid.NewString(), "-organization", orgA, "-organization", orgB}
	require.Zero(t, auditops.RunQualification(args, getenv, &out, &stderr), stderr.String()+out.String())
	var receipt auditops.QualificationReceipt
	require.NoError(t, json.Unmarshal(out.Bytes(), &receipt))
	require.Len(t, receipt.Events, 6)
	require.Equal(t, "passed", receipt.Checks["transaction_queue"])
	require.Equal(t, "pending", receipt.Checks["queue_drained"])
	require.Empty(t, receipt.ErrorCode)
	ids := []string{}
	for _, e := range receipt.Events {
		ids = append(ids, e.ID)
	}
	pool := auditRelayPool(t)
	var n int
	require.NoError(t, pool.QueryRow(testCtx, `SELECT count(*) FROM audit_event_queue WHERE id=ANY($1::uuid[])`, ids).Scan(&n))
	require.Equal(t, 6, n)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		counts, err := testStore.CountAuditHistory(ctx, receipt.WindowFrom, receipt.WindowTo)
		require.NoError(t, err)
		for _, count := range counts {
			require.Zero(t, count)
		}
		return nil
	}))
	// The receipt's platform event and both organizations were queued together;
	// no tee/job fan-out was invoked by the record-only probe.
	require.Equal(t, business.RetentionSecurity, receipt.Events[0].Retention)
	out.Reset()
	stderr.Reset()
	missingArgs := []string{"-json", "-prepare", "-run-id", uuid.NewString(), "-organization", orgA, "-organization", uuid.NewString()}
	require.Equal(t, 1, auditops.RunQualification(missingArgs, getenv, &out, &stderr))
	require.NoError(t, json.Unmarshal(out.Bytes(), &receipt))
	require.Equal(t, "fixture_transaction_failed", receipt.ErrorCode)
	ids = nil
	for _, e := range receipt.Events {
		ids = append(ids, e.ID)
	}
	require.NoError(t, pool.QueryRow(testCtx, `SELECT count(*) FROM audit_event_queue WHERE id=ANY($1::uuid[])`, ids).Scan(&n))
	require.Zero(t, n, "a missing organization writes no fixtures")
}
