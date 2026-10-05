package clickhousestore

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The adapter against a ClickHouse server, when one is reachable:
//
//	docker run --rm -p 9000:9000 -e CLICKHOUSE_USER=<user> -e CLICKHOUSE_PASSWORD=<password> \
//	    clickhouse/clickhouse-server:25.8
//	AUDIT_CLICKHOUSE_TEST_DSN=clickhouse://<user>:<password>@127.0.0.1:9000/default \
//	    go test ./pkg/auditstore/clickhousestore
//
// It is skipped otherwise. Each test creates its own database — the store
// itself never creates one; the deployment does — and drops it afterwards.
// The same variable enrolls ClickHouse in the read-parity suite against
// Postgres (pkg/business/audit_store_parity_test.go).

const testDeployment = "deployment-1"

// serverDatabase creates a database for one test and connects to it.
func serverDatabase(t *testing.T) (driver.Conn, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AUDIT_CLICKHOUSE_TEST_DSN"))
	if dsn == "" {
		t.Skip("AUDIT_CLICKHOUSE_TEST_DSN is not set; see the comment above for how to run against a ClickHouse server")
	}
	options, err := clickhouse.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := clickhouse.Open(options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	database := "audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, admin.Exec(context.Background(), "CREATE DATABASE "+database))
	t.Cleanup(func() { _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database) })

	scoped := *options
	scoped.Auth.Database = database
	conn, err := clickhouse.Open(&scoped)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, database
}

func serverStore(t *testing.T, conn driver.Conn, database string, mutate ...func(*Config)) *Store {
	t.Helper()
	cfg := Config{
		Database:               database,
		DeploymentID:           testDeployment,
		EventsRetention:        2555 * 24 * time.Hour,
		ContentDetailRetention: 30 * 24 * time.Hour,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	store, err := New(conn, cfg)
	require.NoError(t, err)
	return store
}

func TestServerEnsureRefusesAMissingDatabase(t *testing.T) {
	conn, database := serverDatabase(t)
	require.NoError(t, conn.Exec(context.Background(), "DROP DATABASE "+database))
	store := serverStore(t, conn, database)
	require.ErrorContains(t, store.Ensure(context.Background()), "does not exist; the deployment creates it")
}

func TestServerEnsureCreatesBothTablesOnceAndRefusesAMismatch(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))
	require.NoError(t, store.Ensure(ctx), "a second start finds both tables and accepts them")

	for table, days := range map[string]string{EventsTable: "2555", DetailsTable: "30"} {
		var engine string
		require.NoError(t, store.queryRow(ctx, "SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = '"+table+"'", nil, &engine))
		require.Contains(t, engine, "MergeTree PARTITION BY toYYYYMM(occurred_at) ORDER BY (deployment_id, org_id, occurred_at, event_id)", table)
		require.Contains(t, engine, "TTL occurred_at + toIntervalDay("+days+")", table)
	}

	other := serverStore(t, conn, database, func(cfg *Config) { cfg.ContentDetailRetention = 400 * 24 * time.Hour })
	require.ErrorContains(t, other.Ensure(ctx), "rows expire after 30 days, the configured window is 400 days",
		"a details table expiring at another window is refused, never silently kept")
	other = serverStore(t, conn, database, func(cfg *Config) { cfg.EventsRetention = 3650 * 24 * time.Hour })
	require.ErrorContains(t, other.Ensure(ctx), "table "+database+".audit_events: rows expire after 2555 days")

	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+DetailsTable+" MODIFY COLUMN details Nullable(String)"))
	require.ErrorContains(t, store.Ensure(ctx), "column details is Nullable(String), want String")
	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+DetailsTable+" MODIFY COLUMN details String"))
	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+DetailsTable+" REMOVE TTL"))
	require.ErrorContains(t, store.Ensure(ctx), "has no TTL")
	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+DetailsTable+" MODIFY TTL toDateTime(occurred_at) + toIntervalDay(30)"))
	require.ErrorContains(t, store.Ensure(ctx), "expires rows by")
	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+DetailsTable+" MODIFY TTL occurred_at + INTERVAL 30 DAY"))
	require.NoError(t, store.Ensure(ctx), "the table is as the store writes it again")
	require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+DetailsTable+" DROP COLUMN details_sha256"))
	require.ErrorContains(t, store.Ensure(ctx), "has no column details_sha256")
}

