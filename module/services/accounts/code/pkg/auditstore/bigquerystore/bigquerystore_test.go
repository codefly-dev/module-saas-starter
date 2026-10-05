package bigquerystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/require"
)

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

func TestEventRowCarriesTheEnvelopeAndOnlySecurityDetails(t *testing.T) {
	security := record(t, business.RetentionSecurity, "22222222-2222-2222-2222-222222222222")
	row := EventRow("deployment-1", security)
	require.Equal(t, security.Entry.ID, row.InsertID, "the event id is the streaming insert id")
	require.Equal(t, map[string]bigquery.Value{
		"event_id":        security.Entry.ID,
		"deployment_id":   "deployment-1",
		"org_id":          "22222222-2222-2222-2222-222222222222",
		"actor_id":        "11111111-1111-1111-1111-111111111111",
		"actor_type":      "user",
		"event_type":      "saas.auth.login",
		"schema_version":  1,
		"resource":        "session",
		"resource_id":     "session-1",
		"occurred_at":     "2026-10-02T14:30:05.123456Z",
		"ip_address":      nil,
		"impersonated_by": nil,
		"is_impersonated": false,
		"client_id":       nil,
		"retention_class": "security",
		"details_sha256":  security.DetailsSHA256,
		"details":         `{"method":"password"}`,
	}, row.Values)

	content := record(t, business.RetentionContent, "")
	row = EventRow("deployment-1", content)
	require.Nil(t, row.Values["details"], "a content-class event's details are not in the events table")
	require.Nil(t, row.Values["org_id"], "a platform event has no organization")
	require.Equal(t, content.DetailsSHA256, row.Values["details_sha256"], "its hash is")

	values, insertID, err := row.Save()
	require.NoError(t, err)
	require.Equal(t, content.Entry.ID, insertID)
	require.Equal(t, row.Values, values)
}

func TestDetailRow(t *testing.T) {
	content := record(t, business.RetentionContent, "22222222-2222-2222-2222-222222222222")
	row := DetailRow("deployment-1", content)
	require.Equal(t, content.Entry.ID, row.InsertID)
	require.Equal(t, map[string]bigquery.Value{
		"event_id":       content.Entry.ID,
		"deployment_id":  "deployment-1",
		"org_id":         "22222222-2222-2222-2222-222222222222",
		"event_type":     "saas.auth.login",
		"occurred_at":    "2026-10-02T14:30:05.123456Z",
		"details_sha256": content.DetailsSHA256,
		"details":        `{"method":"password"}`,
	}, row.Values)
}

// fakeInserter records each streaming request.
type fakeInserter struct {
	requests [][]Row
	fail     error
}

func (f *fakeInserter) Put(_ context.Context, src any) error {
	if f.fail != nil {
		return f.fail
	}
	savers := src.([]bigquery.ValueSaver)
	request := make([]Row, len(savers))
	for i, saver := range savers {
		request[i] = saver.(Row)
	}
	f.requests = append(f.requests, request)
	return nil
}

func (f *fakeInserter) ids() []string {
	var ids []string
	for _, request := range f.requests {
		for _, row := range request {
			ids = append(ids, row.InsertID)
		}
	}
	return ids
}

func TestAppendAuditBatchRoutesDetailsByRetentionClass(t *testing.T) {
	events, details := &fakeInserter{}, &fakeInserter{}
	store := &Store{events: events, details: details}
	security := record(t, business.RetentionSecurity, "")
	content := record(t, business.RetentionContent, "")

	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1",
		Records:      []business.AuditRecord{security, content},
	}))
	require.Equal(t, []string{security.Entry.ID, content.Entry.ID}, events.ids(), "every event has an events row, in batch order")
	require.Equal(t, []string{content.Entry.ID}, details.ids(), "only a content-class event has a details row")
}

func TestAppendAuditBatchSendsAtMostFiveHundredRowsPerRequest(t *testing.T) {
	events, details := &fakeInserter{}, &fakeInserter{}
	store := &Store{events: events, details: details}
	batch := business.AuditBatch{DeploymentID: "deployment-1"}
	for range 1200 {
		batch.Records = append(batch.Records, record(t, business.RetentionContent, ""))
	}
	require.NoError(t, store.AppendAuditBatch(context.Background(), batch))
	for name, inserter := range map[string]*fakeInserter{"events": events, "details": details} {
		var sizes []int
		for _, request := range inserter.requests {
			sizes = append(sizes, len(request))
		}
		require.Equal(t, []int{500, 500, 200}, sizes, name)
	}
}

