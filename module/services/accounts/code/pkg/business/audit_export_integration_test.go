package business_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// TestExternalTeeCommitsExportJobInAuditTx exercises the real Postgres path: an
// org-scoped Emit with the external tee enabled must land BOTH the audit row and
// its audit_export outbox job, proving the enqueue shares the audit transaction
// (a fake store can't prove same-tx atomicity). Reads the job through the
// worker-role pool, the only role granted job_messages access.
func TestExternalTeeCommitsExportJobInAuditTx(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available (run with codefly test)")
	}
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-tee@rls-test.com", "alice-tee-rls", "Acme Tee A")

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore, business.WithExternalTee())
	require.NoError(t, err)
	defer emitter.Close()

	id := business.NewIDString()
	emitter.Emit(ctx, business.AuditEntry{
		ID: id, ActorType: "user", EventType: "session.revoked", Resource: "test", OrgID: orgA,
	})

	now := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	events, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID: orgA, EventType: "session.revoked", From: &past, To: &now, PageSize: 100,
	})
	require.NoError(t, err)
	require.Len(t, events, 1, "audit row must commit")

	worker, err := infra.NewJobWorkerPool(ctx)
	require.NoError(t, err)
	t.Cleanup(worker.Close)

	var count int
	require.NoError(t, worker.QueryRow(ctx, `
		SELECT COUNT(*) FROM job_messages WHERE queue = $1 AND idempotency_key = $2`,
		business.AuditExportQueue, id,
	).Scan(&count))
	require.Equal(t, 1, count, "export job must commit in the same tx as the audit row")
}
