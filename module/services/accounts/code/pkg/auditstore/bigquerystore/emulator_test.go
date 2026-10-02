package bigquerystore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
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