// largeRecord is a content-class record whose details are about size bytes.
func largeRecord(t *testing.T, size int) business.AuditRecord {
	t.Helper()
	r, err := business.NewAuditRecord(business.AuditEntry{
		ID: business.NewIDString(), ActorType: business.ActorTypeUser, EventType: business.EventAuthLogin, SchemaVersion: 1,
		Resource: "session", CreatedAt: occurredAt,
		Payload: map[string]any{"blob": strings.Repeat("x", size)},
	}, business.RetentionContent)
	require.NoError(t, err)
	return r
}

// requestBytes is what a request carries: its rows as the streaming insert
// encodes them.
func requestBytes(t *testing.T, request []Row) int {
	t.Helper()
	total := 0
	for _, row := range request {
		encoded, err := json.Marshal(row.Values)
		require.NoError(t, err)
		total += len(encoded) + len(row.InsertID)
	}
	return total
}

func TestAppendAuditBatchSplitsRequestsBySizeAsWellAsByRowCount(t *testing.T) {
	events, details := &fakeInserter{}, &fakeInserter{}
	store := &Store{events: events, details: details}
	batch := business.AuditBatch{DeploymentID: "deployment-1"}
	// Thirty rows of about 600 KB: 18 MB, over one request however few the rows.
	for range 30 {
		batch.Records = append(batch.Records, largeRecord(t, 600<<10))
	}
	require.NoError(t, store.AppendAuditBatch(context.Background(), batch))

	require.Greater(t, len(details.requests), 1, "the batch is too large for one request")
	var sent []string
	for _, request := range details.requests {
		require.LessOrEqual(t, requestBytes(t, request), maxRequestBytes)
		for _, row := range request {
			sent = append(sent, row.InsertID)
		}
	}
	var want []string
	for _, record := range batch.Records {
		want = append(want, record.Entry.ID)
	}
	require.Equal(t, want, sent, "every row once, in batch order")
	require.Equal(t, want, events.ids(), "the events table carries no content details and still holds every row")
	require.Len(t, events.requests, 1)
}

func TestAppendAuditBatchRefusesARowThatCannotFitARequestAndSendsNothing(t *testing.T) {
	events, details := &fakeInserter{}, &fakeInserter{}
	store := &Store{events: events, details: details}
	small, huge := largeRecord(t, 1<<10), largeRecord(t, maxRequestBytes)

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1", Records: []business.AuditRecord{small, huge, small},
	})
	require.ErrorIs(t, err, ErrRowTooLarge)
	require.ErrorContains(t, err, huge.Entry.ID, "the error names the event whose row cannot be sent")
	var tooLarge *RowTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	require.Equal(t, huge.Entry.ID, tooLarge.EventID)
	require.Equal(t, DetailsTable, tooLarge.Table)
	require.Greater(t, tooLarge.Size, maxRequestBytes)
	require.Empty(t, events.requests, "nothing is sent of a batch with a row that cannot be, so a retry of the rest never half-writes it")
	require.Empty(t, details.requests)

	// Alone, the rest of the batch goes through.
	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1", Records: []business.AuditRecord{small},
	}))
}

func TestAppendAuditBatchFailsWholeAndNamesTheRejectedRow(t *testing.T) {
	rejected := bigquery.PutMultiError{{InsertID: "event-7", RowIndex: 7, Errors: bigquery.MultiError{errors.New("no such field: extra")}}}
	events, details := &fakeInserter{fail: rejected}, &fakeInserter{}
	store := &Store{events: events, details: details}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1",
		Records:      []business.AuditRecord{record(t, business.RetentionContent, "")},
	})
	require.ErrorContains(t, err, "row 7, insert id event-7")
	require.ErrorContains(t, err, "no such field: extra")
	require.Empty(t, details.requests, "a failed events append stops the batch; the relay retries it whole")
}

