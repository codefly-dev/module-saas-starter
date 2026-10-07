package clickhousestore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
)

type historyConn struct {
	Conn
	details, events [][]any
	statements      []string
}

func (c *historyConn) Query(_ context.Context, statement string, _ ...any) (driver.Rows, error) {
	c.statements = append(c.statements, statement)
	rows := c.events
	if strings.Contains(statement, "FROM "+DetailsTable) {
		rows = c.details
	}
	return &historyRows{rows: rows}, nil
}

type historyRows struct {
	driver.Rows
	rows [][]any
	next int
}

func (r *historyRows) Next() bool { r.next++; return r.next <= len(r.rows) }
func (r *historyRows) Scan(dest ...any) error {
	row := r.rows[r.next-1]
	if len(dest) != len(row) {
		return fmt.Errorf("historyRows: %d destinations for %d columns", len(dest), len(row))
	}
	for i, into := range dest {
		reflect.ValueOf(into).Elem().Set(reflect.ValueOf(row[i]))
	}
	return nil
}
func (r *historyRows) Close() error { return nil }
func (r *historyRows) Err() error   { return nil }

func historyTestStore(t *testing.T, conn Conn, limit int64) *Store {
	t.Helper()
	cfg := validConfig()
	cfg.ExportMaxBytes = limit
	s, err := New(conn, cfg)
	require.NoError(t, err)
	return s
}

func historyEnvelope(id, text string) []any {
	return []any{"deployment-1", id, boundOrg, "actor", "user", "example.read", int64(1), "document", "doc-1", occurredAt,
		"", "", false, "", string(business.RetentionContent), business.AuditDetailsSHA256(text), ""}
}

func TestHistoryDetailsAreBoundedWithoutBuildingServerArrays(t *testing.T) {
	c := &historyConn{}
	for i := 0; i < 8; i++ {
		text := strings.Repeat("p", 900)
		c.details = append(c.details, []any{fmt.Sprint(i), business.AuditDetailsSHA256(text), text})
	}
	visited := 0
	err := historyTestStore(t, c, 2048).ReadStoredAuditEvents(t.Context(), occurredAt.Add(-time.Second), occurredAt.Add(time.Second), func(business.StoredAuditEvent) error {
		visited++
		return nil
	})
	require.ErrorIs(t, err, business.ErrAuditHistoryWindowTooLarge)
	require.Zero(t, visited)
	require.Len(t, c.statements, 1)
	require.NotContains(t, c.statements[0], "groupArray", "the driver must never decode an unbounded details array")
}

func TestHistoryIdenticalDetailsCopiesFitAndCorruptCopiesRemainVisible(t *testing.T) {
	text := `{"pad":"` + strings.Repeat("p", 500) + `"}`
	sha := business.AuditDetailsSHA256(text)
	c := &historyConn{events: [][]any{historyEnvelope("event-1", text)}}
	for i := 0; i < 100; i++ {
		c.details = append(c.details, []any{"event-1", sha, text})
	}
	s := historyTestStore(t, c, 4096)
	read := func() business.StoredAuditEvent {
		var got business.StoredAuditEvent
		require.NoError(t, s.ReadStoredAuditEvents(t.Context(), occurredAt.Add(-time.Second), occurredAt.Add(time.Second), func(event business.StoredAuditEvent) error {
			got = event
			return nil
		}))
		return got
	}
	require.Equal(t, text, read().Details)
	c.details = append(c.details, []any{"event-1", sha, `{"pad":"corrupt"}`}, []any{"event-1", sha, text})
	err := s.ReadStoredAuditEvents(t.Context(), occurredAt.Add(-time.Second), occurredAt.Add(time.Second), func(business.StoredAuditEvent) error { return nil })
	require.ErrorContains(t, err, "do not match their stored hash")
}

func TestHistoryPropagatesTheCallersWindowBudgetSignal(t *testing.T) {
	c := &historyConn{events: [][]any{historyEnvelope("event-1", "{}")}}
	err := historyTestStore(t, c, 4096).ReadStoredAuditEvents(t.Context(), occurredAt.Add(-time.Second), occurredAt.Add(time.Second), func(business.StoredAuditEvent) error {
		return business.ErrAuditHistoryWindowTooLarge
	})
	require.ErrorIs(t, err, business.ErrAuditHistoryWindowTooLarge)
}
