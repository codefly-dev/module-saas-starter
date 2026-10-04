//go:build !pure

package business_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"cloud.google.com/go/bigquery"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Read parity of the audit store swap (ADR 0009) — the conformance suite every
// store of record passes. One fixture, written once through the emitter as a
// postgres deployment writes it (audit_events) and once as a swap deployment
// does (the queue, drained by the relay into each store under test), must read
// back identically through the service: the activity list page by page, every
// aggregation, both export formats and the readable-source query. Each store
// also holds what Postgres does not: events delivered twice, and another
// deployment's and another organization's events, none of which may change an
// answer. A read without a scope is refused, and a refused resource read never
// reaches the store.

const (
	parityProject    = "parity-project"
	parityDataset    = "audit"
	parityDeployment = "deployment-parity"
)

// parityStore is one store of record under test.
type parityStore struct {
	// writer is what the relay appends to.
	writer business.AuditStoreWriter
	// store is what the service reads.
	store business.AuditStore
	// eventIDs lists every event id the store holds, once per copy.
	eventIDs func(t *testing.T) []string
}

// parityStores builds every store of record the suite runs against. A store
// that needs a server the environment does not provide returns nil, after
// logging how to provide one.
var parityStores = map[string]func(t *testing.T) *parityStore{
	"bigquery": newBigQueryParityStore,
}

// bigQueryAppender appends a batch's rows to the fake, as the BigQuery writer
// streams them.
type bigQueryAppender struct{ fake *bigqueryfake.Server }

func (a bigQueryAppender) AppendAuditBatch(_ context.Context, batch business.AuditBatch) error {
	events, details := bigquerystore.BatchRows(batch)
	for _, row := range events {
		if err := a.fake.Insert(bigqueryfake.TablePath(parityProject, parityDataset, bigquerystore.EventsTable), row.Values); err != nil {
			return err
		}
	}
	for _, row := range details {
		if err := a.fake.Insert(bigqueryfake.TablePath(parityProject, parityDataset, bigquerystore.DetailsTable), row.Values); err != nil {
			return err
		}
	}
	return nil
}

// newBigQueryParityStore is the BigQuery store over the fake Storage Read API,
// which evaluates the reader's row restrictions and serves Arrow batches over
// several streams.
func newBigQueryParityStore(t *testing.T) *parityStore {
	fake := bigqueryfake.New()
	eventsPath := bigqueryfake.TablePath(parityProject, parityDataset, bigquerystore.EventsTable)
	fake.CreateTable(eventsPath, bigquerystore.EventsSchema())
	fake.CreateTable(bigqueryfake.TablePath(parityProject, parityDataset, bigquerystore.DetailsTable), bigquerystore.DetailsSchema())
	client, err := bigquery.NewClient(testCtx, parityProject, option.WithoutAuthentication(), option.WithEndpoint("http://127.0.0.1:1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{
		Client: fake, Project: parityProject, Dataset: parityDataset, DeploymentID: parityDeployment,
	})
	require.NoError(t, err)
	store, err := bigquerystore.New(client, bigquerystore.Config{Dataset: parityDataset, ContentDetailRetention: 30 * 24 * time.Hour, Reader: reader})
	require.NoError(t, err)
	return &parityStore{
		writer: bigQueryAppender{fake: fake},
		store:  store,
		eventIDs: func(*testing.T) []string {
			var ids []string
			for _, row := range fake.Rows(eventsPath) {
				ids = append(ids, row["event_id"].(string))
			}
			return ids
		},
	}
}

// fanOut appends every batch to every store under test, and remembers it.
type fanOut struct {
	writers []business.AuditStoreWriter
	batches []business.AuditBatch
}

func (f *fanOut) AppendAuditBatch(ctx context.Context, batch business.AuditBatch) error {
	for _, writer := range f.writers {
		if err := writer.AppendAuditBatch(ctx, batch); err != nil {
			return err
		}
	}
	f.batches = append(f.batches, batch)
	return nil
}

// countingStore counts the reads that reach a store.
type countingStore struct {
	business.AuditStore
	reads int
}

func (c *countingStore) ListAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, string, error) {
	c.reads++
	return c.AuditStore.ListAuditEvents(ctx, read)
}

func (c *countingStore) AggregateAuditEvents(ctx context.Context, read business.AuditRead, spec business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	c.reads++
	return c.AuditStore.AggregateAuditEvents(ctx, read, spec)
}

func (c *countingStore) ExportAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, error) {
	c.reads++
	return c.AuditStore.ExportAuditEvents(ctx, read)
}

