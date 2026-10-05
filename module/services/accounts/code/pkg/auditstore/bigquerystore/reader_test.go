package bigquerystore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The reader against the fake Storage Read API: what each read pushes down,
// and that the answer does not depend on it.

const (
	project    = "test-project"
	dataset    = "audit"
	deployment = "deployment-1"
	orgA       = "aaaaaaaa-0000-4000-8000-000000000001"
	orgB       = "bbbbbbbb-0000-4000-8000-000000000002"
	actor      = "cccccccc-0000-4000-8000-000000000003"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

type warehouse struct {
	fake   *bigqueryfake.Server
	reader *bigquerystore.Reader
}

func newWarehouse(t *testing.T) *warehouse {
	t.Helper()
	fake := bigqueryfake.New()
	fake.CreateTable(bigqueryfake.TablePath(project, dataset, bigquerystore.EventsTable), bigquerystore.EventsSchema())
	fake.CreateTable(bigqueryfake.TablePath(project, dataset, bigquerystore.DetailsTable), bigquerystore.DetailsSchema())
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{
		Client: fake, Project: project, Dataset: dataset, DeploymentID: deployment,
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	return &warehouse{fake: fake, reader: reader}
}

// append writes records as the relay's batch would, under deploymentID.
func (w *warehouse) append(t *testing.T, deploymentID string, records ...business.AuditRecord) {
	t.Helper()
	events, details := bigquerystore.BatchRows(business.AuditBatch{ID: "batch", DeploymentID: deploymentID, Records: records})
	for _, row := range events {
		require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(project, dataset, bigquerystore.EventsTable), row.Values))
	}
	for _, row := range details {
		require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(project, dataset, bigquerystore.DetailsTable), row.Values))
	}
}

func event(t *testing.T, id int, org string, eventType business.EventType, class business.AuditRetentionClass, at time.Time, payload map[string]any) business.AuditRecord {
	t.Helper()
	record, err := business.NewAuditRecord(business.AuditEntry{
		ID:            fmt.Sprintf("00000000-0000-4000-8000-%012d", id),
		OrgID:         org,
		ActorID:       actor,
		ActorType:     business.ActorTypeUser,
		EventType:     eventType,
		SchemaVersion: 1,
		Resource:      "datasource",
		ResourceID:    fmt.Sprintf("source-%d", id%3),
		Payload:       payload,
		CreatedAt:     at,
	}, class)
	require.NoError(t, err)
	return record
}

func ids(entries []business.AuditEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.ID[len(entry.ID)-3:]
	}
	return out
}

func orgRead(q business.AuditQuery) business.AuditRead {
	q.OrgID = orgA
	return business.AuditRead{Scope: business.OrganizationAuditScope(orgA), Query: q}
}

func TestListReadsNewestWindowsFirstAndStopsWhenThePageIsFull(t *testing.T) {
	w := newWarehouse(t)
	// Two events today, one 3 days ago, one 100 days ago, one 2 years ago.
	ages := []time.Duration{time.Hour, 2 * time.Hour, 72 * time.Hour, 100 * 24 * time.Hour, 730 * 24 * time.Hour}
	for i, age := range ages {
		w.append(t, deployment, event(t, i+1, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-age), map[string]any{"n": i}))
	}

	entries, next, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{PageSize: 1}))
	require.NoError(t, err)
	require.Equal(t, []string{"001"}, ids(entries))
	require.NotEmpty(t, next)
	require.Len(t, w.fake.Sessions(), 1, "the newest day held a page and the proof of another")

	entries, next, err = w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{PageSize: 3, PageToken: next}))
	require.NoError(t, err)
	require.Equal(t, []string{"002", "003", "004"}, ids(entries))
	require.NotEmpty(t, next)

	entries, next, err = w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{PageSize: 3, PageToken: next}))
	require.NoError(t, err)
	require.Equal(t, []string{"005"}, ids(entries), "the last read takes everything older than the bounded windows")
	require.Empty(t, next)

	for _, session := range w.fake.Sessions() {
		restriction := session.GetReadSession().GetReadOptions().GetRowRestriction()
		require.Contains(t, restriction, `deployment_id = "deployment-1"`)
		require.Contains(t, restriction, `org_id = "`+orgA+`"`)
	}
}

