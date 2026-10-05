package bigquerystore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"cloud.google.com/go/bigquery/storage/apiv1/storagepb"
	"github.com/apache/arrow/go/v15/arrow"
	"github.com/apache/arrow/go/v15/arrow/array"
	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/apache/arrow/go/v15/arrow/memory"
	"github.com/googleapis/gax-go/v2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The read half of the BigQuery store (ADR 0009).
//
// The service that reads is the service that appends, and it holds
// bigquery.tables.updateData for the relay. With bigquery.jobs.create beside
// it, that identity could run DELETE, UPDATE and MERGE against the store of
// record, which append-only exists to prevent; so the deployment grants it no
// job permission at all. Reads therefore run no query: they go through the
// BigQuery Storage Read API, whose grants are bigquery.readsessions.create,
// .getData and .update and bigquery.tables.getData. Each read session carries a
// row restriction — this deployment, the caller's organization, the time window
// (so partitions outside it are pruned) and the event types when the read
// names them — and the service sorts, pages, deduplicates by event id, joins
// content details and aggregates the rows it streams back (auditeval).

// ReadSessionClient is the Storage Read API surface the reader uses.
// *storage.BigQueryReadClient (cloud.google.com/go/bigquery/storage/apiv1)
// satisfies it; the caller builds it with Application Default Credentials.
type ReadSessionClient interface {
	CreateReadSession(ctx context.Context, req *storagepb.CreateReadSessionRequest, opts ...gax.CallOption) (*storagepb.ReadSession, error)
	ReadRows(ctx context.Context, req *storagepb.ReadRowsRequest, opts ...gax.CallOption) (storagepb.BigQueryRead_ReadRowsClient, error)
}

// ReadConfig names what the reader reads and for whom.
type ReadConfig struct {
	Client ReadSessionClient
	// Project is the BigQuery project: the dataset's, and the one read sessions
	// are created (and billed) in.
	Project string
	Dataset string
	// DeploymentID confines every read to this deployment's events.
	DeploymentID string
	// MaxStreams bounds the streams one read session is split into and read in
	// parallel. Zero is DefaultReadStreams.
	MaxStreams int
	// Now is a seam for tests; nil is the clock.
	Now func() time.Time
}

// DefaultReadStreams is the parallelism of one read session.
const DefaultReadStreams = 4

// Newest-first windows: the activity list and the readable-source query read
// the newest day first, then windows growing eightfold (8, 64 and 512 days
// before it), and stop once the answer is complete; past those, one last read
// takes everything older. Most pages fill from the first window.
const (
	firstReadWindow       = 24 * time.Hour
	readWindowGrowth      = 8
	boundedReadWindows    = 4
	readRowsRetryAttempts = 5
)

// Reader reads the events and details tables.
type Reader struct {
	client       ReadSessionClient
	project      string
	dataset      string
	deploymentID string
	maxStreams   int
	now          func() time.Time
}

// NewReader validates cfg and builds a reader.
func NewReader(cfg ReadConfig) (*Reader, error) {
	if cfg.Client == nil {
		return nil, errors.New("bigquery audit store: read client is required")
	}
	for name, value := range map[string]string{"project": cfg.Project, "dataset": cfg.Dataset, "deployment id": cfg.DeploymentID} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("bigquery audit store: %s is required to read", name)
		}
	}
	reader := &Reader{
		client:       cfg.Client,
		project:      cfg.Project,
		dataset:      cfg.Dataset,
		deploymentID: cfg.DeploymentID,
		maxStreams:   cfg.MaxStreams,
		now:          cfg.Now,
	}
	if reader.maxStreams <= 0 {
		reader.maxStreams = DefaultReadStreams
	}
	if reader.now == nil {
		reader.now = time.Now
	}
	return reader, nil
}

var (
	eventFields = []string{
		"event_id", "deployment_id", "org_id", "actor_id", "actor_type", "event_type", "schema_version",
		"resource", "resource_id", "occurred_at", "ip_address", "impersonated_by", "is_impersonated",
		"client_id", "retention_class", "details_sha256", "details",
	}
	detailFields     = []string{"event_id", "deployment_id", "org_id", "occurred_at", "details_sha256", "details"}
	sourceSyncFields = []string{"event_id", "deployment_id", "org_id", "actor_id", "event_type", "resource", "resource_id", "occurred_at"}
)