func (c *countingStore) LatestSourceSyncEvents(ctx context.Context, scope business.AuditReadScope, sources []string) (map[string]business.AuditSourceSyncEvent, error) {
	c.reads++
	return c.AuditStore.LatestSourceSyncEvents(ctx, scope, sources)
}

type discardArchive struct{}

func (discardArchive) WriteAuditBatch(context.Context, business.AuditBatch) error { return nil }

type parityFixture struct {
	stores         map[string]*parityStore
	postgres       *business.Service
	org, otherOrg  string
	actor, other   string
	sourceOrg      string
	sourceReader   string
	sources        []string
	members        map[string]string
	unseenUser     string
	newestSyncedBy map[string]string
}

func parityEntry(org, actor string, eventType business.EventType, resourceID string, at time.Time, payload map[string]any) business.AuditEntry {
	return business.AuditEntry{
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
	}
}

func newParityFixture(t *testing.T) *parityFixture {
	t.Helper()
	clearData(t)
	ctx := testCtx
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.EnsureAuditPartitions(ctx, 1)
	}))

	f := &parityFixture{
		org: uuid.NewString(), otherOrg: uuid.NewString(),
		actor: uuid.NewString(), other: uuid.NewString(),
		members: map[string]string{}, newestSyncedBy: map[string]string{},
	}

	// The readable-source half needs real principals: a member of the source's
	// organization (labelled by display name), a registered user who is not a
	// member (invisible under row-level security, so labelled by id), and an id
	// with no principal at all.
	member, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "parity-member@test.com",
		Identity:     &gen.UserIdentity{Provider: "email", ProviderId: "parity-member", ProviderEmail: "parity-member@test.com", EmailVerified: true},
	})
	require.NoError(t, err)
	outsider, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "parity-outsider@test.com",
		Identity:     &gen.UserIdentity{Provider: "email", ProviderId: "parity-outsider", ProviderEmail: "parity-outsider@test.com", EmailVerified: true},
	})
	require.NoError(t, err)
	created, err := testService.CreateOrganization(ctx, member.User.Uuid, &gen.CreateOrganizationRequest{Name: "Parity Organization", Slug: "parity-organization"})
	require.NoError(t, err)
	f.sourceOrg, f.sourceReader, f.unseenUser = created.Organization.Id, member.User.Uuid, outsider.User.Uuid
	f.sources = []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}

	// Every instant falls in a partition EnsureAuditPartitions provisions: this
	// month or the one before it.
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	day := 24 * time.Hour
	shared := base.Add(-5 * time.Hour) // two events share this instant; the id breaks the tie
	readPayload := func(boundary string, duration any, count any, outcome string) map[string]any {
		payload := map[string]any{"boundary": boundary, "duration_ms": duration, "outcome": outcome}
		if count != nil {
			payload["result_count"] = count
		}
		return payload
	}
	entries := []business.AuditEntry{
		parityEntry(f.org, f.actor, business.EventAuthLogin, "", base, map[string]any{"method": "password", "client_id": "cli"}),
		parityEntry(f.org, f.actor, business.EventDocumentRead, "doc-1", base.Add(-time.Hour), readPayload("b-1", 12.5, 3, "ok")),
		parityEntry(f.org, f.other, business.EventDocumentRead, "doc-1", base.Add(-2*time.Hour), readPayload("b-1", "7.25", 1, "ok")),
		parityEntry(f.org, f.other, business.EventDocumentRead, "doc-2", shared, readPayload("b-2", "fast", nil, "denied")),
		parityEntry(f.org, f.actor, business.EventDocumentRead, "doc-2", shared, readPayload("b-2", 0.5, 4, "ok")),
		parityEntry(f.org, "", business.EventOrgUpdated, "", base.Add(-26*time.Hour), map[string]any{"name": "Renamed", "slug": "renamed"}),
		parityEntry(f.org, f.actor, business.EventDocumentRead, "doc-3", base.Add(-3*day), map[string]any{
			"boundary": "b-1", "duration_ms": 100, "outcome": "ok", "result_count": 2,
			"tags": []any{"x", map[string]any{"k": 1}}, "nested": map[string]any{"zz": 1, "a": "q"}, "label": "<b>&é",
		}),
		parityEntry(f.org, f.other, business.EventAuthLogin, "", base.Add(-9*day), map[string]any{"method": "passkey"}),
		parityEntry(f.org, f.actor, business.EventDocumentRead, "doc-1", base.Add(-12*day), readPayload("b-3", 2.25, 8, "ok")),
		parityEntry(f.org, f.actor, business.EventOrgUpdated, "", base.Add(-15*day), map[string]any{"name": "First"}),
		parityEntry(f.org, f.other, business.EventDocumentRead, "doc-4", base.Add(-20*day), map[string]any{}),
		// Another organization, and the platform, which only the platform read sees.
		parityEntry(f.otherOrg, f.actor, business.EventDocumentRead, "doc-1", base.Add(-time.Hour), readPayload("b-1", 1, 1, "ok")),
		parityEntry(f.otherOrg, f.actor, business.EventAuthLogin, "", base.Add(-4*day), nil),
		parityEntry("", f.actor, business.EventAuthLogin, "", base.Add(-30*time.Minute), map[string]any{"method": "password"}),
	}
	impersonated := parityEntry(f.org, f.actor, business.EventOrgUpdated, "", base.Add(-6*day), map[string]any{"name": "Acting"})
	impersonated.IsImpersonated, impersonated.ImpersonatedBy, impersonated.ClientID, impersonated.IPAddress = true, f.other, "client-1", "203.0.113.7"
	entries = append(entries, impersonated)

	// The readable-source fixture: per source, several requests by different
	// actors; the newest one names who the projection reports.
	synced := func(source, actor string, at time.Time) business.AuditEntry {
		entry := parityEntry(f.sourceOrg, actor, business.EventDatasourceSourceSynced, source, at, map[string]any{"job_id": uuid.NewString(), "repo": "acme/handbook"})
		if actor == "" {
			entry.ActorType = business.ActorTypeSystem
		}
		return entry
	}
	entries = append(entries,
		synced(f.sources[0], f.unseenUser, base.Add(-3*day)),
		synced(f.sources[0], f.sourceReader, base.Add(-2*time.Hour)),
		synced(f.sources[1], f.sourceReader, base.Add(-25*day)),
		synced(f.sources[1], f.other, base.Add(-10*day)),
		synced(f.sources[2], f.sourceReader, base.Add(-time.Hour)),
		synced(f.sources[2], "", base.Add(-time.Minute)),
		synced(f.sources[3], f.unseenUser, base.Add(-90*time.Minute)),
	)
	f.newestSyncedBy = map[string]string{f.sources[0]: f.sourceReader, f.sources[1]: f.other, f.sources[2]: "", f.sources[3]: f.unseenUser}

	// Written as a postgres deployment writes them, and as a bigquery one does.
	postgresEmitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	queuedEmitter, err := business.NewDurableAuditEmitter(testStore, testStore, business.WithQueuedRecords())
	require.NoError(t, err)
	for _, entry := range entries {
		for _, emitter := range []*business.DurableAuditEmitter{postgresEmitter, queuedEmitter} {
			write := func(ctx context.Context) error { return emitter.EmitTx(ctx, entry) }
			if entry.OrgID == "" {
				require.NoError(t, testStore.WithControlPlane(ctx, write))
			} else {
				require.NoError(t, testStore.WithOrgTx(ctx, entry.OrgID, write))
			}
		}
	}

	f.stores = map[string]*parityStore{}
	delivery := &fanOut{}
	for name, open := range parityStores {
		if store := open(t); store != nil {
			f.stores[name] = store
			delivery.writers = append(delivery.writers, store.writer)
		}
	}
	require.NotEmpty(t, f.stores)
	pool, err := infra.NewAuditRelayPool(ctx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	queue, err := infra.NewPostgresAuditQueue(pool)
	require.NoError(t, err)
	relay, err := business.NewAuditRelay(business.AuditRelayConfig{
		Queue: queue, Store: delivery, Archive: discardArchive{}, Types: testStore,
		DeploymentID: parityDeployment, BatchSize: 4, MaxWait: time.Nanosecond,
	})
	require.NoError(t, err)
	delivered, err := relay.DrainOnce(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, delivered, len(entries))

	// A relay that restarted between its writes and its queue delete delivers a
	// batch again; the store then holds those events twice.
	batches := delivery.batches
	require.GreaterOrEqual(t, len(batches), 3)
	for _, again := range []business.AuditBatch{batches[0], batches[len(batches)/2]} {
		again.ID = uuid.NewString()
		require.NoError(t, delivery.AppendAuditBatch(ctx, again))
	}
	// Another deployment writing the same organization's events into the same
	// tables is outside every read of this one.
	foreign := batches[1]
	foreign.ID, foreign.DeploymentID = uuid.NewString(), "deployment-other"
	require.NoError(t, delivery.AppendAuditBatch(ctx, foreign))

	f.postgres, err = business.NewService(testStore)
	require.NoError(t, err)
	return f
}