// fixture is a set of records, the batches they were appended in, and the
// reference reads over them.
type fixture struct {
	org, otherOrg string
	actor         string
	records       []business.AuditRecord
}

func newRecord(t *testing.T, org, actor string, eventType business.EventType, resourceID string, at time.Time, class business.AuditRetentionClass, payload map[string]any) business.AuditRecord {
	t.Helper()
	record, err := business.NewAuditRecord(business.AuditEntry{
		ID:            uuid.NewString(),
		OrgID:         org,
		ActorID:       actor,
		ActorType:     business.ActorTypeUser,
		EventType:     eventType,
		SchemaVersion: 1,
		Resource:      "datasource",
		ResourceID:    resourceID,
		Payload:       payload,
		CreatedAt:     at,
	}, class)
	require.NoError(t, err)
	return record
}

func appendBatch(t *testing.T, store *Store, deployment string, records ...business.AuditRecord) {
	t.Helper()
	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{
		ID: uuid.NewString(), DeploymentID: deployment, ComposedAt: time.Now(), Records: records,
	}))
}

// reference is the read answered by auditeval over the records themselves,
// once each: the semantics every store reproduces.
func (f *fixture) referenceList(t *testing.T, read business.AuditRead) ([]business.AuditEntry, string) {
	t.Helper()
	m, err := auditeval.NewMatcher(read)
	require.NoError(t, err)
	page, err := auditeval.NewPage(read.Query)
	require.NoError(t, err)
	for _, event := range f.events() {
		ok, err := m.Match(event)
		require.NoError(t, err)
		if ok {
			page.Offer(event)
		}
	}
	events, next := page.Result()
	return results(events), next
}

func (f *fixture) referenceAggregate(t *testing.T, read business.AuditRead, spec business.AuditAggregationSpec) []business.AuditAggregateBucket {
	t.Helper()
	m, err := auditeval.NewMatcher(read)
	require.NoError(t, err)
	aggregator, err := auditeval.NewAggregator(spec, read.Types)
	require.NoError(t, err)
	for _, event := range f.events() {
		ok, err := m.Match(event)
		require.NoError(t, err)
		if ok {
			require.NoError(t, aggregator.Add(event))
		}
	}
	return aggregator.Buckets()
}

func (f *fixture) events() []*auditeval.Event {
	out := make([]*auditeval.Event, len(f.records))
	for i, record := range f.records {
		entry := record.Entry
		entry.Payload = nil
		entry.CreatedAt = time.UnixMicro(entry.CreatedAt.UnixMicro())
		out[i] = &auditeval.Event{Entry: entry, Details: record.Details, HasDetails: true, DetailsSHA256: record.DetailsSHA256}
	}
	return out
}