// window is a span of occurred_at: [lo, hi], or [lo, hi) when hiExclusive. A
// nil bound is open.
type window struct {
	lo, hi      *time.Time
	hiExclusive bool
}

// newestFirst yields the windows a newest-first read walks, from upper
// (inclusive, or open when nil) down to floor (inclusive, or open when nil),
// until visit says the answer is complete.
func (r *Reader) newestFirst(upper, floor *time.Time, visit func(window) (bool, error)) error {
	anchor := r.now()
	if upper != nil {
		anchor = *upper
	}
	current := window{hi: upper}
	span := firstReadWindow
	lo := anchor.Add(-span)
	for step := 1; ; step++ {
		last := false
		switch {
		case floor != nil && !lo.After(*floor):
			current.lo, last = floor, true
		case step >= boundedReadWindows+1:
			current.lo, last = floor, true
		default:
			bound := lo
			current.lo = &bound
		}
		done, err := visit(current)
		if err != nil || done || last {
			return err
		}
		next := *current.lo
		current = window{hi: &next, hiExclusive: true}
		span *= readWindowGrowth
		lo = next.Add(-span)
	}
}

// scope adds the read's scope to a restriction: this deployment always, and
// the organization unless the read is the explicit platform read.
func (r *Reader) scope(scope business.AuditReadScope) *restriction {
	where := &restriction{}
	where.eq("deployment_id", r.deploymentID)
	if !scope.Platform() {
		where.eq("org_id", scope.OrgID())
	}
	return where
}

func (w window) restrict(where *restriction) {
	if w.lo != nil {
		where.timeBound("occurred_at", ">=", *w.lo)
	}
	if w.hi != nil {
		op := "<="
		if w.hiExclusive {
			op = "<"
		}
		where.timeBound("occurred_at", op, *w.hi)
	}
}

// candidates is a read's restriction over a window: the scope, the window,
// and every envelope predicate of the query that a restriction can carry. ok
// is false when the read can match no event type at all.
func (r *Reader) candidates(m *auditeval.Matcher, q business.AuditQuery, w window) (*restriction, bool) {
	where := r.scope(m.Scope())
	w.restrict(where)
	if types, restricted := m.EventTypes(); restricted {
		if len(types) == 0 {
			return nil, false
		}
		if len(types) == 1 {
			where.eq("event_type", types[0])
		} else {
			where.in("event_type", types)
		}
	}
	if m.ActorID() != "" {
		where.eq("actor_id", m.ActorID())
	}
	if q.Resource != "" {
		where.eq("resource", q.Resource)
	}
	if q.ResourceID != "" {
		where.eq("resource_id", q.ResourceID)
	}
	if q.ClientID != "" {
		where.eq("client_id", q.ClientID)
	}
	return where, true
}

// readEvents streams the events in a window that the read could match, each
// joined to its content details when withDetails, and hands each to visit
// (serially). A row of another deployment is never handed on, whatever the
// session returned.
func (r *Reader) readEvents(ctx context.Context, m *auditeval.Matcher, q business.AuditQuery, w window, withDetails bool, visit func(*auditeval.Event) error) error {
	where, ok := r.candidates(m, q, w)
	if !ok {
		return nil
	}
	var details map[string]storedDetail
	if withDetails {
		detailsWhere := r.scope(m.Scope())
		w.restrict(detailsWhere)
		if types, restricted := m.EventTypes(); restricted {
			if len(types) == 1 {
				detailsWhere.eq("event_type", types[0])
			} else {
				detailsWhere.in("event_type", types)
			}
		}
		var err error
		if details, err = r.readDetails(ctx, detailsWhere); err != nil {
			return err
		}
	}
	return r.scan(ctx, EventsTable, eventFields, where, func(row arrowRow) error {
		stored, err := decodeEventRow(row)
		if err != nil {
			return err
		}
		if stored.deploymentID != r.deploymentID {
			return nil
		}
		event := stored.event()
		if withDetails && stored.retentionClass != string(business.RetentionSecurity) {
			if detail, ok := details[event.Entry.ID]; ok && detail.sha256 == stored.detailsSHA256 {
				event.Details, event.HasDetails = detail.details, true
			}
		}
		return visit(event)
	})
}

