package clickhousestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// The read half of the ClickHouse store (ADR 0009).
//
// Postgres row-level security does not reach ClickHouse, so every statement
// carries the read's scope itself: this deployment always, and the caller's
// organization unless the read is the explicit platform read. A read without a
// scope, or whose query names another organization than its scope, is refused
// before any statement runs (business.AuditRead.Validate, through
// auditeval.NewMatcher).
//
// ClickHouse does the work it reproduces exactly: the scope, the time window,
// the envelope filters (actor, event type and the types a category or
// namespace names, resource, client), deduplication by event id (an event
// delivered twice is stored twice), the activity list's order and keyset
// paging, the aggregation (aggregate.go), the readable-source query and the
// join of content details by id and hash. A payload filter whose pairs are all
// top-level strings, booleans or nulls is pushed down too. The service
// evaluates what ClickHouse cannot reproduce: containment of a number or a
// nested value (jsonb compares numbers at any precision and recurses into
// containers), the ->> text of a fractional number or a nested value, and
// details ClickHouse's parser refuses. Every row a read returns is still
// matched by the service (auditeval.Matcher), which is the authority on every
// predicate: a statement may return a candidate the service then drops, never
// drop one the service would keep.

// errUnscopedRow is a row outside the read's scope or deployment, which no
// statement here can return; it fails the read rather than being dropped.
var errUnscopedRow = errors.New("clickhouse audit store: a statement returned an event outside the read's scope")

// readContext carries the settings every read runs with.
func (s *Store) readContext(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(s.readSettings))
}

// storedRow is one events-table row as a read selects it (eventColumns).
type storedRow struct {
	deploymentID   string
	entry          business.AuditEntry
	retentionClass string
	detailsSHA256  string
	details        string
}

// scanStored reads a row selected as eventColumns, with the details column
// last: the events row's own, or the joined payload.
func scanStored(rows driver.Rows, extra ...any) (storedRow, error) {
	var row storedRow
	var eventType string
	var schemaVersion int64
	var occurredAt time.Time
	entry := &row.entry
	dest := []any{
		&row.deploymentID, &entry.ID, &entry.OrgID, &entry.ActorID, &entry.ActorType, &eventType, &schemaVersion,
		&entry.Resource, &entry.ResourceID, &occurredAt, &entry.IPAddress, &entry.ImpersonatedBy, &entry.IsImpersonated,
		&entry.ClientID, &row.retentionClass, &row.detailsSHA256, &row.details,
	}
	if err := rows.Scan(append(dest, extra...)...); err != nil {
		return storedRow{}, err
	}
	entry.EventType = business.EventType(eventType)
	entry.SchemaVersion = int(schemaVersion)
	// The process's local zone at microsecond precision: what a Postgres read
	// hands back.
	entry.CreatedAt = time.UnixMicro(occurredAt.UnixMicro())
	return row, nil
}

func (r storedRow) event() *auditeval.Event {
	return &auditeval.Event{Entry: r.entry, Details: r.details, HasDetails: r.details != "", DetailsSHA256: r.detailsSHA256}
}

// inScope reports whether a row is one the read may see.
func (s *Store) inScope(m *auditeval.Matcher, row storedRow) bool {
	return row.deploymentID == s.deploymentID && m.InScope(row.entry)
}

// scan runs a row-returning statement and hands each row to visit, which
// returns false to stop early.
func (s *Store) scan(ctx context.Context, statement string, args []any, visit func(driver.Rows) (bool, error)) error {
	// Closing rows reads them to the end; cancelling the statement's context is
	// what stops the server, so a read that has its answer cancels first.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := s.conn.Query(s.readContext(ctx), statement, args...)
	if err != nil {
		return fmt.Errorf("clickhouse audit store: read: %w", err)
	}
	for rows.Next() {
		more, err := visit(rows)
		if err != nil || !more {
			cancel()
			_ = rows.Close()
			return err
		}
	}
	if err := closeRows(rows); err != nil {
		return fmt.Errorf("clickhouse audit store: read: %w", err)
	}
	return nil
}