func TestEveryEventsReadIsBoundedInTimeWhenTheQueryIs(t *testing.T) {
	w := newWarehouse(t)
	from, to := now.Add(-48*time.Hour), now.Add(-time.Hour)
	fromBound := `occurred_at >= CAST("2026-10-01 12:00:00.000000+00:00" AS TIMESTAMP)`
	toBound := `occurred_at <= CAST("2026-10-03 11:00:00.000000+00:00" AS TIMESTAMP)`
	_, err := w.reader.AggregateAuditEvents(context.Background(), orgRead(business.AuditQuery{From: &from, To: &to}), business.AuditAggregationSpec{})
	require.NoError(t, err)
	_, err = w.reader.ExportAuditEvents(context.Background(), orgRead(business.AuditQuery{From: &from, To: &to}))
	require.NoError(t, err)
	for _, session := range w.fake.Sessions() {
		restriction := session.GetReadSession().GetReadOptions().GetRowRestriction()
		require.Contains(t, restriction, fromBound, "partitions before the window are pruned")
		require.Contains(t, restriction, toBound, "partitions after the window are pruned")
	}

	sessions := len(w.fake.Sessions())
	_, _, err = w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{From: &from, To: &to}))
	require.NoError(t, err)
	list := w.fake.Sessions()[sessions:]
	require.Len(t, list, 2, "the newest day, then the rest of the window")
	require.Contains(t, list[0].GetReadSession().GetReadOptions().GetRowRestriction(), toBound)
	require.Contains(t, list[1].GetReadSession().GetReadOptions().GetRowRestriction(), fromBound)
	for _, session := range list {
		restriction := session.GetReadSession().GetReadOptions().GetRowRestriction()
		require.Contains(t, restriction, "occurred_at >=")
		require.Contains(t, restriction, "occurred_at <")
	}
}

func TestEventTypeFiltersArePushedDown(t *testing.T) {
	w := newWarehouse(t)
	read := orgRead(business.AuditQuery{Category: "auth"})
	read.Types = business.AuditEventTypeIndex{
		"saas.auth.login":  {Category: "auth"},
		"saas.auth.logout": {Category: "auth"},
		"saas.org.created": {Category: "org"},
	}
	_, _, err := w.reader.ListAuditEvents(context.Background(), read)
	require.NoError(t, err)
	require.Contains(t, w.fake.Sessions()[0].GetReadSession().GetReadOptions().GetRowRestriction(),
		`event_type IN ("saas.auth.login", "saas.auth.logout")`)

	sessions := len(w.fake.Sessions())
	read.Query.EventType = "saas.org.created"
	entries, _, err := w.reader.ListAuditEvents(context.Background(), read)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Len(t, w.fake.Sessions(), sessions, "a read no type can satisfy reads nothing")
}

func TestReadsRefuseAMissingScopeAndAQueryOutsideIt(t *testing.T) {
	w := newWarehouse(t)
	ctx := context.Background()
	_, _, err := w.reader.ListAuditEvents(ctx, business.AuditRead{Query: business.AuditQuery{}})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = w.reader.AggregateAuditEvents(ctx, business.AuditRead{}, business.AuditAggregationSpec{})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = w.reader.ExportAuditEvents(ctx, business.AuditRead{})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = w.reader.LatestSourceSyncEvents(ctx, business.AuditReadScope{}, []string{"source-1"})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)

	_, _, err = w.reader.ListAuditEvents(ctx, business.AuditRead{
		Scope: business.OrganizationAuditScope(orgA), Query: business.AuditQuery{OrgID: orgB},
	})
	require.ErrorContains(t, err, "outside the organization")
	_, _, err = w.reader.ListAuditEvents(ctx, business.AuditRead{
		Scope: business.PlatformAuditScope(), Query: business.AuditQuery{OrgID: orgB},
	})
	require.ErrorContains(t, err, "outside the platform scope")
	require.Empty(t, w.fake.Sessions(), "a refused read reads nothing")
}