// storedDetail is one details-table row: the canonical details of a
// content-class event and their hash.
type storedDetail struct {
	sha256  string
	details string
}

func (r *Reader) readDetails(ctx context.Context, where *restriction) (map[string]storedDetail, error) {
	details := map[string]storedDetail{}
	err := r.scan(ctx, DetailsTable, detailFields, where, func(row arrowRow) error {
		eventID, err := row.str("event_id")
		if err != nil {
			return err
		}
		deploymentID, err := row.str("deployment_id")
		if err != nil {
			return err
		}
		if deploymentID != r.deploymentID {
			return nil
		}
		sha, err := row.str("details_sha256")
		if err != nil {
			return err
		}
		text, err := row.str("details")
		if err != nil {
			return err
		}
		details[eventID] = storedDetail{sha256: sha, details: text}
		return nil
	})
	return details, err
}

// joinDetails reads the details of the given events that have none yet — the
// content-class events of a list page that did not need them to match — in
// one session bounded by their occurrences and ids. An event whose details are
// past the content window keeps none.
func (r *Reader) joinDetails(ctx context.Context, scope business.AuditReadScope, events []*auditeval.Event) error {
	var ids []string
	var lo, hi time.Time
	for _, event := range events {
		if event.HasDetails {
			continue
		}
		at := event.Entry.CreatedAt
		if len(ids) == 0 || at.Before(lo) {
			lo = at
		}
		if len(ids) == 0 || at.After(hi) {
			hi = at
		}
		ids = append(ids, event.Entry.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	where := r.scope(scope)
	window{lo: &lo, hi: &hi}.restrict(where)
	where.in("event_id", ids)
	details, err := r.readDetails(ctx, where)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.HasDetails {
			continue
		}
		if detail, ok := details[event.Entry.ID]; ok && detail.sha256 == event.DetailsSHA256 {
			event.Details, event.HasDetails = detail.details, true
		}
	}
	return nil
}

// ListAuditEvents implements business.AuditReader: the newest windows first,
// stopping once the page and the proof of a next one are in hand.
func (r *Reader) ListAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, string, error) {
	m, err := auditeval.NewMatcher(read)
	if err != nil {
		return nil, "", err
	}
	page, err := auditeval.NewPage(read.Query)
	if err != nil {
		return nil, "", err
	}
	upper := m.To()
	if before := page.Before(); before != nil && (upper == nil || before.Before(*upper)) {
		upper = before
	}
	// A payload filter needs every candidate's details to decide; otherwise
	// only the settled page's content details are read, afterwards.
	withDetails := m.NeedsPayload()
	err = r.newestFirst(upper, m.From(), func(w window) (bool, error) {
		err := r.readEvents(ctx, m, read.Query, w, withDetails, func(event *auditeval.Event) error {
			ok, err := m.Match(event)
			if err != nil || !ok {
				return err
			}
			page.Offer(event)
			return nil
		})
		return page.Full(), err
	})
	if err != nil {
		return nil, "", err
	}
	events, next := page.Result()
	if !withDetails {
		if err := r.joinDetails(ctx, m.Scope(), events); err != nil {
			return nil, "", err
		}
	}
	return results(events), next, nil
}

func results(events []*auditeval.Event) []business.AuditEntry {
	if len(events) == 0 {
		return nil
	}
	out := make([]business.AuditEntry, len(events))
	for i, event := range events {
		out[i] = event.Result()
	}
	return out
}

// needsPayload reports whether an aggregation reads payloads: a payload filter,
// a payload dimension, or a metric over a payload key.
func needsPayload(m *auditeval.Matcher, spec business.AuditAggregationSpec) bool {
	if m.NeedsPayload() {
		return true
	}
	for _, dimension := range spec.GroupBy {
		if strings.HasPrefix(dimension, "payload:") {
			return true
		}
	}
	for _, metric := range spec.Metrics {
		if strings.HasPrefix(metric.Field, "payload:") {
			return true
		}
	}
	return false
}

