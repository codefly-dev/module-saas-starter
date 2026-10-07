package bigquerystore_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// The history copy writes an event again when a failed details write left only
// its events row (business.AuditHistoryCopy). The adapter must make that write
// complete the copy already stored: the append of the event adds an events row
// and a details row, and the read-back attaches the details to every events row
// of the event by its id, so no copy is left without them.
func TestAnEventWrittenAgainAfterAFailedDetailsWriteIsCompleteInEveryCopy(t *testing.T) {
	w := newWarehouse(t)
	at := now.Add(-time.Hour)
	content := event(t, 1, orgA, business.EventDatasourceSourceSynced, business.RetentionContent, at, map[string]any{"n": 1})
	security := event(t, 2, orgA, business.EventAuthLogin, business.RetentionSecurity, at, map[string]any{"method": "password"})

	// The first write: the events rows landed, the details write failed.
	events, _ := bigquerystore.BatchRows(business.AuditBatch{ID: "first", DeploymentID: deployment, Records: []business.AuditRecord{content, security}})
	for _, row := range events {
		require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(project, dataset, bigquerystore.EventsTable), row.Values))
	}
	stored := w.historyReadBack(t, at)
	require.Len(t, stored[content.Entry.ID], 1)
	require.False(t, stored[content.Entry.ID][0].HasDetails, "the details write was lost")
	require.True(t, stored[security.Entry.ID][0].HasDetails, "a security-class event carries its details in its events row")

	// The write again: the content-class event's events row and details row.
	w.append(t, deployment, content)
	stored = w.historyReadBack(t, at)
	require.Len(t, stored[content.Entry.ID], 2, "the events row written again is another copy")
	for _, got := range stored[content.Entry.ID] {
		require.True(t, got.HasDetails, "the details attach to every copy of the event by its id")
		require.Equal(t, content.DetailsSHA256, got.DetailsSHA256)
		require.Equal(t, content.DetailsSHA256, business.AuditDetailsSHA256(got.Details))
	}
	require.Len(t, stored[security.Entry.ID], 1)
}

// historyReadBack is every stored copy of every event in the hour around at, by id.
func (w *warehouse) historyReadBack(t *testing.T, at time.Time) map[string][]business.StoredAuditEvent {
	t.Helper()
	out := map[string][]business.StoredAuditEvent{}
	require.NoError(t, w.reader.ReadStoredAuditEvents(context.Background(), at.Add(-time.Hour), at.Add(time.Hour), func(got business.StoredAuditEvent) error {
		out[got.Entry.ID] = append(out[got.Entry.ID], got)
		return nil
	}))
	return out
}
