package clickhousestore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"

	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/stretchr/testify/require"
)

// What AppendAuditBatch says about a failure is what the relay acts on: it sets
// a row aside only on a business.PermanentRowRejection, and retries everything
// else. These pin which ClickHouse answers become one, against a connection that
// answers as a server does: a block is refused whole, with a code, and says
// nothing of which row is to blame.

// blameConn is a server that refuses any block holding a row for which
// answer(table, rowID) returns an error, naming nothing of the row.
type blameConn struct {
	Conn
	answer func(table, eventID string) error
	// stored is the event ids each table accepted, and sends counts the blocks
	// the server was sent.
	stored map[string][]string
	sends  int
}

func (c *blameConn) PrepareBatch(_ context.Context, query string, _ ...driver.PrepareBatchOption) (driver.Batch, error) {
	table := EventsTable
	if query == insertStatement(DetailsTable, DetailsColumns()) {
		table = DetailsTable
	}
	return &blameBatch{conn: c, table: table}, nil
}

func (c *blameConn) ids(table string) []string {
	ids := slices.Clone(c.stored[table])
	sort.Strings(ids)
	return slices.Compact(ids)
}

type blameBatch struct {
	driver.Batch
	conn  *blameConn
	table string
	rows  [][]any
	sent  bool
	// unencodable is the event ids Append refuses, as the driver refuses a value
	// it cannot convert to the column's type.
	unencodable map[string]bool
}

func (b *blameBatch) Append(values ...any) error {
	if b.unencodable[values[0].(string)] {
		return &proto.BlockError{Op: "AppendRow", ColumnName: "schema_version", Err: errors.New("converting string to Int64 is unsupported")}
	}
	b.rows = append(b.rows, values)
	return nil
}

func (b *blameBatch) Send() error {
	b.conn.sends++
	for _, row := range b.rows {
		if b.conn.answer == nil {
			continue
		}
		if err := b.conn.answer(b.table, row[0].(string)); err != nil {
			return err
		}
	}
	if b.conn.stored == nil {
		b.conn.stored = map[string][]string{}
	}
	for _, row := range b.rows {
		b.conn.stored[b.table] = append(b.conn.stored[b.table], row[0].(string))
	}
	b.sent = true
	return nil
}

func (b *blameBatch) Abort() error { b.sent = true; return nil }
func (b *blameBatch) IsSent() bool { return b.sent }

func exception(code int32) error {
	return &clickhouse.Exception{Code: code, Name: "DB::Exception", Message: fmt.Sprintf("code %d", code)}
}

func recordsOf(t *testing.T, n int, class business.AuditRetentionClass) ([]business.AuditRecord, []string) {
	t.Helper()
	records := make([]business.AuditRecord, n)
	ids := make([]string, n)
	for i := range records {
		records[i] = record(t, class, "")
		ids[i] = records[i].Entry.ID
	}
	return records, ids
}

func refusedIDs(err error) []string {
	var ids []string
	for _, rejection := range business.PermanentRowRejections(err) {
		ids = append(ids, rejection.EventID)
	}
	return ids
}

func TestEveryRowDefectCodeIsAKnownErrorAndFindsTheRowToBlame(t *testing.T) {
	for code, name := range rowDefectCodes {
		records, ids := recordsOf(t, 9, business.RetentionSecurity)
		conn := &blameConn{answer: func(_, id string) error {
			if id == ids[4] {
				return exception(code)
			}
			return nil
		}}
		store, err := New(conn, validConfig())
		require.NoError(t, err)

		err = store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

		require.Equal(t, []string{ids[4]}, refusedIDs(err), "%d %s", code, name)
		want := slices.Clone(ids)
		want = slices.Delete(want, 4, 5)
		sort.Strings(want)
		require.Equal(t, want, conn.ids(EventsTable), "%d %s: every other row is written while the row to blame is found", code, name)
		require.Less(t, conn.sends, 2*len(records), "%d %s: the search is a bisection, not a write per row", code, name)
	}
}

func TestSeveralRowsRefusedForTheirContentAreAllFound(t *testing.T) {
	records, ids := recordsOf(t, 20, business.RetentionContent)
	bad := []string{ids[0], ids[7], ids[8], ids[19]}
	conn := &blameConn{answer: func(table, id string) error {
		if slices.Contains(bad, id) {
			return exception(41)
		}
		return nil
	}}
	store, err := New(conn, validConfig())
	require.NoError(t, err)

	err = store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.ElementsMatch(t, bad, refusedIDs(err))
	require.Len(t, conn.ids(EventsTable), 16)
	require.Len(t, conn.ids(DetailsTable), 16, "the details of the rows that were not refused are written")
	for _, id := range bad {
		require.NotContains(t, conn.ids(DetailsTable), id, "an event refused in the events table is not given details")
	}
}