// AggregateAuditEvents implements business.AuditReader: one read of the
// query's window, deduplicated and aggregated in the service.
func (r *Reader) AggregateAuditEvents(ctx context.Context, read business.AuditRead, spec business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	m, err := auditeval.NewMatcher(read)
	if err != nil {
		return nil, err
	}
	aggregator, err := auditeval.NewAggregator(spec, read.Types)
	if err != nil {
		return nil, err
	}
	seen := auditeval.NewDedupe()
	w := window{lo: m.From(), hi: m.To()}
	err = r.readEvents(ctx, m, read.Query, w, needsPayload(m, spec), func(event *auditeval.Event) error {
		ok, err := m.Match(event)
		if err != nil || !ok || !seen.First(event.Entry.ID) {
			return err
		}
		return aggregator.Add(event)
	})
	if err != nil {
		return nil, err
	}
	return aggregator.Buckets(), nil
}

// ExportAuditEvents implements business.AuditReader: every matching event of
// the query's window, once each, newest first, with its payload.
func (r *Reader) ExportAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, error) {
	m, err := auditeval.NewMatcher(read)
	if err != nil {
		return nil, err
	}
	seen := auditeval.NewDedupe()
	var events []*auditeval.Event
	w := window{lo: m.From(), hi: m.To()}
	err = r.readEvents(ctx, m, read.Query, w, true, func(event *auditeval.Event) error {
		ok, err := m.Match(event)
		if err != nil || !ok || !seen.First(event.Entry.ID) {
			return err
		}
		events = append(events, event)
		return nil
	})
	if err != nil {
		return nil, err
	}
	auditeval.SortNewestFirst(events)
	return results(events), nil
}