// ListAuditEvents implements business.AuditReader: the page in keyset order,
// read in chunks newest first, each chunk deduplicated, ordered and limited in
// ClickHouse. The page is complete once it holds a whole page and the proof of
// another, or the history runs out.
func (s *Store) ListAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, string, error) {
	m, err := auditeval.NewMatcher(read)
	if err != nil {
		return nil, "", err
	}
	page, err := auditeval.NewPage(read.Query)
	if err != nil {
		return nil, "", err
	}
	pl := newPlan(m, read.Query)
	if pl.none {
		return nil, "", nil
	}
	// A payload filter the statement carries returns matches alone, so a chunk
	// of one more than a page fills it; one the service evaluates may reject
	// candidates, so it reads further at a time.
	chunk := page.Size() + 1
	if m.NeedsPayload() && pl.payload == nil && chunk < unpushedChunk {
		chunk = unpushedChunk
	}
	at, id, after := page.After()
	for {
		events, err := s.listChunk(ctx, pl, at, id, after, chunk)
		if err != nil {
			return nil, "", err
		}
		if m.NeedsPayload() {
			if err := s.joinDetails(ctx, m.Scope(), events); err != nil {
				return nil, "", err
			}
		}
		for _, event := range events {
			ok, err := m.Match(event)
			if err != nil {
				return nil, "", err
			}
			if ok {
				page.Offer(event)
			}
		}
		if page.Full() || len(events) < chunk {
			break
		}
		last := events[len(events)-1].Entry
		at, id, after = last.CreatedAt, last.ID, true
	}
	events, next := page.Result()
	if !m.NeedsPayload() {
		if err := s.joinDetails(ctx, m.Scope(), events); err != nil {
			return nil, "", err
		}
	}
	return results(events), next, nil
}

// unpushedChunk is how many candidates a list reads at a time when the service
// evaluates its payload filter.
const unpushedChunk = 1000

// listChunk reads up to limit events strictly after the keyset position
// (when after), newest first, each once.
func (s *Store) listChunk(ctx context.Context, pl plan, at time.Time, id string, after bool, limit int) ([]*auditeval.Event, error) {
	p := &sqlParams{}
	w, ok := s.eventsWhere(p, pl)
	if !ok {
		return nil, nil
	}
	if after {
		bound := p.instant(at)
		w.and("occurred_at <= " + bound)
		w.and("(occurred_at, event_id) < (" + bound + ", " + p.text(id) + ")")
	}
	statement := "SELECT " + eventColumns + " FROM " + EventsTable + " WHERE " + w.String() +
		" ORDER BY occurred_at DESC, event_id DESC LIMIT 1 BY event_id LIMIT " + p.count(limit)
	var events []*auditeval.Event
	err := s.scan(ctx, statement, p.args, func(rows driver.Rows) (bool, error) {
		row, err := scanStored(rows)
		if err != nil {
			return false, fmt.Errorf("clickhouse audit store: read %s: %w", EventsTable, err)
		}
		if !s.inScope(pl.m, row) {
			return false, errUnscopedRow
		}
		events = append(events, row.event())
		return true, nil
	})
	return events, err
}

// joinDetails reads the details of the given events that have none yet — the
// content-class events — in one statement bounded by their occurrences and
// ids. An event whose details are past the content window keeps none.
func (s *Store) joinDetails(ctx context.Context, scope business.AuditReadScope, events []*auditeval.Event) error {
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
	p := &sqlParams{}
	w := s.scope(p, scope)
	w.window(p, &lo, &hi)
	w.and("has(" + p.texts(ids) + ", event_id)")
	statement := "SELECT event_id, details_sha256, any(details) FROM " + DetailsTable + " WHERE " + w.String() +
		" GROUP BY event_id, details_sha256"
	type key struct{ id, sha string }
	details := map[key]string{}
	err := s.scan(ctx, statement, p.args, func(rows driver.Rows) (bool, error) {
		var id, sha, text string
		if err := rows.Scan(&id, &sha, &text); err != nil {
			return false, fmt.Errorf("clickhouse audit store: read %s: %w", DetailsTable, err)
		}
		details[key{id, sha}] = text
		return true, nil
	})
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.HasDetails {
			continue
		}
		if text, ok := details[key{event.Entry.ID, event.DetailsSHA256}]; ok && text != "" {
			event.Details, event.HasDetails = text, true
		}
	}
	return nil
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

// joined streams every matching event once, with its details, to visit — in
// list order when ordered — after the service has matched it.
func (s *Store) joined(ctx context.Context, m *auditeval.Matcher, pl plan, ordered bool, visit func(*auditeval.Event) error) error {
	p := &sqlParams{}
	statement, ok := s.joinedEvents(p, pl, ordered)
	if !ok {
		return nil
	}
	return s.scan(ctx, statement, p.args, func(rows driver.Rows) (bool, error) {
		row, err := scanStored(rows)
		if err != nil {
			return false, fmt.Errorf("clickhouse audit store: read %s: %w", EventsTable, err)
		}
		if !s.inScope(m, row) {
			return false, errUnscopedRow
		}
		event := row.event()
		ok, err := m.Match(event)
		if err != nil || !ok {
			return err == nil, err
		}
		return true, visit(event)
	})
}

