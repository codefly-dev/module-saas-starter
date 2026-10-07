package clickhousestore

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The history copy writes an event again when a failed details write left only
// its events row (business.AuditHistoryCopy). The adapter must make that write
// complete the copy already stored: the append of the event adds an events row
// and a details row, and the read-back attaches the details to every events row
// of the event by its id, so no copy is left without them.
func TestServerAnEventWrittenAgainAfterAFailedDetailsWriteIsCompleteInEveryCopy(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))

	org, actor := uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	content := newRecord(t, org, actor, business.EventDatasourceSourceSynced, "source-1", at, business.RetentionContent, map[string]any{"n": 1})
	security := newRecord(t, org, actor, business.EventAuthLogin, "session-1", at, business.RetentionSecurity, map[string]any{"method": "password"})
	readBack := func() map[string][]business.StoredAuditEvent {
		out := map[string][]business.StoredAuditEvent{}
		require.NoError(t, store.ReadStoredAuditEvents(ctx, at.Add(-time.Hour), at.Add(time.Hour), func(got business.StoredAuditEvent) error {
			out[got.Entry.ID] = append(out[got.Entry.ID], got)
			return nil
		}))
		return out
	}

	// The first write: the events rows landed, the details write failed.
	events, _ := BatchRows(business.AuditBatch{DeploymentID: testDeployment, Records: []business.AuditRecord{content, security}})
	refused, err := store.insert(ctx, EventsTable, EventsColumns(), events)
	require.NoError(t, err)
	require.Empty(t, refused)
	stored := readBack()
	require.Len(t, stored[content.Entry.ID], 1)
	require.False(t, stored[content.Entry.ID][0].HasDetails, "the details write was lost")
	require.True(t, stored[security.Entry.ID][0].HasDetails, "a security-class event carries its details in its events row")

	// The write again: the content-class event's events row and details row.
	appendBatch(t, store, testDeployment, content)
	stored = readBack()
	require.Len(t, stored[content.Entry.ID], 2, "the events row written again is another copy")
	for _, got := range stored[content.Entry.ID] {
		require.True(t, got.HasDetails, "the details attach to every copy of the event by its id")
		require.Equal(t, content.DetailsSHA256, got.DetailsSHA256)
		require.Equal(t, content.DetailsSHA256, business.AuditDetailsSHA256(got.Details))
	}
	require.Len(t, stored[security.Entry.ID], 1)
}
