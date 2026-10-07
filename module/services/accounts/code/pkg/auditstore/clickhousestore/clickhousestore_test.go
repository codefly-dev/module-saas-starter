package clickhousestore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
)

const day = 24 * time.Hour

func validConfig() Config {
	return Config{Database: "audit", DeploymentID: "deployment-1", EventsRetention: 2555 * day, ContentDetailRetention: 30 * day}
}

// panicConn fails a test that reaches the server: every method of the
// embedded nil interface panics.
type panicConn struct{ Conn }

func TestCreateStatementsOnOneServer(t *testing.T) {
	store, err := New(panicConn{}, validConfig())
	require.NoError(t, err)
	statements := store.CreateStatements()
	require.Len(t, statements, 2)
	require.Equal(t, `CREATE TABLE IF NOT EXISTS audit_events
(
    event_id String,
    deployment_id LowCardinality(String),
    org_id String COMMENT 'Empty for a platform event.',
    actor_id String COMMENT 'Empty when the event names no actor.',
    actor_type LowCardinality(String),
    event_type LowCardinality(String),
    schema_version Int64,
    resource LowCardinality(String) COMMENT 'The target\'s resource type.',
    resource_id String COMMENT 'The target\'s id; empty when none.',
    occurred_at DateTime64(6, 'UTC'),
    ip_address String,
    impersonated_by String,
    is_impersonated Bool,
    client_id String,
    retention_class LowCardinality(String),
    details_sha256 String COMMENT 'Lowercase hex SHA-256 of the canonical details string.',
    details String COMMENT 'Canonical details JSON of a security-class event; empty for a content-class one.'
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (deployment_id, org_id, occurred_at, event_id)
TTL occurred_at + INTERVAL 2555 DAY
COMMENT 'Audit store of record: one row per audit event (ADR 0009).'`, statements[0])
	require.Equal(t, `CREATE TABLE IF NOT EXISTS audit_event_details
(
    event_id String,
    deployment_id LowCardinality(String),
    org_id String COMMENT 'Empty for a platform event.',
    event_type LowCardinality(String),
    occurred_at DateTime64(6, 'UTC'),
    details_sha256 String,
    details String COMMENT 'Canonical details JSON.'
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (deployment_id, org_id, occurred_at, event_id)
TTL occurred_at + INTERVAL 30 DAY
COMMENT 'Full details of content-class audit events, kept for the content window (ADR 0009).'`, statements[1])
}

func TestCreateStatementsOnACluster(t *testing.T) {
	for _, cluster := range []string{"audit_cluster", "{cluster}"} {
		cfg := validConfig()
		cfg.Cluster = cluster
		store, err := New(panicConn{}, cfg)
		require.NoError(t, err)
		for _, statement := range store.CreateStatements() {
			require.Contains(t, statement, " ON CLUSTER '"+cluster+"'\n(")
			require.Contains(t, statement, "\nENGINE = ReplicatedMergeTree\n",
				"replicated under the servers' default replica path and name")
		}
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	_, err := New(nil, validConfig())
	require.ErrorContains(t, err, "connection is required")
	for name, tc := range map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"database":         {func(c *Config) { c.Database = " " }, "database is required"},
		"deployment":       {func(c *Config) { c.DeploymentID = "" }, "deployment id is required"},
		"events window":    {func(c *Config) { c.EventsRetention = 0 }, "events retention must be a whole number of days"},
		"partial day":      {func(c *Config) { c.EventsRetention = 30*day + time.Hour }, "events retention must be a whole number of days"},
		"content window":   {func(c *Config) { c.ContentDetailRetention = time.Hour }, "content detail retention must be a whole number of days"},
		"quoted cluster":   {func(c *Config) { c.Cluster = "a' ON CLUSTER 'b" }, "not a valid cluster name"},
		"macro in a name":  {func(c *Config) { c.Cluster = "x{cluster}" }, "not a valid cluster name"},
		"cluster too long": {func(c *Config) { c.Cluster = strings.Repeat("c", 129) }, "not a valid cluster name"},
	} {
		cfg := validConfig()
		tc.mutate(&cfg)
		_, err := New(panicConn{}, cfg)
		require.ErrorContains(t, err, tc.want, name)
	}
}