// ExportAuditEvents implements business.AuditReader: every matching event,
// once each, newest first, with its payload.
func (s *Store) ExportAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, error) {
	m, err := auditeval.NewMatcher(read)
	if err != nil {
		return nil, err
	}
	var events []*auditeval.Event
	err = s.joined(ctx, m, newPlan(m, read.Query), true, func(event *auditeval.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results(events), nil
}

// AggregateAuditEvents implements business.AuditReader: one GROUP BY in
// ClickHouse when it is exact for this read (aggregate.go), and otherwise the
// deduplicated, matched events aggregated in the service.
func (s *Store) AggregateAuditEvents(ctx context.Context, read business.AuditRead, spec business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	m, err := auditeval.NewMatcher(read)
	if err != nil {
		return nil, err
	}
	// The service's aggregator is built either way: it refuses what Postgres
	// refuses, and it is the fallback.
	aggregator, err := auditeval.NewAggregator(spec, read.Types)
	if err != nil {
		return nil, err
	}
	pl := newPlan(m, read.Query)
	if pl.none {
		return nil, nil
	}
	if !m.NeedsPayload() || pl.payload != nil {
		buckets, exact, err := s.aggregateInClickHouse(ctx, pl, spec, read.Types)
		if err != nil || exact {
			return buckets, err
		}
	}
	if s.inService != nil {
		s.inService()
	}
	err = s.joined(ctx, m, pl, false, aggregator.Add)
	if err != nil {
		return nil, err
	}
	return aggregator.Buckets(), nil
}

// aggregateInClickHouse runs the aggregation statement. exact is false when a
// row it grouped is one ClickHouse cannot read as Postgres does, and the
// buckets are then discarded.
func (s *Store) aggregateInClickHouse(ctx context.Context, pl plan, spec business.AuditAggregationSpec, types business.AuditEventTypeIndex) ([]business.AuditAggregateBucket, bool, error) {
	agg, ok, err := s.buildAggregation(pl, spec, types)
	if err != nil || !ok {
		return nil, err == nil, err
	}
	var out []business.AuditAggregateBucket
	var inexact uint64
	err = s.scan(ctx, agg.statement, agg.args, func(rows driver.Rows) (bool, error) {
		keys := make([]string, agg.dims)
		var count, rowInexact uint64
		values := make([]float64, len(agg.metrics))
		arrays := make([][]float64, len(agg.metrics))
		samples := make([]uint64, len(agg.metrics))
		dest := make([]any, 0, agg.dims+2+2*len(agg.metrics))
		for i := range keys {
			dest = append(dest, &keys[i])
		}
		dest = append(dest, &count)
		for i, metric := range agg.metrics {
			if metric.Op == "percentile" {
				dest = append(dest, &arrays[i])
			} else {
				dest = append(dest, &values[i])
			}
		}
		for i := range samples {
			dest = append(dest, &samples[i])
		}
		dest = append(dest, &rowInexact)
		if err := rows.Scan(dest...); err != nil {
			return false, fmt.Errorf("clickhouse audit store: read aggregation: %w", err)
		}
		inexact += rowInexact
		if inexact > 0 {
			return false, nil
		}
		bucket := business.AuditAggregateBucket{
			Keys:    keys,
			Key:     keys[0],
			Count:   int64(count),
			Samples: make(map[string]int64, len(agg.aliases)),
			Metrics: make(map[string]float64, len(agg.aliases)+len(spec.Derived)),
		}
		for i, metric := range agg.metrics {
			alias := agg.aliases[i]
			bucket.Samples[alias] = int64(samples[i])
			if samples[i] == 0 {
				continue
			}
			if metric.Op == "percentile" {
				bucket.Metrics[alias] = auditeval.PercentileCont(arrays[i], metric.Percentile)
			} else {
				bucket.Metrics[alias] = values[i]
			}
		}
		for _, derived := range spec.Derived {
			numerator, numeratorOK := bucket.Metrics[derived.Numerator]
			denominator, denominatorOK := bucket.Metrics[derived.Denominator]
			if numeratorOK && denominatorOK && denominator != 0 {
				bucket.Metrics[derived.Alias] = numerator / denominator
			}
		}
		out = append(out, bucket)
		return true, nil
	})
	if err != nil {
		return nil, false, err
	}
	if inexact > 0 {
		return nil, false, nil
	}
	return out, true, nil
}

// LatestSourceSyncEvents implements business.AuditSourceSyncReader: per
// source, its newest sync request in the scope's organization — by
// occurrence, and by event id between two at one instant — in one GROUP BY.
func (s *Store) LatestSourceSyncEvents(ctx context.Context, scope business.AuditReadScope, sources []string) (map[string]business.AuditSourceSyncEvent, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if scope.Platform() {
		return nil, errors.New("clickhouse audit store: the readable-source query reads one organization")
	}
	out := map[string]business.AuditSourceSyncEvent{}
	seen := map[string]bool{}
	var ids []string
	for _, source := range sources {
		if !seen[source] {
			seen[source] = true
			ids = append(ids, source)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	p := &sqlParams{}
	w := s.scope(p, scope)
	w.and("event_type = " + p.text(string(business.EventDatasourceSourceSynced)))
	w.and("resource = " + p.text("datasource"))
	w.and("has(" + p.texts(ids) + ", resource_id)")
	statement := "SELECT resource_id, max(occurred_at), argMax(actor_id, (occurred_at, event_id)) FROM " + EventsTable +
		" WHERE " + w.String() + " GROUP BY resource_id"
	err := s.scan(ctx, statement, p.args, func(rows driver.Rows) (bool, error) {
		var source, actor string
		var at time.Time
		if err := rows.Scan(&source, &at, &actor); err != nil {
			return false, fmt.Errorf("clickhouse audit store: read %s: %w", EventsTable, err)
		}
		if !seen[source] {
			return false, errUnscopedRow
		}
		out[source] = business.AuditSourceSyncEvent{RequestedAt: time.UnixMicro(at.UnixMicro()), ActorID: actor}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadStoredAuditEvents implements business.AuditHistoryReader: every copy of
// every event of this deployment in [from, to), each with the details its
// store holds. A content-class event's details come from the details table;
// when the copies there disagree, a copy whose hash does not match the
// event's is the one reported, so the disagreement is what verification sees.
func (s *Store) ReadStoredAuditEvents(ctx context.Context, from, to time.Time, visit func(business.StoredAuditEvent) error) error {
	p := &sqlParams{}
	window := func() string {
		return "deployment_id = " + p.text(s.deploymentID) + " AND occurred_at >= " + p.instant(from) + " AND occurred_at < " + p.instant(to)
	}
	events := window()
	details := window()
	statement := "SELECT e.deployment_id, e.event_id, e.org_id, e.actor_id, e.actor_type, e.event_type, e.schema_version, e.resource, " +
		"e.resource_id, e.occurred_at, e.ip_address, e.impersonated_by, e.is_impersonated, e.client_id, e.retention_class, " +
		"e.details_sha256, e.details, arrayMap(c -> c.1, d.copies), arrayMap(c -> c.2, d.copies) " +
		"FROM (SELECT " + eventColumns + " FROM " + EventsTable + " WHERE " + events + ") AS e " +
		"LEFT JOIN (SELECT event_id, groupArray((details_sha256, details)) AS copies FROM " + DetailsTable +
		" WHERE " + details + " GROUP BY event_id) AS d ON e.event_id = d.event_id"
	return s.scan(ctx, statement, p.args, func(rows driver.Rows) (bool, error) {
		var hashes, texts []string
		row, err := scanStored(rows, &hashes, &texts)
		if err != nil {
			return false, fmt.Errorf("clickhouse audit store: read %s: %w", EventsTable, err)
		}
		if row.deploymentID != s.deploymentID {
			return false, errUnscopedRow
		}
		event := business.StoredAuditEvent{
			DeploymentID:  row.deploymentID,
			Entry:         row.entry,
			Retention:     business.AuditRetentionClass(row.retentionClass),
			DetailsSHA256: row.detailsSHA256,
			Details:       row.details,
			HasDetails:    row.details != "",
		}
		if event.Retention != business.RetentionSecurity {
			event.Details, event.HasDetails = "", false
			for i, text := range texts {
				event.Details, event.HasDetails = text, true
				if hashes[i] != row.detailsSHA256 {
					break
				}
			}
		}
		return true, visit(event)
	})
}