func TestServerReadsMatchTheReferenceSemantics(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))

	f := &fixture{org: uuid.NewString(), otherOrg: uuid.NewString(), actor: uuid.NewString()}
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	tricky := "doc\\1\t'x'}{p0:String}\n"
	security, content := business.RetentionSecurity, business.RetentionContent
	f.records = []business.AuditRecord{
		newRecord(t, f.org, f.actor, business.EventAuthLogin, "", base, security, map[string]any{"method": "password", "flag": true}),
		newRecord(t, f.org, f.actor, business.EventDocumentRead, tricky, base.Add(-time.Hour), content, map[string]any{"boundary": "b-1", "duration_ms": 12.5, "count": 3}),
		newRecord(t, f.org, "", business.EventDocumentRead, "doc-2", base.Add(-2*time.Hour), content, map[string]any{"boundary": "b\\2", "duration_ms": "7.25", "note": nil}),
		// An integer beyond 64 bits: valid JSON, which ClickHouse's parser refuses.
		newRecord(t, f.org, f.actor, business.EventDocumentRead, "doc-3", base.Add(-3*time.Hour), content, map[string]any{"boundary": "b-1", "big": jsonNumber("123456789012345678901234567890"), "duration_ms": 4}),
		newRecord(t, f.org, f.actor, business.EventDocumentRead, "doc-4", base.Add(-30*time.Hour), content, map[string]any{"boundary": "b-3", "nested": map[string]any{"zz": 1, "a": []any{"q"}}, "duration_ms": jsonNumber("1.50")}),
		newRecord(t, f.org, f.actor, business.EventOrgUpdated, "", base.Add(-50*time.Hour), security, map[string]any{"name": "First"}),
		newRecord(t, f.otherOrg, f.actor, business.EventDocumentRead, "doc-1", base.Add(-time.Hour), content, map[string]any{"boundary": "b-1"}),
		newRecord(t, "", f.actor, business.EventAuthLogin, "", base.Add(-10*time.Minute), security, map[string]any{"method": "passkey"}),
	}
	appendBatch(t, store, testDeployment, f.records[:4]...)
	appendBatch(t, store, testDeployment, f.records[4:]...)
	appendBatch(t, store, testDeployment, f.records[1:3]...)              // delivered again
	appendBatch(t, store, "deployment-other", f.records[0], f.records[5]) // another deployment's copy
	foreign := newRecord(t, f.org, f.actor, business.EventAuthLogin, "", base, security, map[string]any{"method": "password"})
	appendBatch(t, store, "deployment-other", foreign)

	var parts uint64
	require.NoError(t, store.queryRow(ctx, "SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'audit_events' AND active", nil, &parts))
	require.LessOrEqual(t, parts, uint64(5), "every append is one insert, one part, per table")

	types := business.AuditEventTypeIndex{
		string(business.EventAuthLogin):    {Category: "auth", Namespace: "saas"},
		string(business.EventDocumentRead): {Category: "document", Namespace: "saas"},
		string(business.EventOrgUpdated):   {Category: "organization", Namespace: "saas"},
	}
	week := base.Add(-48 * time.Hour)
	queries := map[string]business.AuditQuery{
		"organization":      {OrgID: f.org},
		"actor":             {OrgID: f.org, ActorID: f.actor},
		"types":             {OrgID: f.org, EventTypes: []string{string(business.EventDocumentRead), string(business.EventOrgUpdated)}},
		"category":          {OrgID: f.org, Category: "document"},
		"tricky resource":   {OrgID: f.org, ResourceID: tricky},
		"payload string":    {OrgID: f.org, PayloadContains: map[string]any{"boundary": "b-1"}},
		"payload escaped":   {OrgID: f.org, PayloadContains: map[string]any{"boundary": "b\\2"}},
		"payload bool":      {OrgID: f.org, PayloadContains: map[string]any{"flag": true}},
		"payload on a type": {OrgID: f.org, EventType: string(business.EventAuthLogin), PayloadContains: map[string]any{"method": "password"}},
		"payload null":      {OrgID: f.org, PayloadContains: map[string]any{"note": nil}},
		"payload number":    {OrgID: f.org, PayloadContains: map[string]any{"count": 3}},
		"payload nested":    {OrgID: f.org, PayloadContains: map[string]any{"nested": map[string]any{"a": []any{"q"}}}},
		"payload big":       {OrgID: f.org, PayloadContains: map[string]any{"big": jsonNumber("123456789012345678901234567890")}},
		"window":            {OrgID: f.org, From: &week, To: &base},
		"platform":          {},
		"platform for one":  {ActorID: f.actor},
	}
	specs := map[string]business.AuditAggregationSpec{
		"default":  {},
		"category": {GroupBy: []string{"category", "actor"}, Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "category"}, {Op: "count_distinct", Field: "actor_id"}}},
		"week":     {GroupBy: []string{"time"}, Bucket: "week", Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "resource_id"}}},
		"text":     {GroupBy: []string{"payload:boundary", "payload:method"}, Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "payload:count"}}},
		"nested":   {GroupBy: []string{"payload:nested"}},
		"fraction": {GroupBy: []string{"payload:duration_ms"}},
		"numbers": {GroupBy: []string{"event_type"}, Metrics: []business.AuditMetric{
			{Op: "count", Alias: "n"},
			{Op: "sum", Field: "payload:duration_ms"},
			{Op: "avg", Field: "payload:duration_ms"},
			{Op: "min", Field: "payload:duration_ms"},
			{Op: "max", Field: "payload:count"},
			{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.3, Alias: "p30"},
		}, Derived: []business.AuditDerivedMetric{{Alias: "ratio", Numerator: "sum_duration_ms", Denominator: "n"}}},
	}
	inService := 0
	store.inService = func() { inService++ }
	// Which aggregations ClickHouse runs, and which the service does: the
	// answers are compared below either way.
	for _, tc := range []struct {
		query, spec string
		want        int
	}{
		{"organization", "default", 0},
		{"tricky resource", "numbers", 0},
		{"payload on a type", "week", 0},
		{"payload string", "week", 1}, // a candidate ClickHouse cannot parse
		{"tricky resource", "fraction", 1},
		{"tricky resource", "nested", 0},
		{"organization", "numbers", 1},
		{"payload number", "default", 1},
	} {
		before := inService
		q := queries[tc.query]
		_, err := store.AggregateAuditEvents(ctx, business.AuditRead{Scope: business.OrganizationAuditScope(q.OrgID), Query: q, Types: types}, specs[tc.spec])
		require.NoError(t, err)
		require.Equal(t, tc.want, inService-before, "%s / %s", tc.query, tc.spec)
	}

	for name, q := range queries {
		scope := business.OrganizationAuditScope(q.OrgID)
		if q.OrgID == "" {
			scope = business.PlatformAuditScope()
		}
		for _, size := range []int32{0, 1, 2} {
			q := q
			q.PageSize = size
			for pages := 0; ; pages++ {
				read := business.AuditRead{Scope: scope, Query: q, Types: types}
				want, wantNext := f.referenceList(t, read)
				got, gotNext, err := store.ListAuditEvents(ctx, read)
				require.NoError(t, err, name)
				require.Equal(t, want, got, "%s, page size %d, page %d", name, size, pages)
				require.Equal(t, wantNext, gotNext, name)
				if wantNext == "" {
					break
				}
				q.PageToken = wantNext
			}
		}
		read := business.AuditRead{Scope: scope, Query: q, Types: types}
		all := read
		all.Query.PageSize = 1000
		want, _ := f.referenceList(t, all)
		got, err := store.ExportAuditEvents(ctx, read)
		require.NoError(t, err, name)
		require.Equal(t, want, got, "export %s", name)

		for specName, spec := range specs {
			want := f.referenceAggregate(t, read, spec)
			got, err := store.AggregateAuditEvents(ctx, read, spec)
			require.NoError(t, err, "%s / %s", name, specName)
			require.Equal(t, want, got, "%s / %s", name, specName)
		}
	}

	t.Run("scope", func(t *testing.T) {
		_, _, err := store.ListAuditEvents(ctx, business.AuditRead{Query: business.AuditQuery{OrgID: f.org}})
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, _, err = store.ListAuditEvents(ctx, business.AuditRead{Scope: business.OrganizationAuditScope(f.org), Query: business.AuditQuery{OrgID: f.otherOrg}})
		require.ErrorContains(t, err, "outside the")
		_, err = store.LatestSourceSyncEvents(ctx, business.PlatformAuditScope(), []string{"doc-1"})
		require.ErrorContains(t, err, "one organization")
	})

	t.Run("history", func(t *testing.T) {
		copies := map[string]int{}
		err := store.ReadStoredAuditEvents(ctx, base.Add(-100*time.Hour), base.Add(time.Microsecond), func(event business.StoredAuditEvent) error {
			require.Equal(t, testDeployment, event.DeploymentID)
			copies[event.Entry.ID]++
			for _, record := range f.records {
				if record.Entry.ID == event.Entry.ID {
					require.Equal(t, record.Retention, event.Retention)
					require.Equal(t, record.DetailsSHA256, event.DetailsSHA256)
					require.True(t, event.HasDetails)
					require.Equal(t, record.Details, event.Details)
				}
			}
			return nil
		})
		require.NoError(t, err)
		require.Len(t, copies, len(f.records), "every event of this deployment, and none of another's")
		require.Equal(t, 2, copies[f.records[1].Entry.ID], "every copy")
		require.Equal(t, 1, copies[f.records[0].Entry.ID])
	})
}