func TestScopeHoldsWhenTheStoreReturnsEveryRow(t *testing.T) {
	w := newWarehouse(t)
	w.fake.IgnoreRestrictions = true
	w.append(t, deployment, event(t, 1, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Hour), nil))
	w.append(t, deployment, event(t, 2, orgB, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Hour), nil))
	w.append(t, "deployment-2", event(t, 3, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Hour), nil))
	w.append(t, deployment, event(t, 4, "", business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Hour), nil))

	entries, _, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{}))
	require.NoError(t, err)
	require.Equal(t, []string{"001"}, ids(entries), "another organization, another deployment and the platform are out of scope")

	entries, _, err = w.reader.ListAuditEvents(context.Background(), business.AuditRead{Scope: business.PlatformAuditScope()})
	require.NoError(t, err)
	require.Equal(t, []string{"004", "002", "001"}, ids(entries), "the platform read spans organizations, never deployments")
}

func TestContentDetailsAreJoinedWithinTheirWindowOnly(t *testing.T) {
	w := newWarehouse(t)
	recent := event(t, 1, orgA, business.EventDatasourceSourceSynced, business.RetentionContent, now.Add(-time.Hour), map[string]any{"n": 1})
	expired := event(t, 2, orgA, business.EventDatasourceSourceSynced, business.RetentionContent, now.Add(-2*time.Hour), map[string]any{"n": 2})
	w.append(t, deployment, recent, expired)
	// The details table expires partitions at the content window; drop the row
	// that BigQuery would have expired.
	detailsPath := bigqueryfake.TablePath(project, dataset, bigquerystore.DetailsTable)
	kept := w.fake.Rows(detailsPath)[:1]
	w.fake.CreateTable(detailsPath, bigquerystore.DetailsSchema())
	require.NoError(t, w.fake.Insert(detailsPath, kept...))

	entries, _, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{}))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, map[string]any{"n": float64(1)}, entries[0].Payload)
	require.Nil(t, entries[1].Payload, "an event past the content window keeps its envelope, not its details")

	entries, _, err = w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{PayloadContains: map[string]any{"n": 2}}))
	require.NoError(t, err)
	require.Empty(t, entries, "a payload filter cannot match details that are gone")
}

func TestATransientStreamFailureResumesAtTheOffsetReached(t *testing.T) {
	w := newWarehouse(t)
	w.fake.StreamsPerSession, w.fake.RowsPerBatch = 1, 1
	for i := 1; i <= 4; i++ {
		w.append(t, deployment, event(t, i, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Duration(i)*time.Minute), nil))
	}
	var mu sync.Mutex
	failed := map[int64]bool{}
	w.fake.FailReadRows = func(_ string, offset int64) error {
		mu.Lock()
		defer mu.Unlock()
		if offset == 2 && !failed[offset] {
			failed[offset] = true
			return status.Error(codes.Unavailable, "transient")
		}
		return nil
	}
	entries, _, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{}))
	require.NoError(t, err)
	require.Equal(t, []string{"001", "002", "003", "004"}, ids(entries), "nothing lost, nothing read twice")

	w.fake.FailReadRows = func(string, int64) error { return status.Error(codes.PermissionDenied, "no getData") }
	_, _, err = w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{}))
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a permanent failure is not retried: %v", err)
}

func TestRestrictionLiteralsCannotEscapeTheirQuotes(t *testing.T) {
	w := newWarehouse(t)
	hostile := `x" OR TRUE OR "` + "\né\\"
	_, _, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{ResourceID: hostile}))
	require.NoError(t, err, "the fake parses the restriction back, so a broken literal fails here")
	restriction := w.fake.Sessions()[0].GetReadSession().GetReadOptions().GetRowRestriction()
	require.Contains(t, restriction, `resource_id = "x\" OR TRUE OR \"\U0000000A\U000000E9\\"`)
	require.False(t, strings.Contains(restriction, "\n"))
}