// LatestSourceSyncEvents implements business.AuditSourceSyncReader: the newest
// windows first, until every source has its newest request or the history
// runs out.
func (r *Reader) LatestSourceSyncEvents(ctx context.Context, scope business.AuditReadScope, sources []string) (map[string]business.AuditSourceSyncEvent, error) {
	scope, err := auditeval.CanonicalScope(scope)
	if err != nil {
		return nil, err
	}
	if scope.Platform() {
		return nil, errors.New("bigquery audit store: the readable-source query reads one organization")
	}
	out := map[string]business.AuditSourceSyncEvent{}
	wanted := map[string]bool{}
	var ids []string
	for _, source := range sources {
		if !wanted[source] {
			wanted[source] = true
			ids = append(ids, source)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	newestIDs := map[string]string{}
	eventType := string(business.EventDatasourceSourceSynced)
	err = r.newestFirst(nil, nil, func(w window) (bool, error) {
		where := r.scope(scope)
		w.restrict(where)
		where.eq("event_type", eventType)
		where.eq("resource", "datasource")
		where.in("resource_id", ids)
		err := r.scan(ctx, EventsTable, sourceSyncFields, where, func(row arrowRow) error {
			stored, err := decodeEventRow(row)
			if err != nil {
				return err
			}
			entry := stored.event().Entry
			if stored.deploymentID != r.deploymentID || entry.OrgID != scope.OrgID() || string(entry.EventType) != eventType ||
				entry.Resource != "datasource" || !wanted[entry.ResourceID] {
				return nil
			}
			current, found := out[entry.ResourceID]
			if !found || entry.CreatedAt.After(current.RequestedAt) ||
				(entry.CreatedAt.Equal(current.RequestedAt) && entry.ID > newestIDs[entry.ResourceID]) {
				out[entry.ResourceID] = business.AuditSourceSyncEvent{RequestedAt: entry.CreatedAt, ActorID: entry.ActorID}
				newestIDs[entry.ResourceID] = entry.ID
			}
			return nil
		})
		return len(out) == len(ids), err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadStoredAuditEvents implements business.AuditHistoryReader: one session
// over each table for the window, every copy of every event of this
// deployment, with the details each copy has. A content-class event's details
// come from the details table; when the copies there disagree, a copy whose
// hash does not match the event's is the one reported, so the disagreement is
// what verification sees.
func (r *Reader) ReadStoredAuditEvents(ctx context.Context, from, to time.Time, visit func(business.StoredAuditEvent) error) error {
	w := window{lo: &from, hi: &to, hiExclusive: true}
	detailsWhere := r.scope(business.PlatformAuditScope())
	w.restrict(detailsWhere)
	copies := map[string][]storedDetail{}
	err := r.scan(ctx, DetailsTable, detailFields, detailsWhere, func(row arrowRow) error {
		var detail storedDetail
		var eventID, deploymentID string
		for name, into := range map[string]*string{
			"event_id": &eventID, "deployment_id": &deploymentID, "details_sha256": &detail.sha256, "details": &detail.details,
		} {
			value, err := row.str(name)
			if err != nil {
				return err
			}
			*into = value
		}
		if deploymentID == r.deploymentID {
			copies[eventID] = append(copies[eventID], detail)
		}
		return nil
	})
	if err != nil {
		return err
	}
	where := r.scope(business.PlatformAuditScope())
	w.restrict(where)
	return r.scan(ctx, EventsTable, eventFields, where, func(row arrowRow) error {
		stored, err := decodeEventRow(row)
		if err != nil {
			return err
		}
		if stored.deploymentID != r.deploymentID {
			return nil
		}
		event := business.StoredAuditEvent{
			DeploymentID:  stored.deploymentID,
			Entry:         stored.entry,
			Retention:     business.AuditRetentionClass(stored.retentionClass),
			DetailsSHA256: stored.detailsSHA256,
			Details:       stored.details,
			HasDetails:    stored.hasDetails,
		}
		if event.Retention != business.RetentionSecurity {
			event.Details, event.HasDetails = "", false
			for _, detail := range copies[stored.entry.ID] {
				event.Details, event.HasDetails = detail.details, true
				if detail.sha256 != stored.detailsSHA256 {
					break
				}
			}
		}
		return visit(event)
	})
}

// scan creates one read session over table and hands every row it returns to
// visit, serially. The session's streams are read in parallel; a stream broken
// by a transient error is reopened at the row it reached.
func (r *Reader) scan(ctx context.Context, table string, fields []string, where *restriction, visit func(arrowRow) error) error {
	if where.err != nil {
		return where.err
	}
	session, err := r.client.CreateReadSession(ctx, &storagepb.CreateReadSessionRequest{
		Parent: "projects/" + r.project,
		ReadSession: &storagepb.ReadSession{
			Table:      fmt.Sprintf("projects/%s/datasets/%s/tables/%s", r.project, r.dataset, table),
			DataFormat: storagepb.DataFormat_ARROW,
			ReadOptions: &storagepb.ReadSession_TableReadOptions{
				SelectedFields: fields,
				RowRestriction: where.String(),
			},
		},
		MaxStreamCount: int32(r.maxStreams),
	})
	if err != nil {
		return fmt.Errorf("bigquery audit store: create read session on %s: %w", table, err)
	}
	if len(session.GetStreams()) == 0 {
		return nil
	}
	schema := session.GetArrowSchema().GetSerializedSchema()
	if len(schema) == 0 {
		return fmt.Errorf("bigquery audit store: read session on %s returned no Arrow schema", table)
	}
	var mu sync.Mutex
	group, ctx := errgroup.WithContext(ctx)
	for _, stream := range session.GetStreams() {
		name := stream.GetName()
		group.Go(func() error {
			return r.readStream(ctx, table, name, schema, func(row arrowRow) error {
				mu.Lock()
				defer mu.Unlock()
				return visit(row)
			})
		})
	}
	return group.Wait()
}

// readStream reads one stream to its end, reopening it at the offset reached
// after a transient failure. The retries are for a stream that makes no
// headway: a failure that follows rows read since the last one starts the
// count, and the backoff, over, so a long stream that breaks now and then is
// never out of retries while it keeps moving.
func (r *Reader) readStream(ctx context.Context, table, stream string, schema []byte, visit func(arrowRow) error) error {
	var offset int64
	newBackoff := func() gax.Backoff { return gax.Backoff{Initial: 100 * time.Millisecond, Max: 5 * time.Second} }
	backoff := newBackoff()
	failures := 0
	for {
		reached := offset
		rows, err := r.client.ReadRows(ctx, &storagepb.ReadRowsRequest{ReadStream: stream, Offset: offset})
		if err == nil {
			offset, err = r.consume(rows, schema, offset, visit)
			if err == nil {
				return nil
			}
			if offset > reached {
				failures, backoff = 0, newBackoff()
			}
		}
		var decodeErr decodeError
		if errors.As(err, &decodeErr) || !retryableRead(err) || ctx.Err() != nil {
			return fmt.Errorf("bigquery audit store: read %s stream %s: %w", table, stream, err)
		}
		failures++
		if failures >= readRowsRetryAttempts {
			return fmt.Errorf("bigquery audit store: read %s stream %s after %d attempts: %w", table, stream, failures, err)
		}
		if err := gax.Sleep(ctx, backoff.Pause()); err != nil {
			return err
		}
	}
}

// consume reads responses until the stream ends, returning the offset reached.
func (r *Reader) consume(rows storagepb.BigQueryRead_ReadRowsClient, schema []byte, offset int64, visit func(arrowRow) error) (int64, error) {
	for {
		response, err := rows.Recv()
		if errors.Is(err, io.EOF) {
			return offset, nil
		}
		if err != nil {
			return offset, err
		}
		batch := response.GetArrowRecordBatch()
		if batch == nil || len(batch.GetSerializedRecordBatch()) == 0 {
			continue
		}
		// The offset to resume from counts the rows decoded, which is what the
		// response's row count reports when it is set.
		rows, err := decodeBatch(schema, batch.GetSerializedRecordBatch(), visit)
		offset += int64(rows)
		if err != nil {
			return offset, err
		}
	}
}

func retryableRead(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.DeadlineExceeded, codes.Internal, codes.Unavailable, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// decodeError is a row the reader could not decode: a schema the store did
// not write, never worth retrying.
type decodeError struct{ err error }

func (e decodeError) Error() string { return e.err.Error() }
func (e decodeError) Unwrap() error { return e.err }

// decodeBatch decodes one serialized Arrow record batch against the session's
// serialized schema, hands each row to visit, and returns how many rows it
// visited. A visit error is returned as it is; a decoding failure as a
// decodeError.
//
// BigQuery sends the schema once, on the session, and each batch without it,
// so the two are read as one stream. A batch that carries its own schema is a
// whole stream already — the BigQuery emulator sends them so — and is read
// alone.
func decodeBatch(schema, batch []byte, visit func(arrowRow) error) (int, error) {
	stream := io.MultiReader(bytes.NewReader(schema), bytes.NewReader(batch))
	if carriesSchema(batch) {
		stream = bytes.NewReader(batch)
	}
	reader, err := ipc.NewReader(stream, ipc.WithAllocator(memory.DefaultAllocator))
	if err != nil {
		return 0, decodeError{fmt.Errorf("decode arrow batch: %w", err)}
	}
	defer reader.Release()
	visited := 0
	for reader.Next() {
		record := reader.Record()
		columns := make(map[string]arrow.Array, record.NumCols())
		for i, field := range record.Schema().Fields() {
			columns[field.Name] = record.Column(i)
		}
		for i := 0; i < int(record.NumRows()); i++ {
			if err := visit(arrowRow{columns: columns, index: i}); err != nil {
				return visited, err
			}
			visited++
		}
	}
	if err := reader.Err(); err != nil {
		return visited, decodeError{fmt.Errorf("decode arrow batch: %w", err)}
	}
	return visited, nil
}

// carriesSchema reports whether a serialized batch begins with a schema message.
func carriesSchema(batch []byte) bool {
	messages := ipc.NewMessageReader(bytes.NewReader(batch))
	defer messages.Release()
	message, err := messages.Message()
	if err != nil {
		return false
	}
	return message.Type() == ipc.MessageSchema
}

// arrowRow is one row of a decoded record batch.
type arrowRow struct {
	columns map[string]arrow.Array
	index   int
}

func (r arrowRow) column(name string) (arrow.Array, error) {
	column, ok := r.columns[name]
	if !ok {
		return nil, decodeError{fmt.Errorf("column %s is missing", name)}
	}
	return column, nil
}

// str is a STRING column's value; NULL is "".
func (r arrowRow) str(name string) (string, error) {
	column, err := r.column(name)
	if err != nil {
		return "", err
	}
	if column.IsNull(r.index) {
		return "", nil
	}
	switch values := column.(type) {
	case *array.String:
		return values.Value(r.index), nil
	case *array.LargeString:
		return values.Value(r.index), nil
	default:
		return "", decodeError{fmt.Errorf("column %s is %s, want a string", name, column.DataType())}
	}
}

// optional is a nullable STRING column's value and whether it is set.
func (r arrowRow) optional(name string) (string, bool, error) {
	column, err := r.column(name)
	if err != nil {
		return "", false, err
	}
	if column.IsNull(r.index) {
		return "", false, nil
	}
	value, err := r.str(name)
	return value, err == nil, err
}

func (r arrowRow) integer(name string) (int64, error) {
	column, err := r.column(name)
	if err != nil {
		return 0, err
	}
	values, ok := column.(*array.Int64)
	if !ok {
		return 0, decodeError{fmt.Errorf("column %s is %s, want int64", name, column.DataType())}
	}
	return values.Value(r.index), nil
}

func (r arrowRow) boolean(name string) (bool, error) {
	column, err := r.column(name)
	if err != nil {
		return false, err
	}
	values, ok := column.(*array.Boolean)
	if !ok {
		return false, decodeError{fmt.Errorf("column %s is %s, want bool", name, column.DataType())}
	}
	return !values.IsNull(r.index) && values.Value(r.index), nil
}

// timestamp is a TIMESTAMP column's value in the process's local zone — the
// zone a Postgres read hands back — at the microsecond precision both keep.
func (r arrowRow) timestamp(name string) (time.Time, error) {
	column, err := r.column(name)
	if err != nil {
		return time.Time{}, err
	}
	values, ok := column.(*array.Timestamp)
	if !ok {
		return time.Time{}, decodeError{fmt.Errorf("column %s is %s, want a timestamp", name, column.DataType())}
	}
	value := int64(values.Value(r.index))
	switch values.DataType().(*arrow.TimestampType).Unit {
	case arrow.Second:
		return time.Unix(value, 0), nil
	case arrow.Millisecond:
		return time.UnixMilli(value), nil
	case arrow.Microsecond:
		return time.UnixMicro(value), nil
	default:
		return time.Unix(0, value), nil
	}
}

// storedEvent is one events-table row.
type storedEvent struct {
	entry          business.AuditEntry
	deploymentID   string
	retentionClass string
	detailsSHA256  string
	details        string
	hasDetails     bool
}

func (s storedEvent) event() *auditeval.Event {
	return &auditeval.Event{Entry: s.entry, Details: s.details, HasDetails: s.hasDetails, DetailsSHA256: s.detailsSHA256}
}

// decodeEventRow reads the selected columns of an events-table row; a column
// the read did not select is left zero.
func decodeEventRow(row arrowRow) (storedEvent, error) {
	var stored storedEvent
	entry := &stored.entry
	text := func(name string, into *string) error {
		if _, selected := row.columns[name]; !selected {
			return nil
		}
		value, err := row.str(name)
		*into = value
		return err
	}
	var eventType string
	for name, into := range map[string]*string{
		"event_id": &entry.ID, "deployment_id": &stored.deploymentID, "org_id": &entry.OrgID,
		"actor_id": &entry.ActorID, "actor_type": &entry.ActorType, "event_type": &eventType,
		"resource": &entry.Resource, "resource_id": &entry.ResourceID, "ip_address": &entry.IPAddress,
		"impersonated_by": &entry.ImpersonatedBy, "client_id": &entry.ClientID,
		"retention_class": &stored.retentionClass, "details_sha256": &stored.detailsSHA256,
	} {
		if err := text(name, into); err != nil {
			return storedEvent{}, err
		}
	}
	entry.EventType = business.EventType(eventType)
	if _, selected := row.columns["schema_version"]; selected {
		version, err := row.integer("schema_version")
		if err != nil {
			return storedEvent{}, err
		}
		entry.SchemaVersion = int(version)
	}
	if _, selected := row.columns["is_impersonated"]; selected {
		impersonated, err := row.boolean("is_impersonated")
		if err != nil {
			return storedEvent{}, err
		}
		entry.IsImpersonated = impersonated
	}
	occurredAt, err := row.timestamp("occurred_at")
	if err != nil {
		return storedEvent{}, err
	}
	entry.CreatedAt = occurredAt
	if _, selected := row.columns["details"]; selected {
		if stored.details, stored.hasDetails, err = row.optional("details"); err != nil {
			return storedEvent{}, err
		}
	}
	return stored, nil
}