func TestAuditStoreParity(t *testing.T) {
	f := newParityFixture(t)
	for name, store := range f.stores {
		t.Run(name, func(t *testing.T) { runAuditStoreParity(t, f, store) })
	}
}

func runAuditStoreParity(t *testing.T, f *parityFixture, under *parityStore) {
	ctx := testCtx
	counted := &countingStore{AuditStore: under.store}
	swapped, err := business.NewService(testStore)
	require.NoError(t, err)
	swapped.SetAuditStore(counted)

	copies := map[string]int{}
	for _, id := range under.eventIDs(t) {
		copies[id]++
	}
	duplicated := 0
	for _, n := range copies {
		if n > 1 {
			duplicated++
		}
	}
	require.Positive(t, duplicated, "the store holds events more than once")

	week := time.Now().Add(-7 * 24 * time.Hour)
	recent := time.Now().Add(-3 * time.Hour)
	queries := map[string]business.AuditQuery{
		"organization":       {OrgID: f.org},
		"actor":              {OrgID: f.org, ActorID: f.actor},
		"event type":         {OrgID: f.org, EventType: string(business.EventDocumentRead)},
		"event type set":     {OrgID: f.org, EventTypes: []string{string(business.EventAuthLogin), string(business.EventOrgUpdated)}},
		"type and set":       {OrgID: f.org, EventType: string(business.EventAuthLogin), EventTypes: []string{string(business.EventOrgUpdated)}},
		"category":           {OrgID: f.org, Category: "organization"},
		"namespace":          {OrgID: f.org, Namespace: "saas"},
		"resource":           {OrgID: f.org, Resource: "datasource", ResourceID: "doc-1"},
		"client":             {OrgID: f.org, ClientID: "client-1"},
		"payload":            {OrgID: f.org, PayloadContains: map[string]any{"boundary": "b-1"}},
		"payload number":     {OrgID: f.org, PayloadContains: map[string]any{"result_count": 3.0}},
		"payload nested":     {OrgID: f.org, PayloadContains: map[string]any{"nested": map[string]any{"a": "q"}, "tags": []any{"x"}}},
		"window":             {OrgID: f.org, From: &week, To: &recent},
		"platform for actor": {ActorID: f.actor},
		"other organization": {OrgID: f.otherOrg},
	}

	t.Run("list", func(t *testing.T) {
		// The fixture is what the comparisons compare: not two empty answers.
		all, _, _, err := swapped.QueryAuditLog(ctx, business.AuditQuery{OrgID: f.org, PageSize: 100})
		require.NoError(t, err)
		require.Len(t, all, 12)
		for name, q := range queries {
			for _, size := range []int32{0, 1, 3} {
				q := q
				q.PageSize = size
				var pages int
				for {
					want, wantNext, wantTotal, err := f.postgres.QueryAuditLog(ctx, q)
					require.NoError(t, err, name)
					got, gotNext, gotTotal, err := swapped.QueryAuditLog(ctx, q)
					require.NoError(t, err, name)
					require.Equal(t, want, got, "%s, page size %d, page %d", name, size, pages)
					require.Equal(t, wantNext, gotNext, name)
					require.Equal(t, wantTotal, gotTotal, name)
					pages++
					if wantNext == "" {
						break
					}
					q.PageToken = wantNext
				}
			}
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		specs := map[string]business.AuditAggregationSpec{
			"default":  {},
			"category": {GroupBy: []string{"category"}},
			"actor":    {GroupBy: []string{"actor"}, Metrics: []business.AuditMetric{{Op: "count"}, {Op: "count_distinct", Field: "resource_id"}}},
			"day":      {GroupBy: []string{"time"}, Bucket: "day"},
			"week":     {GroupBy: []string{"time"}, Bucket: "week", Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "actor_id"}}},
			"month":    {GroupBy: []string{"time"}, Bucket: "month"},
			"payload dimensions": {GroupBy: []string{"event_type", "payload:outcome"}, Metrics: []business.AuditMetric{
				{Op: "count_distinct", Field: "payload:boundary"},
				{Op: "count_distinct", Field: "category"},
				{Op: "count_distinct", Field: "event_type"},
				{Op: "count_distinct", Field: "resource"},
			}},
			"nested payload dimension": {GroupBy: []string{"payload:tags", "payload:nested", "payload:label"}},
			"metrics": {GroupBy: []string{"payload:boundary"}, Metrics: []business.AuditMetric{
				{Op: "count", Alias: "reads"},
				{Op: "sum", Field: "payload:duration_ms"},
				{Op: "avg", Field: "payload:duration_ms"},
				{Op: "min", Field: "payload:duration_ms"},
				{Op: "max", Field: "payload:duration_ms"},
				{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.5, Alias: "p50"},
				{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.9, Alias: "p90"},
				{Op: "sum", Field: "payload:result_count", Alias: "results"},
				{Op: "max", Field: "payload:missing", Alias: "absent"},
			}, Derived: []business.AuditDerivedMetric{
				{Alias: "results_per_read", Numerator: "results", Denominator: "reads"},
				{Alias: "absent_ratio", Numerator: "absent", Denominator: "reads"},
			}},
		}
		for queryName, q := range queries {
			for specName, spec := range specs {
				want, err := f.postgres.AggregateAuditLog(ctx, q, spec)
				require.NoError(t, err, "%s / %s", queryName, specName)
				if queryName == "organization" {
					require.NotEmpty(t, want, specName)
				}
				got, err := swapped.AggregateAuditLog(ctx, q, spec)
				require.NoError(t, err, "%s / %s", queryName, specName)
				require.Equal(t, want, got, "%s / %s", queryName, specName)
			}
		}
	})

	t.Run("export", func(t *testing.T) {
		for _, format := range []string{"json", "csv"} {
			for _, export := range []struct {
				org, actor, eventType string
				eventTypes            []string
			}{
				{org: f.org},
				{org: f.org, eventType: string(business.EventDocumentRead)},
				{org: f.org, eventTypes: []string{string(business.EventAuthLogin)}},
				{actor: f.actor},
			} {
				want, wantType, _, err := f.postgres.ExportAuditLog(ctx, export.org, format, export.actor, export.eventType, export.eventTypes)
				require.NoError(t, err)
				got, gotType, _, err := swapped.ExportAuditLog(ctx, export.org, format, export.actor, export.eventType, export.eventTypes)
				require.NoError(t, err)
				require.Equal(t, wantType, gotType)
				require.Greater(t, len(want), 200, "%s export of %+v holds events", format, export)
				require.Equal(t, string(want), string(got), "%s export of %+v", format, export)
			}
		}
	})

	t.Run("readable sources", func(t *testing.T) {
		verified := auth.WithVerifiedDatabaseIdentity(ctx, f.sourceReader, f.sourceOrg)
		asked := append([]string{uuid.NewString()}, f.sources...)
		require.NoError(t, testStore.WithSourceReadSnapshot(verified, f.sourceOrg, func(snapshot context.Context) error {
			want, err := f.postgres.LatestSourceSyncRequests(snapshot, f.sourceOrg, asked)
			require.NoError(t, err)
			got, err := swapped.LatestSourceSyncRequests(snapshot, f.sourceOrg, asked)
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Len(t, got, len(f.sources), "a source never synced has no request")
			require.Equal(t, "parity-member@test.com", got[f.sources[0]].RequestedBy, "a visible principal is named")
			require.Equal(t, f.other, got[f.sources[1]].RequestedBy, "an id with no principal names itself")
			require.Equal(t, "", got[f.sources[2]].RequestedBy, "a request with no actor names no one")
			require.Equal(t, f.unseenUser, got[f.sources[3]].RequestedBy, "a principal outside the organization is not disclosed")
			return nil
		}))
	})

	t.Run("scope", func(t *testing.T) {
		store := under.store
		_, _, err := store.ListAuditEvents(ctx, business.AuditRead{Query: business.AuditQuery{OrgID: f.org}})
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, err = store.AggregateAuditEvents(ctx, business.AuditRead{}, business.AuditAggregationSpec{})
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, err = store.ExportAuditEvents(ctx, business.AuditRead{})
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, err = store.LatestSourceSyncEvents(ctx, business.AuditReadScope{}, f.sources)
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, _, err = store.ListAuditEvents(ctx, business.AuditRead{
			Scope: business.OrganizationAuditScope(f.org), Query: business.AuditQuery{OrgID: f.otherOrg},
		})
		require.Error(t, err, "a query naming another organization than its scope is refused")

		// A resource read is authorized in Postgres before the store is read; a
		// reader without the grant reads nothing from it.
		before := counted.reads
		_, err = swapped.AggregateAuditLogForReader(ctx, f.actor, business.AuditQuery{
			OrgID: f.org, Resource: "datasource", ResourceID: uuid.NewString(), EventType: string(business.EventDatasourceSourceSynced),
		}, business.AuditAggregationSpec{})
		require.Equal(t, codes.PermissionDenied, status.Code(err), fmt.Sprint(err))
		require.Equal(t, before, counted.reads, "a refused read never reaches the store")
	})
}