func TestTablesArePartitionedByDayOnOccurredAtAndClusteredByOrganization(t *testing.T) {
	store := &Store{retention: 90 * 24 * time.Hour}
	specs := store.tableSpecs()
	require.Len(t, specs, 2)
	for _, spec := range specs {
		meta := metadataFor(spec)
		require.Equal(t, bigquery.DayPartitioningType, meta.TimePartitioning.Type, spec.name)
		require.Equal(t, "occurred_at", meta.TimePartitioning.Field, spec.name)
		require.Equal(t, []string{"org_id"}, meta.Clustering.Fields, spec.name)
	}
	require.Equal(t, EventsTable, specs[0].name)
	require.Zero(t, metadataFor(specs[0]).TimePartitioning.Expiration, "the events table is kept for the compliance window, which no code shortens")
	require.Equal(t, DetailsTable, specs[1].name)
	require.Equal(t, 90*24*time.Hour, metadataFor(specs[1]).TimePartitioning.Expiration, "details expire at the content window")
}

func TestEnsureRefusesATableItCannotWriteAsConfigured(t *testing.T) {
	store := &Store{retention: 30 * 24 * time.Hour}
	details := store.tableSpecs()[1]
	matching := metadataFor(details)
	require.NoError(t, conforms(details, matching))

	for name, mutate := range map[string]func(*bigquery.TableMetadata){
		"different window": func(m *bigquery.TableMetadata) { m.TimePartitioning.Expiration = 400 * 24 * time.Hour },
		"not partitioned":  func(m *bigquery.TableMetadata) { m.TimePartitioning = nil },
		"other partition":  func(m *bigquery.TableMetadata) { m.TimePartitioning.Field = "ingested_at" },
		"missing column":   func(m *bigquery.TableMetadata) { m.Schema = m.Schema[1:] },
		"retyped column": func(m *bigquery.TableMetadata) {
			m.Schema = append(bigquery.Schema{}, m.Schema...)
			retyped := *m.Schema[4]
			retyped.Type = bigquery.DateFieldType
			m.Schema[4] = &retyped
		},
	} {
		existing := metadataFor(details)
		mutate(existing)
		require.Error(t, conforms(details, existing), name)
	}
}

func TestNewValidatesItsConfiguration(t *testing.T) {
	_, err := New(nil, Config{Dataset: "audit", ContentDetailRetention: 24 * time.Hour})
	require.Error(t, err)
	client := &bigquery.Client{}
	for name, cfg := range map[string]Config{
		"no dataset":           {ContentDetailRetention: 24 * time.Hour},
		"sub-day content days": {Dataset: "audit", ContentDetailRetention: time.Hour},
	} {
		_, err := New(client, cfg)
		require.Error(t, err, name)
	}
}

// A time bound beyond the range of a TIMESTAMP is written as the edge of the
// range, never as a literal BigQuery cannot cast.
func TestATimeBoundBeyondTheTimestampRangeIsClamped(t *testing.T) {
	for name, tc := range map[string]struct {
		at   time.Time
		want string
	}{
		"in range":   {occurredAt, `occurred_at >= CAST("2026-10-02 14:30:05.123456+00:00" AS TIMESTAMP)`},
		"year 0":     {time.Date(0, 6, 1, 0, 0, 0, 0, time.UTC), `occurred_at >= CAST("0001-01-01 00:00:00.000000+00:00" AS TIMESTAMP)`},
		"year -300":  {time.Date(-300, 6, 1, 0, 0, 0, 0, time.UTC), `occurred_at >= CAST("0001-01-01 00:00:00.000000+00:00" AS TIMESTAMP)`},
		"year 10000": {time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), `occurred_at >= CAST("9999-12-31 23:59:59.999999+00:00" AS TIMESTAMP)`},
		"zero time":  {time.Time{}, `occurred_at >= CAST("0001-01-01 00:00:00.000000+00:00" AS TIMESTAMP)`},
	} {
		where := &restriction{}
		where.timeBound("occurred_at", ">=", tc.at)
		require.Equal(t, tc.want, where.String(), name)
	}
}

func ExampleEventRow() {
	r, _ := business.NewAuditRecord(business.AuditEntry{
		ID: "00000000-0000-0000-0000-000000000001", ActorType: "system", EventType: business.EventRoleCreated,
		Resource: "role", CreatedAt: occurredAt, Payload: map[string]any{"name": "auditor"},
	}, business.RetentionSecurity)
	row := EventRow("deployment-1", r)
	fmt.Println(row.InsertID, row.Values["occurred_at"], row.Values["details"])
	// Output: 00000000-0000-0000-0000-000000000001 2026-10-02T14:30:05.123456Z {"name":"auditor"}
}