func TestConformsStatesTheMismatch(t *testing.T) {
	store, err := New(panicConn{}, validConfig())
	require.NoError(t, err)
	details := store.tableSpecs()[1]
	shape := func() *tableShape {
		columns := map[string]string{}
		for _, column := range details.columns {
			columns[column.Name] = column.Type
		}
		return &tableShape{
			engineFull: "MergeTree PARTITION BY toYYYYMM(occurred_at) ORDER BY (deployment_id, org_id, occurred_at, event_id) " +
				"TTL occurred_at + toIntervalDay(30) SETTINGS index_granularity = 8192",
			columns: columns,
		}
	}
	require.NoError(t, conforms(details, shape()))
	replicated := shape()
	replicated.engineFull = "ReplicatedMergeTree('/clickhouse/tables/{uuid}/{shard}', '{replica}') PARTITION BY toYYYYMM(occurred_at) " +
		"ORDER BY (deployment_id, org_id, occurred_at, event_id) TTL occurred_at + toIntervalDay(30)"
	require.NoError(t, conforms(details, replicated), "a TTL clause at the end, with no SETTINGS after it")

	missing := shape()
	delete(missing.columns, "details_sha256")
	require.EqualError(t, conforms(details, missing), "has no column details_sha256")
	retyped := shape()
	retyped.columns["occurred_at"] = "DateTime64(3, 'UTC')"
	require.EqualError(t, conforms(details, retyped), "column occurred_at is DateTime64(3, 'UTC'), want DateTime64(6, 'UTC')")
	longer := shape()
	longer.engineFull = strings.Replace(longer.engineFull, "toIntervalDay(30)", "toIntervalDay(400)", 1)
	require.EqualError(t, conforms(details, longer), "rows expire after 400 days, the configured window is 30 days")
	none := shape()
	none.engineFull = "MergeTree ORDER BY (deployment_id, org_id, occurred_at, event_id) SETTINGS index_granularity = 8192"
	require.EqualError(t, conforms(details, none), "has no TTL; the configured window is occurred_at + INTERVAL 30 DAY")
	other := shape()
	other.engineFull = strings.Replace(other.engineFull, "TTL occurred_at + toIntervalDay(30)",
		"TTL occurred_at + toIntervalDay(30) DELETE WHERE org_id = ''", 1)
	require.ErrorContains(t, conforms(details, other), "expires rows by")
}

func TestReadsRefuseAnUnscopedReadBeforeReachingTheServer(t *testing.T) {
	store, err := New(panicConn{}, validConfig())
	require.NoError(t, err)
	ctx := context.Background()
	org := "22222222-2222-2222-2222-222222222222"

	_, _, err = store.ListAuditEvents(ctx, business.AuditRead{Query: business.AuditQuery{OrgID: org}})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = store.AggregateAuditEvents(ctx, business.AuditRead{}, business.AuditAggregationSpec{})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = store.ExportAuditEvents(ctx, business.AuditRead{})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = store.LatestSourceSyncEvents(ctx, business.AuditReadScope{}, []string{"source-1"})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)

	_, _, err = store.ListAuditEvents(ctx, business.AuditRead{
		Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: "33333333-3333-3333-3333-333333333333"},
	})
	require.ErrorContains(t, err, "outside the organization", "a query naming another organization than its scope")
	_, _, err = store.ListAuditEvents(ctx, business.AuditRead{Scope: business.PlatformAuditScope(), Query: business.AuditQuery{OrgID: org}})
	require.ErrorContains(t, err, "outside the platform scope")
	_, err = store.LatestSourceSyncEvents(ctx, business.PlatformAuditScope(), []string{"source-1"})
	require.ErrorContains(t, err, "reads one organization")
	_, err = store.AggregateAuditEvents(ctx, business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org}},
		business.AuditAggregationSpec{GroupBy: []string{"category"}})
	require.ErrorContains(t, err, "needs the event-type index", "a category without the index is refused, as auditeval refuses it")

	// A read that can match no event type runs no statement.
	none := business.AuditQuery{OrgID: org, EventType: "saas.auth.login", EventTypes: []string{"saas.org.updated"}}
	entries, next, err := store.ListAuditEvents(ctx, business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: none})
	require.NoError(t, err)
	require.Nil(t, entries)
	require.Empty(t, next)
}

