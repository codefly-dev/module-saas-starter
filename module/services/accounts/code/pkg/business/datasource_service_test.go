package business_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// TestAddGitHubSource_EmitsAudit proves the connection surface writes the audit
// event its method_policy declares (datasource.source_added) — the compliance
// contract the sidecar catalog advertises. Before this, the policy declared the
// event but no code emitted it, so the audit trail was silently empty.
func TestAddGitHubSource_EmitsAudit(t *testing.T) {
	clearData(t)
	ctx := testCtx
	userID, orgID := mustUserAndOrg(t, ctx, "ds-add@audit-test.com", "ds-add-audit", "DS Audit Add")

	ds, err := testService.AddGitHubSource(ctx, userID, business.AddGitHubSourceInput{
		OrgID:      orgID,
		Repo:       "octocat/hello-world",
		Paths:      []string{"docs"},
		Collection: "wiki",
		Credential: "ghp_token",
	})
	require.NoError(t, err)
	require.NotEmpty(t, ds.ID)

	events := queryDatasourceAudit(t, orgID, "datasource.source_added")
	require.Len(t, events, 1)
	require.Equal(t, userID, events[0].ActorID)
	require.Equal(t, ds.ID, events[0].ResourceID)
	require.Equal(t, "octocat/hello-world", events[0].Payload["repo"])
	require.Equal(t, "wiki", events[0].Payload["collection"])
}

// TestSyncDatasource_EmitsAudit proves Sync writes datasource.sync_requested,
// and that a no-op sync (unknown id) records nothing.
func TestSyncDatasource_EmitsAudit(t *testing.T) {
	clearData(t)
	ctx := testCtx
	userID, orgID := mustUserAndOrg(t, ctx, "ds-sync@audit-test.com", "ds-sync-audit", "DS Audit Sync")

	ds, err := testService.AddGitHubSource(ctx, userID, business.AddGitHubSourceInput{
		OrgID: orgID, Repo: "octocat/hello-world", Collection: "wiki", Credential: "ghp_token",
	})
	require.NoError(t, err)

	synced, err := testService.SyncDatasource(ctx, userID, orgID, ds.ID)
	require.NoError(t, err)
	require.Equal(t, business.DatasourceSyncStatusPending, synced.SyncStatus)

	events := queryDatasourceAudit(t, orgID, "datasource.sync_requested")
	require.Len(t, events, 1)
	require.Equal(t, userID, events[0].ActorID)
	require.Equal(t, ds.ID, events[0].ResourceID)

	// A sync against an unknown id is a no-op: not-found, and no audit event.
	_, err = testService.SyncDatasource(ctx, userID, orgID, business.NewIDString())
	require.Error(t, err)
	after := queryDatasourceAudit(t, orgID, "datasource.sync_requested")
	require.Len(t, after, 1)
}

func queryDatasourceAudit(t *testing.T, orgID, eventType string) []business.AuditEntry {
	t.Helper()
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)
	entries, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID: orgID, EventType: eventType, From: &past, To: &future, PageSize: 100,
	})
	require.NoError(t, err)
	return entries
}