// jsonNumber is a payload number with exactly this text, as the queue reads it.
func jsonNumber(text string) any { return json.Number(text) }

func TestServerLatestSourceSyncEvents(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))

	org, actor, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := time.Now().UTC().Truncate(time.Microsecond)
	synced := func(source, by string, at time.Time) business.AuditRecord {
		return newRecord(t, org, by, business.EventDatasourceSourceSynced, source, at, business.RetentionContent, map[string]any{"job_id": uuid.NewString()})
	}
	newest := synced("source-1", actor, base.Add(-time.Hour))
	appendBatch(t, store, testDeployment,
		synced("source-1", other, base.Add(-3*time.Hour)), newest,
		synced("source-2", "", base.Add(-2*time.Hour)),
		newRecord(t, uuid.NewString(), other, business.EventDatasourceSourceSynced, "source-3", base, business.RetentionContent, nil),
	)
	appendBatch(t, store, testDeployment, newest)
	appendBatch(t, store, "deployment-other", synced("source-1", other, base))

	got, err := store.LatestSourceSyncEvents(ctx, business.OrganizationAuditScope(org), []string{"source-1", "source-2", "source-3", "source-1"})
	require.NoError(t, err)
	require.Equal(t, map[string]business.AuditSourceSyncEvent{
		"source-1": {RequestedAt: time.UnixMicro(base.Add(-time.Hour).UnixMicro()), ActorID: actor},
		"source-2": {RequestedAt: time.UnixMicro(base.Add(-2 * time.Hour).UnixMicro())},
	}, got, "another organization's and another deployment's requests are outside the read")
}