// recordingConn records the batches the store prepares.
type recordingConn struct {
	Conn
	batches []*recordingBatch
	fail    error
}

func (c *recordingConn) PrepareBatch(_ context.Context, query string, _ ...driver.PrepareBatchOption) (driver.Batch, error) {
	batch := &recordingBatch{query: query, fail: c.fail}
	c.batches = append(c.batches, batch)
	return batch, nil
}

type recordingBatch struct {
	driver.Batch
	query   string
	rows    [][]any
	sent    int
	aborted bool
	fail    error
}

func (b *recordingBatch) Append(values ...any) error {
	if b.fail != nil {
		return b.fail
	}
	b.rows = append(b.rows, values)
	return nil
}

func (b *recordingBatch) Send() error  { b.sent++; return nil }
func (b *recordingBatch) Abort() error { b.aborted = true; return nil }
func (b *recordingBatch) IsSent() bool { return b.sent > 0 || b.aborted }

var occurredAt = time.Date(2026, 10, 2, 14, 30, 5, 123456000, time.UTC)

func record(t *testing.T, class business.AuditRetentionClass, org string) business.AuditRecord {
	t.Helper()
	r, err := business.NewAuditRecord(business.AuditEntry{
		ID:            business.NewIDString(),
		OrgID:         org,
		ActorID:       "11111111-1111-1111-1111-111111111111",
		ActorType:     business.ActorTypeUser,
		EventType:     business.EventAuthLogin,
		SchemaVersion: 1,
		Resource:      "session",
		ResourceID:    "session-1",
		Payload:       map[string]any{"method": "password"},
		CreatedAt:     occurredAt,
	}, class)
	require.NoError(t, err)
	return r
}

func TestAppendAuditBatchIsOneInsertPerTable(t *testing.T) {
	conn := &recordingConn{}
	store, err := New(conn, validConfig())
	require.NoError(t, err)
	security := record(t, business.RetentionSecurity, "22222222-2222-2222-2222-222222222222")
	content := record(t, business.RetentionContent, "")
	other := record(t, business.RetentionContent, "22222222-2222-2222-2222-222222222222")
	batch := business.AuditBatch{ID: "batch-1", DeploymentID: "deployment-1", Records: []business.AuditRecord{security, content, other}}
	require.NoError(t, store.AppendAuditBatch(context.Background(), batch))

	require.Len(t, conn.batches, 2, "one insert per table for the whole relay batch, never one per row")
	events, details := conn.batches[0], conn.batches[1]
	require.Equal(t, "INSERT INTO audit_events (event_id, deployment_id, org_id, actor_id, actor_type, event_type, schema_version, "+
		"resource, resource_id, occurred_at, ip_address, impersonated_by, is_impersonated, client_id, retention_class, details_sha256, details)", events.query)
	require.Equal(t, "INSERT INTO audit_event_details (event_id, deployment_id, org_id, event_type, occurred_at, details_sha256, details)", details.query)
	require.Equal(t, 1, events.sent)
	require.Equal(t, 1, details.sent)
	require.Len(t, events.rows, 3, "every record has an events row")
	require.Len(t, details.rows, 2, "only content-class records have a details row")

	require.Equal(t, []any{
		security.Entry.ID, "deployment-1", "22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111", "user",
		"saas.auth.login", int64(1), "session", "session-1", occurredAt, "", "", false, "", "security", security.DetailsSHA256,
		`{"method":"password"}`,
	}, events.rows[0], "a security-class event keeps its details in the events row")
	require.Equal(t, "", events.rows[1][len(events.rows[1])-1], "a content-class events row is content-free")
	require.Equal(t, "", events.rows[1][2], "a platform event has no organization")
	require.Equal(t, []any{content.Entry.ID, "deployment-1", "", "saas.auth.login", occurredAt, content.DetailsSHA256, `{"method":"password"}`}, details.rows[0])

	conn.batches = nil
	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{ID: "batch-2", DeploymentID: "deployment-1",
		Records: []business.AuditRecord{security}}))
	require.Len(t, conn.batches, 1, "a batch without content-class events inserts no details")

	conn.batches, conn.fail = nil, errors.New("block too large")
	err = store.AppendAuditBatch(context.Background(), batch)
	require.ErrorContains(t, err, "block too large")
	require.True(t, conn.batches[0].aborted, "a batch that failed to compose is aborted, never sent partially")
	require.Zero(t, conn.batches[0].sent)
}