func TestACodeThatIsNotARowDefectNeverSearchesAndNeverRefusesARow(t *testing.T) {
	for name, failure := range map[string]error{
		"network":               exception(210),
		"socket timeout":        exception(209),
		"timeout exceeded":      exception(159),
		"memory":                exception(241),
		"readonly":              exception(164),
		"table readonly":        exception(242),
		"no space":              exception(243),
		"too many parts":        exception(252),
		"too many queries":      exception(202),
		"too few live replicas": exception(285),
		"quorum previous write": exception(286),
		"unknown insert status": exception(319),
		"keeper":                exception(999),
		"unknown table":         exception(60),
		"access denied":         exception(497),
		"authentication":        exception(516),
		"block row limit":       exception(158),
		"block byte limit":      exception(396),
		"a code nobody listed":  exception(9999),
		"deadline":              context.DeadlineExceeded,
		"closed connection":     errors.New("write tcp 10.0.0.2:9000: broken pipe"),
		"driver batch error":    clickhouse.ErrBatchInvalid,
		"wrong value count":     &proto.BlockError{Op: "Append", Err: errors.New("expected 17 arguments, got 5")},
	} {
		for _, size := range []int{1, 12} {
			records, _ := recordsOf(t, size, business.RetentionSecurity)
			conn := &blameConn{answer: func(string, string) error { return failure }}
			store, err := New(conn, validConfig())
			require.NoError(t, err)

			err = store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

			require.Error(t, err, name)
			require.Empty(t, refusedIDs(err), "%s, batch of %d: a failure that is not about a row's content refuses no row", name, size)
			require.Equal(t, 1, conn.sends, "%s, batch of %d: and nothing is split to look for one", name, size)
			require.ErrorIs(t, err, failure)
		}
	}
}

// A single row that fails with a code that is not a defect is not refused even
// when a defect failed the block it was in: the search stops at the first
// failure that says nothing of a row.
func TestASearchStopsAtAFailureThatIsNotARowDefect(t *testing.T) {
	records, ids := recordsOf(t, 8, business.RetentionSecurity)
	conn := &blameConn{answer: func(_, id string) error {
		switch id {
		case ids[1]:
			return exception(41)
		case ids[6]:
			return exception(285)
		}
		return nil
	}}
	store, err := New(conn, validConfig())
	require.NoError(t, err)

	err = store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Equal(t, []string{ids[1]}, refusedIDs(err), "the row refused for its content before the failure is still named")
	require.ErrorContains(t, err, "code 285", "and the failure that stopped the search is returned for the relay to retry")
}

// A block that is refused as a whole for a reason of its own — its size — is
// not blamed on a row when every half of it is accepted.
func TestABlockRefusedWithADefectCodeWhoseRowsAreAllAcceptedAloneRefusesNothing(t *testing.T) {
	records, ids := recordsOf(t, 10, business.RetentionSecurity)
	conn := &blameConn{}
	store, err := New(&sizeLimitedConn{blameConn: conn, limit: 3}, validConfig())
	require.NoError(t, err)

	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records}))

	sort.Strings(ids)
	require.Equal(t, ids, conn.ids(EventsTable), "every row is written")
}

// sizeLimitedConn refuses a block of more than limit rows with TOO_LARGE_STRING_SIZE.
type sizeLimitedConn struct {
	*blameConn
	limit int
}

func (c *sizeLimitedConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	inner, err := c.blameConn.PrepareBatch(ctx, query, opts...)
	if err != nil {
		return nil, err
	}
	return &limitedBatch{blameBatch: inner.(*blameBatch), limit: c.limit}, nil
}

type limitedBatch struct {
	*blameBatch
	limit int
}

func (b *limitedBatch) Send() error {
	if len(b.rows) > b.limit {
		b.conn.sends++
		return exception(131)
	}
	return b.blameBatch.Send()
}

func TestARowTheDriverCannotEncodeIsRefusedBeforeAnythingIsSent(t *testing.T) {
	records, ids := recordsOf(t, 6, business.RetentionContent)
	conn := &encodingConn{blameConn: &blameConn{}, unencodable: map[string]bool{ids[2]: true, ids[3]: true}}
	store, err := New(conn, validConfig())
	require.NoError(t, err)

	err = store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.ElementsMatch(t, []string{ids[2], ids[3]}, refusedIDs(err), "the driver's error names the row being appended")
	require.Len(t, conn.ids(EventsTable), 4, "the others are sent, in a block without them")
	require.Len(t, conn.ids(DetailsTable), 4)
}

type encodingConn struct {
	*blameConn
	unencodable map[string]bool
}

func (c *encodingConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	inner, err := c.blameConn.PrepareBatch(ctx, query, opts...)
	if err != nil {
		return nil, err
	}
	inner.(*blameBatch).unencodable = c.unencodable
	return inner, nil
}