// An organization id is any spelling of a uuid to Postgres; the store keeps
// the canonical lowercase form, so a read written otherwise still finds it.
func TestServerReadsAnOrganizationOfAnySpelling(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))

	org, other, actor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	synced := newRecord(t, org, actor, business.EventDatasourceSourceSynced, "source-1", base, business.RetentionContent, map[string]any{"n": 1})
	appendBatch(t, store, testDeployment,
		synced,
		newRecord(t, org, actor, business.EventAuthLogin, "", base.Add(-time.Hour), business.RetentionSecurity, map[string]any{"method": "password"}),
		newRecord(t, other, actor, business.EventAuthLogin, "", base.Add(-time.Hour), business.RetentionSecurity, nil),
	)
	for _, spelling := range []string{strings.ToUpper(org), "{" + org + "}", strings.ReplaceAll(org, "-", "")} {
		read := business.AuditRead{Scope: business.OrganizationAuditScope(spelling), Query: business.AuditQuery{OrgID: spelling}}
		listed, _, err := store.ListAuditEvents(ctx, read)
		require.NoError(t, err, spelling)
		require.Len(t, listed, 2, spelling)
		exported, err := store.ExportAuditEvents(ctx, read)
		require.NoError(t, err, spelling)
		require.Len(t, exported, 2, spelling)
		buckets, err := store.AggregateAuditEvents(ctx, read, business.AuditAggregationSpec{})
		require.NoError(t, err, spelling)
		var counted int64
		for _, bucket := range buckets {
			counted += bucket.Count
		}
		require.Equal(t, int64(2), counted, spelling)
		latest, err := store.LatestSourceSyncEvents(ctx, business.OrganizationAuditScope(spelling), []string{"source-1"})
		require.NoError(t, err, spelling)
		require.Equal(t, map[string]business.AuditSourceSyncEvent{
			"source-1": {RequestedAt: time.UnixMicro(base.UnixMicro()), ActorID: actor},
		}, latest, spelling)
	}
}