// No value a read carries is ever part of a statement's text: every one is a
// server-side parameter.
func TestStatementsCarryNoValues(t *testing.T) {
	store, err := New(panicConn{}, validConfig())
	require.NoError(t, err)
	org := "22222222-2222-2222-2222-222222222222"
	from := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	values := []string{"value-actor", "value-resource", "value-id", "value-client", "value-key", "value-text", "value-type", "value-category"}
	read := business.AuditRead{
		Scope: business.OrganizationAuditScope(org),
		Query: business.AuditQuery{
			OrgID: org, ActorID: "44444444-4444-4444-4444-444444444444", Resource: "value-resource", ResourceID: "value-id",
			ClientID: "value-client", EventTypes: []string{"value-type", "saas.auth.login"}, From: &from,
			PayloadContains: map[string]any{"value-key": "value-text", "flag": true},
		},
		Types: business.AuditEventTypeIndex{"value-type": {Category: "value-category"}},
	}
	m, err := auditeval.NewMatcher(read)
	require.NoError(t, err)
	pl := newPlan(m, read.Query)
	require.NotNil(t, pl.payload, "a filter of strings and booleans is pushed down")

	p := &sqlParams{}
	joined, ok := store.joinedEvents(p, pl, true)
	require.True(t, ok)
	agg, ok, err := store.buildAggregation(pl, business.AuditAggregationSpec{
		GroupBy: []string{"category", "payload:value-key"},
		Metrics: []business.AuditMetric{{Op: "percentile", Field: "payload:value-key", Percentile: 0.5}, {Op: "count_distinct", Field: "category"}},
	}, read.Types)
	require.NoError(t, err)
	require.True(t, ok)
	for _, statement := range []string{joined, agg.statement} {
		for _, value := range append(values, org, "deployment-1", "44444444", "2026") {
			require.NotContains(t, statement, value)
		}
		require.Contains(t, statement, "{p0:String}")
	}
}

func TestParamEscaperKeepsEveryByte(t *testing.T) {
	require.Equal(t, `a\\b\tc\nd\re\0f'g}{h`, paramEscaper.Replace("a\\b\tc\nd\re\x00f'g}{h"))
}

func TestPushablePayload(t *testing.T) {
	for filter, want := range map[string]bool{
		`{"boundary":"b-1"}`:           true,
		`{"flag":true,"note":null}`:    true,
		`{"count":3}`:                  false,
		`{"nested":{"a":"q"}}`:         false,
		`{"tags":["x"]}`:               false,
		`{"":"empty key"}`:             false,
		`{"boundary":"b-1","count":1}`: false,
	} {
		m, err := auditeval.NewMatcher(business.AuditRead{
			Scope: business.PlatformAuditScope(),
			Query: business.AuditQuery{PayloadContains: decode(t, filter)},
		})
		require.NoError(t, err)
		require.Equal(t, want, newPlan(m, business.AuditQuery{}).payload != nil, filter)
	}
}

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	return out
}

