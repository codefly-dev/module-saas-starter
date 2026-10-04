package clickhousestore

import (
	"context"
	"encoding/json"
	"errors"
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
