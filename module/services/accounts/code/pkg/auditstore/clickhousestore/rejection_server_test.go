package clickhousestore

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A row a real server refuses for its content, and a failure of another kind,
// against the exception codes and types the driver really returns. Skipped
// without a server; see server_test.go.

func TestServerIsolatesARowTheTableRefusesAndNothingElse(t *testing.T) {
	ctx := context.Background()
	conn, database := serverDatabase(t)
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))
	// A deployment's own CHECK constraint: the one way this schema has a row's
	// content refused by the server.
	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+EventsTable+" ADD CONSTRAINT no_forbidden CHECK resource_id != 'forbidden'"))

	org, actor := uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	var records []business.AuditRecord
	for _, resourceID := range []string{"a", "b", "forbidden", "c", "d", "forbidden", "e"} {
		records = append(records, newRecord(t, org, actor, business.EventAuthLogin, resourceID, at, business.RetentionContent, map[string]any{"k": "v"}))
	}

	err := store.AppendAuditBatch(ctx, business.AuditBatch{ID: uuid.NewString(), DeploymentID: testDeployment, ComposedAt: time.Now(), Records: records})

	require.ElementsMatch(t, []string{records[2].Entry.ID, records[5].Entry.ID}, refusedIDs(err))
	var exception *clickhouse.Exception
	require.ErrorAs(t, err, &exception)
	require.EqualValues(t, 469, exception.Code, "the code the server really gives a refused CHECK")
	var events, details uint64
	require.NoError(t, store.queryRow(ctx, "SELECT count() FROM "+EventsTable, nil, &events))
	require.NoError(t, store.queryRow(ctx, "SELECT count() FROM "+DetailsTable, nil, &details))
	require.EqualValues(t, 5, events, "the rows around the refused ones are written")
	// The details go first, so those of the two events the events table then refused
	// are already there: read by nothing, since no read starts from the details
	// table, and held by the archive and the quarantine as well.
	require.EqualValues(t, 7, details)

	// A failure that is not about a row: a table that is gone refuses nothing.
	require.NoError(t, conn.Exec(ctx, "DROP TABLE "+DetailsTable))
	err = store.AppendAuditBatch(ctx, business.AuditBatch{ID: uuid.NewString(), DeploymentID: testDeployment, ComposedAt: time.Now(),
		Records: []business.AuditRecord{records[0], records[1]}})
	require.Error(t, err)
	require.Empty(t, refusedIDs(err))
	require.ErrorAs(t, err, &exception)
	require.EqualValues(t, 60, exception.Code, "UNKNOWN_TABLE, as the server gives it")
}
