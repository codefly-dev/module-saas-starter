//go:build !pure

package infra_test

import (
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/operations"

	"github.com/stretchr/testify/require"
)

// The adapter regressions prove that verified scope refusals enter the existing
// owner emitter. Exercise that same constructor/emitter with the real tenant
// pool here, so an in-memory Emit capture alone cannot stand for persistence.
func TestSourceOperationRefusalAuditPersists(t *testing.T) {
	actor := seedUser(t)
	org := seedOrg(t, actor)
	foreign := seedOrg(t, actor)
	source := business.NewIDString()
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetAuditEmitter(emitter)
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
	svc.AuditSourceOperation(ctx, actor, org, source, operations.Declaration{}, "refused-effect", "refused")
	rows, _, total, err := svc.QueryAuditLog(ctx, business.AuditQuery{OrgID: org, EventType: string(business.EventDatasourceOperationInvoked), ResourceID: source})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	event := rows[0]
	require.NotEmpty(t, event.ID)
	require.False(t, event.CreatedAt.IsZero())
	require.Equal(t, business.EventDatasourceOperationInvoked, event.EventType)
	require.Equal(t, org, event.OrgID)
	require.Equal(t, actor, event.ActorID)
	require.Equal(t, "user", event.ActorType)
	require.Equal(t, "datasource", event.Resource)
	require.Equal(t, source, event.ResourceID)
	require.Equal(t, map[string]any{"operation": "unknown", "effect": "UNKNOWN", "effect_id": "refused-effect", "outcome": "refused"}, event.Payload)
	// Read through the other tenant's transaction: the refusal must not appear.
	rows, _, total, err = svc.QueryAuditLog(auth.WithVerifiedDatabaseIdentity(testCtx, actor, foreign), business.AuditQuery{OrgID: foreign, EventType: string(business.EventDatasourceOperationInvoked), ResourceID: source})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, rows)
}