// Postgres reads an organization id of any spelling of a uuid as the one value
// it names, and the store keeps the canonical form: the statement of a read
// written otherwise must bind the canonical one.
func TestAnOrganizationIdIsBoundInItsCanonicalForm(t *testing.T) {
	store, err := New(panicConn{}, validConfig())
	require.NoError(t, err)
	canonical := "22222222-aaaa-4000-8000-0000000000ab"
	for _, spelling := range []string{strings.ToUpper(canonical), "{" + canonical + "}", strings.ReplaceAll(canonical, "-", "")} {
		read := business.AuditRead{Scope: business.OrganizationAuditScope(spelling), Query: business.AuditQuery{OrgID: spelling}}
		m, err := auditeval.NewMatcher(read)
		require.NoError(t, err, spelling)
		p := &sqlParams{}
		w, ok := store.eventsWhere(p, newPlan(m, read.Query))
		require.True(t, ok)
		require.Contains(t, w.String(), "org_id = ")
		var bound []any
		for _, arg := range p.args {
			bound = append(bound, arg.(driver.NamedValue).Value)
		}
		require.Contains(t, bound, canonical, spelling)
		require.NotContains(t, bound, spelling, spelling)
	}

	_, err = store.LatestSourceSyncEvents(context.Background(), business.OrganizationAuditScope("org-1"), []string{"source-1"})
	require.ErrorContains(t, err, "not a uuid", "the readable-source query refuses what the other reads refuse, as Postgres does")
}

// A time bound is a query parameter of the column's own type, whose range is
// 1900 to 2299; the driver refuses the Go zero time outright and the server
// has no value for a year before the range. A bound outside the range is
// carried as the edge it clamps to: every stored value lies inside the range,
// so every comparison against it keeps its answer.
func TestATimeBoundOutsideTheColumnRangeIsClamped(t *testing.T) {
	inside := time.Date(2026, 10, 3, 12, 0, 0, 123456000, time.UTC)
	for name, tc := range map[string]struct{ at, want time.Time }{
		"in range":    {inside, inside},
		"zero time":   {time.Time{}, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)},
		"year 1000":   {time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)},
		"unix epoch":  {time.Unix(0, 0), time.Unix(0, 0)},
		"year 9999":   {time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), time.Date(2299, 12, 31, 23, 59, 59, 999999000, time.UTC)},
		"far future":  {time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2299, 12, 31, 23, 59, 59, 999999000, time.UTC)},
		"other zone":  {inside.In(time.FixedZone("x", 3600)), inside},
		"column edge": {time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		p := &sqlParams{}
		require.Equal(t, "{p0:DateTime64(6, 'UTC')}", p.instant(tc.at), name)
		bound := p.args[0].(driver.NamedDateValue)
		require.False(t, bound.Value.IsZero(), "%s: the driver refuses a zero time", name)
		require.True(t, bound.Value.Equal(tc.want), "%s: got %s, want %s", name, bound.Value, tc.want)
	}
}

// On a cluster an insert acknowledged by one replica can be lost with it
// before the others copy it, and the relay deletes its queue rows on the
// acknowledgement; so every insert waits for a majority of replicas, and every
// read refuses a replica that has not caught up with the quorum. On one
// server neither setting means anything and neither is sent.
func TestAClusterInsertsWithAQuorumAndReadsSequentially(t *testing.T) {
	single, err := New(panicConn{}, validConfig())
	require.NoError(t, err)
	require.NotContains(t, single.writeSettings, "insert_quorum")
	require.NotContains(t, single.readSettings, "select_sequential_consistency")

	cfg := validConfig()
	cfg.Cluster = "audit_cluster"
	clustered, err := New(panicConn{}, cfg)
	require.NoError(t, err)
	require.Equal(t, "auto", clustered.writeSettings["insert_quorum"], "a majority of the replicas")
	require.Equal(t, 0, clustered.writeSettings["insert_quorum_parallel"], "sequential quorum inserts, which sequential reads need")
	require.Equal(t, 1, clustered.readSettings["select_sequential_consistency"])
	timeout, ok := clustered.writeSettings["insert_quorum_timeout"].(int)
	require.True(t, ok)
	require.Positive(t, timeout)
	require.Less(t, time.Duration(timeout)*time.Millisecond, 2*time.Minute, "the quorum gives up before the relay's own attempt does")
	for name, value := range single.readSettings {
		require.Equal(t, value, clustered.readSettings[name], "the pinned reading settings are the same on a cluster: %s", name)
	}
	for _, store := range []*Store{single, clustered} {
		require.Equal(t, 0, store.writeSettings["async_insert"], "an acknowledgment must mean the rows reached storage, even under an async user profile")
		require.Equal(t, 1, store.writeSettings["wait_for_async_insert"], "an insert must never acknowledge buffering alone")
	}
}