// Postgres reads an organization id of any spelling of a uuid as the one value
// it names, and the warehouse keeps the canonical form: a read of the same
// organization written otherwise must restrict on the canonical one.
func TestAnOrganizationIdIsReadInItsCanonicalForm(t *testing.T) {
	w := newWarehouse(t)
	w.append(t, deployment,
		event(t, 1, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Hour), nil),
		event(t, 2, orgB, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Hour), nil),
		event(t, 3, orgA, business.EventDatasourceSourceSynced, business.RetentionContent, now.Add(-time.Hour), map[string]any{"n": 3}),
	)
	ctx := context.Background()
	for _, spelling := range []string{strings.ToUpper(orgA), "{" + orgA + "}", strings.ReplaceAll(orgA, "-", "")} {
		read := business.AuditRead{Scope: business.OrganizationAuditScope(spelling), Query: business.AuditQuery{OrgID: spelling}}
		entries, _, err := w.reader.ListAuditEvents(ctx, read)
		require.NoError(t, err, spelling)
		require.Equal(t, []string{"003", "001"}, ids(entries), spelling)

		exported, err := w.reader.ExportAuditEvents(ctx, read)
		require.NoError(t, err, spelling)
		require.Equal(t, []string{"003", "001"}, ids(exported), spelling)

		buckets, err := w.reader.AggregateAuditEvents(ctx, read, business.AuditAggregationSpec{})
		require.NoError(t, err, spelling)
		var counted int64
		for _, bucket := range buckets {
			counted += bucket.Count
		}
		require.Equal(t, int64(2), counted, spelling)

		synced, err := w.reader.LatestSourceSyncEvents(ctx, business.OrganizationAuditScope(spelling), []string{"source-0"})
		require.NoError(t, err, spelling)
		require.Len(t, synced, 1, spelling)
	}
	for _, session := range w.fake.Sessions() {
		restriction := session.GetReadSession().GetReadOptions().GetRowRestriction()
		require.Contains(t, restriction, `org_id = "`+orgA+`"`)
		require.NotContains(t, strings.ToLower(restriction), strings.ToUpper(orgA[:8]))
	}

	_, err := w.reader.LatestSourceSyncEvents(ctx, business.OrganizationAuditScope("org-1"), []string{"source-0"})
	require.ErrorContains(t, err, "not a uuid", "the readable-source query refuses what the other reads refuse, as Postgres does")
}

// The budget of retries is for a stream that makes no headway. A long stream
// that breaks now and then, each time further on, is not out of retries: it
// has never failed twice at one place.
func TestAStreamThatKeepsMakingProgressIsNeverOutOfRetries(t *testing.T) {
	w := newWarehouse(t)
	w.fake.StreamsPerSession, w.fake.RowsPerBatch = 1, 1
	const events = 12
	for i := 1; i <= events; i++ {
		w.append(t, deployment, event(t, i, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Duration(i)*time.Minute), nil))
	}
	var mu sync.Mutex
	failures := 0
	failedAt := map[int64]bool{}
	w.fake.FailReadRows = func(_ string, offset int64) error {
		mu.Lock()
		defer mu.Unlock()
		if offset > 0 && !failedAt[offset] {
			failedAt[offset] = true
			failures++
			return status.Error(codes.Unavailable, "transient")
		}
		return nil
	}
	entries, _, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{PageSize: events}))
	require.NoError(t, err)
	require.Len(t, entries, events, "nothing lost")
	require.Equal(t, events-1, failures, "every row boundary broke the stream once, far more than the retries one stream without progress gets")
}

func TestAStreamThatFailsAtOnePlaceStopsAfterItsRetries(t *testing.T) {
	w := newWarehouse(t)
	w.fake.StreamsPerSession, w.fake.RowsPerBatch = 1, 1
	for i := 1; i <= 3; i++ {
		w.append(t, deployment, event(t, i, orgA, business.EventAuthLogin, business.RetentionSecurity, now.Add(-time.Duration(i)*time.Minute), nil))
	}
	var mu sync.Mutex
	attempts := 0
	w.fake.FailReadRows = func(_ string, offset int64) error {
		mu.Lock()
		defer mu.Unlock()
		if offset == 1 {
			attempts++
			return status.Error(codes.Unavailable, "transient")
		}
		return nil
	}
	_, _, err := w.reader.ListAuditEvents(context.Background(), orgRead(business.AuditQuery{}))
	require.ErrorContains(t, err, "after 5 attempts")
	require.Equal(t, 5, attempts)
}
