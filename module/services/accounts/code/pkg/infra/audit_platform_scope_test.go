//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// A platform-wide audit read returns the platform's own events (recorded with no
// organization) and never an organization's. It used to return every tenant's.
func TestPlatformWideAuditReadReturnsOnlyPlatformEvents(t *testing.T) {
	actor := seedUser(t)
	org := seedOrg(t, actor)
	marker := "platform-scope-" + business.NewIDString()
	for _, orgID := range []string{org, ""} {
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.InsertAuditEvent(ctx, business.AuditEntry{
				ID: business.NewIDString(), ActorID: actor, ActorType: "user", EventType: business.EventScopeGranted,
				SchemaVersion: 1, Resource: marker, ResourceID: marker, OrgID: orgID,
				Payload: map[string]any{"scope_path": "x"},
			})
		}))
	}
	var rows []business.AuditEntry
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		rows, _, _, err = testStore.QueryAuditLog(ctx, business.AuditQuery{PlatformEventsOnly: true, Resource: marker, PageSize: 10})
		return err
	}))
	require.Len(t, rows, 1, "only the event recorded with no organization")
	require.Empty(t, rows[0].OrgID)
}