// catalogueConn answers what Ensure asks of a server's system tables from a
// fixed catalogue, so the engines Ensure accepts and refuses are pinned without
// a server: the database it reports, each table's engine and engine_full, and
// each table's columns. It records every statement Ensure executes, since a
// table found must never be created over.
type catalogueConn struct {
	Conn
	database string
	tables   map[string]catalogueTable
	executed []string
}

type catalogueTable struct {
	engine, engineFull string
	columns            map[string]string
}

func (c *catalogueConn) Exec(_ context.Context, query string, _ ...any) error {
	c.executed = append(c.executed, query)
	return nil
}

func (c *catalogueConn) Query(_ context.Context, query string, args ...any) (driver.Rows, error) {
	table := func() (catalogueTable, bool) {
		if len(args) != 1 {
			return catalogueTable{}, false
		}
		named, ok := args[0].(driver.NamedValue)
		if !ok {
			return catalogueTable{}, false
		}
		found, ok := c.tables[named.Value.(string)]
		return found, ok
	}
	switch {
	case strings.Contains(query, "system.databases"):
		return &catalogueRows{rows: [][]any{{c.database, uint64(1)}}}, nil
	case strings.Contains(query, "system.tables"):
		found, ok := table()
		if !ok {
			return &catalogueRows{}, nil
		}
		return &catalogueRows{rows: [][]any{{found.engine, found.engineFull}}}, nil
	case strings.Contains(query, "system.columns"):
		found, _ := table()
		var rows [][]any
		for name, typ := range found.columns {
			rows = append(rows, []any{name, typ})
		}
		return &catalogueRows{rows: rows}, nil
	}
	return nil, errors.New("catalogueConn: unexpected query: " + query)
}

// catalogueRows scans a row into its destinations from the right: system.tables
// is read as (engine, engine_full) or as engine_full alone, whichever the store
// asks for.
type catalogueRows struct {
	driver.Rows
	rows [][]any
	next int
}

func (r *catalogueRows) Next() bool {
	r.next++
	return r.next <= len(r.rows)
}

func (r *catalogueRows) Scan(dest ...any) error {
	row := r.rows[r.next-1]
	if len(dest) > len(row) {
		return errors.New("catalogueRows: more destinations than columns")
	}
	for i, d := range dest {
		value := row[len(row)-len(dest)+i]
		switch d := d.(type) {
		case *string:
			*d = value.(string)
		case *uint64:
			*d = value.(uint64)
		default:
			return errors.New("catalogueRows: unsupported destination")
		}
	}
	return nil
}

func (r *catalogueRows) Err() error   { return nil }
func (r *catalogueRows) Close() error { return nil }

// existingTables is a catalogue holding both audit tables exactly as the store
// writes them, with the given engine for each.
func existingTables(store *Store, engineFor func(table string) string) *catalogueConn {
	conn := &catalogueConn{database: store.database, tables: map[string]catalogueTable{}}
	for _, spec := range store.tableSpecs() {
		columns := map[string]string{}
		for _, column := range spec.columns {
			columns[column.Name] = column.Type
		}
		engine := engineFor(spec.name)
		engineFull := engine
		if strings.HasPrefix(engine, "Replicated") {
			engineFull += "('/clickhouse/tables/{uuid}/{shard}', '{replica}')"
		}
		engineFull += " PARTITION BY toYYYYMM(occurred_at) ORDER BY (deployment_id, org_id, occurred_at, event_id) " +
			"TTL occurred_at + toIntervalDay(" + strconv.Itoa(spec.days) + ") SETTINGS index_granularity = 8192"
		conn.tables[spec.name] = catalogueTable{engine: engine, engineFull: engineFull, columns: columns}
	}
	return conn
}

