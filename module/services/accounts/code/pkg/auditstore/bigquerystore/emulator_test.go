package bigquerystore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	bqstorage "cloud.google.com/go/bigquery/storage/apiv1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// The adapter against a BigQuery emulator, when one is reachable:
//
//	docker run --rm -p 9050:9050 ghcr.io/goccy/bigquery-emulator --project=test-project
//	AUDIT_BIGQUERY_EMULATOR_HOST=localhost:9050 go test ./pkg/auditstore/bigquerystore
//
// It is skipped otherwise, and no gate runs it. The emulator does not implement
// BigQuery's streaming insert-id deduplication or partition expiry, so this
// proves the table shapes Ensure creates, its idempotence, its refusals, and
// that a batch's rows land where the retention class sends them — not the
// deduplication window, which is BigQuery's own.
func emulatorClient(t *testing.T) *bigquery.Client {
	t.Helper()
	host := strings.TrimSpace(os.Getenv("AUDIT_BIGQUERY_EMULATOR_HOST"))
	if host == "" {
		t.Skip("AUDIT_BIGQUERY_EMULATOR_HOST is not set; see the comment above for how to run against an emulator")
	}
	client, err := bigquery.NewClient(context.Background(), "test-project",
		option.WithEndpoint("http://"+host), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// emulatorDataset creates a dataset for one test. The adapter itself never
// creates one: the deployment does.
func emulatorDataset(t *testing.T, client *bigquery.Client) string {
	t.Helper()
	name := "audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, client.Dataset(name).Create(context.Background(), &bigquery.DatasetMetadata{}))
	t.Cleanup(func() { _ = client.Dataset(name).DeleteWithContents(context.Background()) })
	return name
}

func TestEmulatorEnsureRefusesAMissingDataset(t *testing.T) {
	client := emulatorClient(t)
	store, err := New(client, Config{Dataset: "audit_absent", ContentDetailRetention: 30 * 24 * time.Hour})
	require.NoError(t, err)
	require.ErrorContains(t, store.Ensure(context.Background()), "does not exist")
}

func TestEmulatorEnsureCreatesBothTablesOnce(t *testing.T) {
	client := emulatorClient(t)
	ctx := context.Background()
	dataset := emulatorDataset(t, client)
	store, err := New(client, Config{Dataset: dataset, ContentDetailRetention: 30 * 24 * time.Hour})
	require.NoError(t, err)

	require.NoError(t, store.Ensure(ctx))
	require.NoError(t, store.Ensure(ctx), "a second start finds both tables and accepts them")

	for table, expiration := range map[string]time.Duration{EventsTable: 0, DetailsTable: 30 * 24 * time.Hour} {
		meta, err := client.Dataset(dataset).Table(table).Metadata(ctx)
		require.NoError(t, err, table)
		require.NotNil(t, meta.TimePartitioning, table)
		require.Equal(t, "occurred_at", meta.TimePartitioning.Field, table)
		require.Equal(t, expiration, meta.TimePartitioning.Expiration, table)
	}

	other, err := New(client, Config{Dataset: dataset, ContentDetailRetention: 400 * 24 * time.Hour})
	require.NoError(t, err)
	require.ErrorContains(t, other.Ensure(ctx), "partitions expire",
		"a details table expiring at another window is refused, never silently kept")
}

func TestEmulatorAppendRoutesRecordsByRetentionClass(t *testing.T) {
	client := emulatorClient(t)
	ctx := context.Background()
	dataset := emulatorDataset(t, client)
	store, err := New(client, Config{Dataset: dataset, ContentDetailRetention: 30 * 24 * time.Hour})
	require.NoError(t, err)
	require.NoError(t, store.Ensure(ctx))

	security := record(t, business.RetentionSecurity, "22222222-2222-2222-2222-222222222222")
	content := record(t, business.RetentionContent, "")
	require.NoError(t, store.AppendAuditBatch(ctx, business.AuditBatch{
		ID: "batch-1", DeploymentID: "deployment-1", ComposedAt: occurredAt,
		Records: []business.AuditRecord{security, content},
	}))

	type eventRow struct {
		EventID        string              `bigquery:"event_id"`
		RetentionClass string              `bigquery:"retention_class"`
		DetailsSHA256  string              `bigquery:"details_sha256"`
		Details        bigquery.NullString `bigquery:"details"`
		OrgID          bigquery.NullString `bigquery:"org_id"`
		OccurredAt     time.Time           `bigquery:"occurred_at"`
	}
	events := map[string]eventRow{}
	it := client.Dataset(dataset).Table(EventsTable).Read(ctx)
	for {
		var row eventRow
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		events[row.EventID] = row
	}
	require.Len(t, events, 2)
	require.Equal(t, security.Details, events[security.Entry.ID].Details.StringVal, "a security event keeps its details in the events table")
	require.False(t, events[content.Entry.ID].Details.Valid, "a content event does not")
	require.False(t, events[content.Entry.ID].OrgID.Valid, "a platform event has no organization")
	require.Equal(t, content.DetailsSHA256, events[content.Entry.ID].DetailsSHA256)
	require.True(t, occurredAt.Equal(events[security.Entry.ID].OccurredAt))

	var details []string
	it = client.Dataset(dataset).Table(DetailsTable).Read(ctx)
	for {
		var row struct {
			EventID string `bigquery:"event_id"`
			Details string `bigquery:"details"`
		}
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		require.Equal(t, content.Details, row.Details)
		details = append(details, row.EventID)
	}
	require.Equal(t, []string{content.Entry.ID}, details, "only the content event has a details row")
}

// emulatorReader is a reader on the emulator's Storage Read API, at
// AUDIT_BIGQUERY_EMULATOR_GRPC_HOST:
//
//	docker run --rm -p 9050:9050 -p 9060:9060 ghcr.io/goccy/bigquery-emulator --project=test-project
//	AUDIT_BIGQUERY_EMULATOR_HOST=localhost:9050 AUDIT_BIGQUERY_EMULATOR_GRPC_HOST=localhost:9060 go test ./pkg/auditstore/bigquerystore
//
// The emulator evaluates a read session's row restriction with a GoogleSQL
// engine, so this is where the restriction text the reader writes is parsed
// by something other than the fake that shares its author.
func emulatorReader(t *testing.T, dataset, deploymentID string) *Reader {
	t.Helper()
	host := strings.TrimSpace(os.Getenv("AUDIT_BIGQUERY_EMULATOR_GRPC_HOST"))
	if host == "" {
		t.Skip("AUDIT_BIGQUERY_EMULATOR_GRPC_HOST is not set; see emulatorReader for how to run against an emulator")
	}
	client, err := bqstorage.NewBigQueryReadClient(context.Background(), option.WithEndpoint(host), option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	// The emulator serves one stream per session.
	reader, err := NewReader(ReadConfig{Client: client, Project: "test-project", Dataset: dataset, DeploymentID: deploymentID, MaxStreams: 1})
	require.NoError(t, err)
	return reader
}

func TestEmulatorReadsThroughTheStorageReadAPI(t *testing.T) {
	client := emulatorClient(t)
	ctx := context.Background()
	dataset := emulatorDataset(t, client)
	reader := emulatorReader(t, dataset, "deployment-1")
	store, err := New(client, Config{Dataset: dataset, ContentDetailRetention: 30 * 24 * time.Hour, Reader: reader})
	require.NoError(t, err)
	require.NoError(t, store.Ensure(ctx))

	org := "22222222-2222-2222-2222-222222222222"
	security := record(t, business.RetentionSecurity, org)
	content := record(t, business.RetentionContent, org)
	content.Entry.EventType = business.EventDatasourceSourceSynced
	content.Entry.CreatedAt = occurredAt.Add(time.Minute)
	other := record(t, business.RetentionSecurity, "33333333-3333-3333-3333-333333333333")
	batch := business.AuditBatch{ID: "batch-1", DeploymentID: "deployment-1", ComposedAt: occurredAt,
		Records: []business.AuditRecord{security, content, other}}
	require.NoError(t, store.AppendAuditBatch(ctx, batch))
	batch.ID = "batch-1-again"
	require.NoError(t, store.AppendAuditBatch(ctx, batch), "a redelivery")
	batch.ID, batch.DeploymentID = "batch-elsewhere", "deployment-2"
	require.NoError(t, store.AppendAuditBatch(ctx, batch), "another deployment")

	from, to := occurredAt.Add(-time.Hour), occurredAt.Add(time.Hour)
	read := business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org, From: &from, To: &to}}
	entries, next, err := store.ListAuditEvents(ctx, read)
	require.NoError(t, err)
	require.Empty(t, next)
	require.Len(t, entries, 2, "each event once, this organization and deployment only")
	require.Equal(t, content.Entry.ID, entries[0].ID, "newest first")
	require.Equal(t, map[string]any{"method": "password"}, entries[0].Payload, "content details are joined")
	require.Equal(t, map[string]any{"method": "password"}, entries[1].Payload)

	read.Query.EventTypes = []string{string(business.EventAuthLogin), string(business.EventDatasourceSourceSynced)}
	read.Query.ResourceID = "session-1"
	buckets, err := store.AggregateAuditEvents(ctx, read, business.AuditAggregationSpec{GroupBy: []string{"event_type"}})
	require.NoError(t, err)
	require.Len(t, buckets, 2)
	for _, bucket := range buckets {
		require.Equal(t, int64(1), bucket.Count, bucket.Key)
	}

	exported, err := store.ExportAuditEvents(ctx, business.AuditRead{Scope: business.PlatformAuditScope(), Query: business.AuditQuery{
		PayloadContains: map[string]any{"method": "pass\"word\né"},
	}})
	require.NoError(t, err, "a literal with a quote, a newline and a non-ASCII rune parses")
	require.Empty(t, exported)
	exported, err = store.ExportAuditEvents(ctx, business.AuditRead{Scope: business.PlatformAuditScope()})
	require.NoError(t, err)
	require.Len(t, exported, 3, "the platform read spans organizations, never deployments")

	copies := 0
	require.NoError(t, store.ReadStoredAuditEvents(ctx, from, to, func(event business.StoredAuditEvent) error {
		copies++
		require.Equal(t, "deployment-1", event.DeploymentID)
		require.True(t, event.HasDetails)
		require.Equal(t, business.AuditDetailsSHA256(event.Details), event.DetailsSHA256)
		return nil
	}))
	require.Equal(t, 6, copies, "the read-back sees every copy")

	// The server, not the service, applies the restriction: what it streams
	// back is already narrowed, at microsecond precision, and a literal stays a
	// literal.
	served := func(build func(*restriction)) int {
		where := &restriction{}
		build(where)
		n := 0
		require.NoError(t, reader.scan(ctx, EventsTable, eventFields, where, func(arrowRow) error { n++; return nil }))
		return n
	}
	require.Equal(t, 9, served(func(*restriction) {}))
	require.Equal(t, 6, served(func(w *restriction) { w.eq("deployment_id", "deployment-1") }))
	require.Equal(t, 9, served(func(w *restriction) { w.timeBound("occurred_at", "<=", content.Entry.CreatedAt) }))
	require.Equal(t, 6, served(func(w *restriction) { w.timeBound("occurred_at", "<", content.Entry.CreatedAt) }))
	require.Equal(t, 3, served(func(w *restriction) { w.in("event_type", []string{string(business.EventDatasourceSourceSynced), "saas.none.none"}) }))
	require.Zero(t, served(func(w *restriction) { w.eq("resource_id", `session-1" OR TRUE OR "`+"\n\u00e9\\") }))
}