// A zero or far-future bound — a Go zero time through a protobuf timestamp is
// the year 1 — is no bound at all to Postgres; here it must read the same.
func TestServerReadsATimeBoundOutsideTheColumnRange(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))

	org, actor := uuid.NewString(), uuid.NewString()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	f := &fixture{org: org}
	f.records = []business.AuditRecord{
		newRecord(t, org, actor, business.EventAuthLogin, "", base, business.RetentionSecurity, map[string]any{"method": "password"}),
		newRecord(t, org, actor, business.EventDocumentRead, "doc-1", base.Add(-time.Hour), business.RetentionContent, map[string]any{"boundary": "b-1", "count": 3}),
		newRecord(t, org, actor, business.EventDocumentRead, "doc-2", base.Add(-48*time.Hour), business.RetentionContent, map[string]any{"boundary": "b-1"}),
	}
	appendBatch(t, store, testDeployment, f.records...)

	zero, farFuture := time.Time{}, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	between := base.Add(-2 * time.Hour)
	reads := map[string]struct {
		from, to *time.Time
		want     int
	}{
		"zero from":              {from: &zero, want: 3},
		"far-future to":          {to: &farFuture, want: 3},
		"both":                   {from: &zero, to: &farFuture, want: 3},
		"zero from, real to":     {from: &zero, to: &between, want: 1},
		"real from, far to":      {from: &between, to: &farFuture, want: 2},
		"to before every event":  {to: &zero, want: 0},
		"from after every event": {from: &farFuture, want: 0},
	}
	specs := map[string]business.AuditAggregationSpec{
		"default": {},
		"payload": {GroupBy: []string{"payload:boundary"}},
	}
	for name, tc := range reads {
		q := business.AuditQuery{OrgID: org, From: tc.from, To: tc.to}
		read := business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: q}
		listed, _, err := store.ListAuditEvents(ctx, read)
		require.NoError(t, err, name)
		require.Len(t, listed, tc.want, name)
		want, _ := f.referenceList(t, read)
		require.Equal(t, want, listed, name)
		exported, err := store.ExportAuditEvents(ctx, read)
		require.NoError(t, err, name)
		require.Equal(t, want, exported, name)
		for specName, spec := range specs {
			got, err := store.AggregateAuditEvents(ctx, read, spec)
			require.NoError(t, err, "%s / %s", name, specName)
			require.Equal(t, f.referenceAggregate(t, read, spec), got, "%s / %s", name, specName)
		}
		payload := q
		payload.PayloadContains = map[string]any{"boundary": "b-1"}
		read.Query = payload
		listed, _, err = store.ListAuditEvents(ctx, read)
		require.NoError(t, err, name)
		want, _ = f.referenceList(t, read)
		require.Equal(t, want, listed, name+" with a payload filter")
	}

	// The history read-back takes the same bounds.
	copies := 0
	require.NoError(t, store.ReadStoredAuditEvents(ctx, zero, farFuture, func(business.StoredAuditEvent) error { copies++; return nil }))
	require.Equal(t, 3, copies)
}

// Decimal texts whose nearest double a fast parser misses: ClickHouse reads
// each — as a JSON number and as a JSON string — to the double Postgres's
// float8in reads, so the metrics over them run in ClickHouse and agree.
func TestServerNumericReadingIsExact(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	store := serverStore(t, conn, database)
	require.NoError(t, store.Ensure(ctx))

	f := &fixture{org: uuid.NewString()}
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i, text := range []string{
		"96.55639781", "0.000000000000005720547203731686", "0.000000000000011375683516399455",
		"376.8549944162621642512", "844.8835565338717500304", "801.3627100269752912500", "9007199254740993", "-12345678901234567",
	} {
		at := base.Add(-time.Duration(i) * time.Minute)
		f.records = append(f.records,
			newRecord(t, f.org, "", business.EventDocumentRead, "doc", at, business.RetentionContent, map[string]any{"label": "number " + text, "value": jsonNumber(text)}),
			newRecord(t, f.org, "", business.EventDocumentRead, "doc", at, business.RetentionContent, map[string]any{"label": "string " + text, "value": text}),
		)
	}
	appendBatch(t, store, testDeployment, f.records...)

	inService := 0
	store.inService = func() { inService++ }
	read := business.AuditRead{Scope: business.OrganizationAuditScope(f.org), Query: business.AuditQuery{OrgID: f.org}}
	for _, metric := range []string{"sum", "avg", "min", "max", "percentile"} {
		// One group per value: each metric is that value's reading itself.
		spec := business.AuditAggregationSpec{GroupBy: []string{"payload:label"}, Metrics: []business.AuditMetric{
			{Op: metric, Field: "payload:value", Percentile: 0.37},
		}}
		got, err := store.AggregateAuditEvents(ctx, read, spec)
		require.NoError(t, err)
		require.Equal(t, f.referenceAggregate(t, read, spec), got, metric)
		// And across them all, in one group.
		spec.GroupBy = []string{"event_type"}
		got, err = store.AggregateAuditEvents(ctx, read, spec)
		require.NoError(t, err)
		require.Equal(t, f.referenceAggregate(t, read, spec), got, metric)
	}
	require.Zero(t, inService, "every one ran in ClickHouse")
}