func same(engine string) func(string) string { return func(string) string { return engine } }

// A cluster's inserts are made durable by insert_quorum, which counts replicas;
// a MergeTree table lives on one node and has none to count. The relay deletes
// its queue rows on the acknowledgement, so a table found on a cluster that is
// not ReplicatedMergeTree is refused, naming the table and its engine, rather
// than accepted for having the right columns and TTL.
func TestEnsureOnAClusterRequiresEveryTableToBeReplicated(t *testing.T) {
	clustered := validConfig()
	clustered.Cluster = "audit_cluster"

	for name, tc := range map[string]struct {
		cfg     Config
		engines func(table string) string
		refuse  []string
	}{
		"a cluster refuses a MergeTree events table": {
			cfg: clustered, engines: same("MergeTree"),
			refuse: []string{"table audit.audit_events", "engine MergeTree", `cluster "audit_cluster"`, "ReplicatedMergeTree"},
		},
		"a cluster refuses a MergeTree details table beside a replicated events table": {
			cfg: clustered,
			engines: func(table string) string {
				if table == DetailsTable {
					return "MergeTree"
				}
				return "ReplicatedMergeTree"
			},
			refuse: []string{"table audit.audit_event_details", "engine MergeTree"},
		},
		"a cluster refuses a MergeTree variant that is not replicated": {
			cfg: clustered, engines: same("ReplacingMergeTree"),
			refuse: []string{"table audit.audit_events", "engine ReplacingMergeTree"},
		},
		"a cluster refuses an engine that is not a MergeTree at all": {
			cfg: clustered, engines: same("Memory"),
			refuse: []string{"table audit.audit_events", "engine Memory"},
		},
		"a cluster accepts ReplicatedMergeTree": {cfg: clustered, engines: same("ReplicatedMergeTree")},
		"a cluster refuses ReplicatedReplacingMergeTree": {
			cfg: clustered, engines: same("ReplicatedReplacingMergeTree"),
			refuse: []string{"table audit.audit_events", "engine ReplicatedReplacingMergeTree"},
		},
		"one server accepts MergeTree":                  {cfg: validConfig(), engines: same("MergeTree")},
		"one server keeps accepting a replicated table": {cfg: validConfig(), engines: same("ReplicatedMergeTree")},
	} {
		t.Run(name, func(t *testing.T) {
			store, err := New(panicConn{}, tc.cfg)
			require.NoError(t, err)
			conn := existingTables(store, tc.engines)
			store.conn = conn

			err = store.Ensure(context.Background())
			if tc.refuse == nil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				for _, want := range tc.refuse {
					require.ErrorContains(t, err, want)
				}
			}
			require.Empty(t, conn.executed, "a table that exists is never created over")
		})
	}
}

func TestEnsureRejectsTransformingEnginesForEitherTable(t *testing.T) {
	for _, cluster := range []string{"", "audit_cluster"} {
		for _, engine := range []string{"ReplacingMergeTree", "SummingMergeTree", "AggregatingMergeTree", "CollapsingMergeTree", "VersionedCollapsingMergeTree"} {
			for _, prefix := range []string{"", "Replicated"} {
				for _, table := range []string{EventsTable, DetailsTable} {
					t.Run(cluster+"/"+prefix+engine+"/"+table, func(t *testing.T) {
						cfg := validConfig()
						cfg.Cluster = cluster
						store, err := New(panicConn{}, cfg)
						require.NoError(t, err)
						conn := existingTables(store, func(name string) string {
							if name == table {
								return prefix + engine
							}
							return "ReplicatedMergeTree"
						})
						store.conn = conn
						err = store.Ensure(context.Background())
						require.ErrorContains(t, err, "table audit."+table+": engine "+prefix+engine)
						require.Empty(t, conn.executed)
					})
				}
			}
		}
	}
}
