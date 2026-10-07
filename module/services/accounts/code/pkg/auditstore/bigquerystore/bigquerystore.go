// Package bigquerystore is the BigQuery adapter of the audit store swap
// (ADR 0009): both halves of business.AuditStore.
//
// The service's authority is deliberately minimal and never includes
// bigquery.jobs.create, so it runs no load, query or DML job. The write half
// holds bigquery.datasets.get, bigquery.tables.create, bigquery.tables.get and
// bigquery.tables.updateData: rows are appended with streaming inserts
// (tabledata.insertAll) and tables are created, never altered, at startup. The
// read half (reader.go) holds bigquery.tables.getData, bigquery.tables.list and
// the read-session permissions, and reads through the Storage Read API.
// Credentials are Application Default Credentials only: the caller builds the
// clients, and on Kubernetes that is the pod's workload identity.
package bigquerystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

// Table names. Both live in the configured dataset.
const (
	EventsTable  = "audit_events"
	DetailsTable = "audit_event_details"
)

// maxRowsPerInsert is BigQuery's recommended streaming request size; a relay
// batch larger than it is sent as several requests.
const maxRowsPerInsert = 500

// maxRequestBytes bounds the rows one request carries, as BigQuery encodes
// them. BigQuery refuses a request over 10 MB (and, with the same limit, a
// single row over it), and a row count says nothing of that: a few hundred
// content-class events with large details are many megabytes. The bound sits
// under the limit to leave room for the request's own envelope and for the
// difference between this estimate and the encoding.
const maxRequestBytes = 8 << 20

// rowEnvelopeBytes is what a row adds to a request beyond its values and its
// insert id: the keys and punctuation around them.
const rowEnvelopeBytes = 64

// ErrRowTooLarge is the error of a row that cannot be sent in any request.
var ErrRowTooLarge = errors.New("bigquery audit store: row exceeds the request size limit")

// RowTooLargeError names the event whose row cannot be sent: alone it is more
// than a request carries. Retrying cannot help, so AppendAuditBatch returns it
// inside a business.PermanentRowRejection, which is what the relay sets a row
// aside on.
type RowTooLargeError struct {
	// Table is the table the row was for.
	Table string
	// EventID is the event the row is of.
	EventID string
	// Size is the row's size, as BigQuery encodes it, and Limit is what one
	// request carries.
	Size, Limit int
}

func (e *RowTooLargeError) Error() string {
	return fmt.Sprintf("bigquery audit store: append to %s: the row of event %s is %d bytes, over the %d bytes one request carries",
		e.Table, e.EventID, e.Size, e.Limit)
}

// Unwrap makes errors.Is(err, ErrRowTooLarge) true.
func (e *RowTooLargeError) Unwrap() error { return ErrRowTooLarge }

// timestampLayout is BigQuery's canonical TIMESTAMP text at its microsecond
// precision, which is also Postgres's.
const timestampLayout = "2006-01-02T15:04:05.000000Z"

// Config names where the store writes.
type Config struct {
	// Dataset must already exist: the writer may read it, never create it.
	Dataset string
	// ContentDetailRetention is the details table's partition expiration: how
	// long a content-class event's full details are kept.
	ContentDetailRetention time.Duration
	// Reader is the read half. Without one the store is write-only, as the
	// relay needs it, and every read is refused.
	Reader *Reader
}

// rowInserter is the streaming-insert seam; *bigquery.Inserter satisfies it.
type rowInserter interface {
	Put(ctx context.Context, src any) error
}

// Store appends relay batches to the events and details tables.
type Store struct {
	client    *bigquery.Client
	dataset   string
	retention time.Duration
	events    rowInserter
	details   rowInserter
	reader    *Reader
}

// New builds a store over client, which the caller constructs with
// Application Default Credentials.
func New(client *bigquery.Client, cfg Config) (*Store, error) {
	if client == nil {
		return nil, errors.New("bigquery audit store: client is required")
	}
	if strings.TrimSpace(cfg.Dataset) == "" {
		return nil, errors.New("bigquery audit store: dataset is required")
	}
	if cfg.ContentDetailRetention < 24*time.Hour {
		return nil, errors.New("bigquery audit store: content detail retention must be at least one day")
	}
	dataset := client.Dataset(cfg.Dataset)
	events := dataset.Table(EventsTable).Inserter()
	details := dataset.Table(DetailsTable).Inserter()
	// A row that does not fit the schema fails its whole request rather than
	// being dropped or stripped: the batch is retried, never shortened.
	events.SkipInvalidRows, events.IgnoreUnknownValues = false, false
	details.SkipInvalidRows, details.IgnoreUnknownValues = false, false
	return &Store{
		client:    client,
		dataset:   cfg.Dataset,
		retention: cfg.ContentDetailRetention,
		events:    events,
		details:   details,
		reader:    cfg.Reader,
	}, nil
}

// errWriteOnly refuses a read of a store built without a reader.
var errWriteOnly = errors.New("bigquery audit store: built without a reader")

// ListAuditEvents implements business.AuditReader.
func (s *Store) ListAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, string, error) {
	if s.reader == nil {
		return nil, "", errWriteOnly
	}
	return s.reader.ListAuditEvents(ctx, read)
}

// AggregateAuditEvents implements business.AuditReader.
func (s *Store) AggregateAuditEvents(ctx context.Context, read business.AuditRead, spec business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	if s.reader == nil {
		return nil, errWriteOnly
	}
	return s.reader.AggregateAuditEvents(ctx, read, spec)
}

// ExportAuditEvents implements business.AuditReader.
func (s *Store) ExportAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, error) {
	if s.reader == nil {
		return nil, errWriteOnly
	}
	return s.reader.ExportAuditEvents(ctx, read)
}

// LatestSourceSyncEvents implements business.AuditSourceSyncReader.
func (s *Store) LatestSourceSyncEvents(ctx context.Context, scope business.AuditReadScope, sources []string) (map[string]business.AuditSourceSyncEvent, error) {
	if s.reader == nil {
		return nil, errWriteOnly
	}
	return s.reader.LatestSourceSyncEvents(ctx, scope, sources)
}

// ReadStoredAuditEvents implements business.AuditHistoryReader.
func (s *Store) ReadStoredAuditEvents(ctx context.Context, from, to time.Time, visit func(business.StoredAuditEvent) error) error {
	if s.reader == nil {
		return errWriteOnly
	}
	return s.reader.ReadStoredAuditEvents(ctx, from, to, visit)
}

var (
	_ business.AuditStore         = (*Store)(nil)
	_ business.AuditHistoryReader = (*Store)(nil)
)

// EventsSchema is the events table: every event's envelope, the SHA-256 of its
// canonical details, and the details themselves for security-class events.
func EventsSchema() bigquery.Schema {
	return bigquery.Schema{
		{Name: "event_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "deployment_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "org_id", Type: bigquery.StringFieldType, Description: "Empty for a platform event."},
		{Name: "actor_id", Type: bigquery.StringFieldType},
		{Name: "actor_type", Type: bigquery.StringFieldType, Required: true},
		{Name: "event_type", Type: bigquery.StringFieldType, Required: true},
		{Name: "schema_version", Type: bigquery.IntegerFieldType, Required: true},
		{Name: "resource", Type: bigquery.StringFieldType, Required: true, Description: "The target's resource type."},
		{Name: "resource_id", Type: bigquery.StringFieldType, Description: "The target's id."},
		{Name: "occurred_at", Type: bigquery.TimestampFieldType, Required: true},
		{Name: "ip_address", Type: bigquery.StringFieldType},
		{Name: "impersonated_by", Type: bigquery.StringFieldType},
		{Name: "is_impersonated", Type: bigquery.BooleanFieldType, Required: true},
		{Name: "client_id", Type: bigquery.StringFieldType},
		{Name: "retention_class", Type: bigquery.StringFieldType, Required: true},
		{Name: "details_sha256", Type: bigquery.StringFieldType, Required: true, Description: "Lowercase hex SHA-256 of the canonical details string."},
		{Name: "details", Type: bigquery.StringFieldType, Description: "Canonical details JSON; security-class events only."},
	}
}

// DetailsSchema is the details table: the full details of content-class
// events, kept for the content window.
func DetailsSchema() bigquery.Schema {
	return bigquery.Schema{
		{Name: "event_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "deployment_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "org_id", Type: bigquery.StringFieldType},
		{Name: "event_type", Type: bigquery.StringFieldType, Required: true},
		{Name: "occurred_at", Type: bigquery.TimestampFieldType, Required: true},
		{Name: "details_sha256", Type: bigquery.StringFieldType, Required: true},
		{Name: "details", Type: bigquery.StringFieldType, Required: true},
	}
}

// tableSpec is what Ensure creates and what it requires of a table it finds.
type tableSpec struct {
	name        string
	description string
	schema      bigquery.Schema
	expiration  time.Duration
}

func (s *Store) tableSpecs() []tableSpec {
	return []tableSpec{
		{
			name:        EventsTable,
			description: "Audit store of record: one row per audit event (ADR 0009).",
			schema:      EventsSchema(),
		},
		{
			name:        DetailsTable,
			description: "Full details of content-class audit events, kept for the content window (ADR 0009).",
			schema:      DetailsSchema(),
			expiration:  s.retention,
		},
	}
}

// metadataFor is the table every spec is created with: partitioned by day on
// occurred_at, clustered by organization, and — for the details table —
// expiring each partition at the content window.
func metadataFor(spec tableSpec) *bigquery.TableMetadata {
	return &bigquery.TableMetadata{
		Name:        spec.name,
		Description: spec.description,
		Schema:      spec.schema,
		TimePartitioning: &bigquery.TimePartitioning{
			Type:       bigquery.DayPartitioningType,
			Field:      "occurred_at",
			Expiration: spec.expiration,
		},
		Clustering: &bigquery.Clustering{Fields: []string{"org_id"}},
	}
}

// Ensure verifies the dataset exists, creates each table that is missing, and
// refuses a table it finds that does not match: the writer holds no
// bigquery.tables.update, so a mismatch is the deployment's to fix, and a
// details table expiring at a window other than the configured one would keep
// content longer or shorter than the deployment declared. Whole-table expiry
// is refused too, including expiry inherited from the dataset when a table is
// created: it deletes every partition regardless of the configured window.
func (s *Store) Ensure(ctx context.Context) error {
	dataset := s.client.Dataset(s.dataset)
	if _, err := dataset.Metadata(ctx); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("bigquery audit store: dataset %q does not exist; the deployment creates it", s.dataset)
		}
		return fmt.Errorf("bigquery audit store: read dataset %q: %w", s.dataset, err)
	}
	for _, spec := range s.tableSpecs() {
		table := dataset.Table(spec.name)
		existing, err := table.Metadata(ctx)
		if isNotFound(err) {
			if err := table.Create(ctx, metadataFor(spec)); err != nil && !isAlreadyExists(err) {
				return fmt.Errorf("bigquery audit store: create table %s: %w", spec.name, err)
			}
			// Another replica may have created it first; read back what exists.
			if existing, err = table.Metadata(ctx); err != nil {
				return fmt.Errorf("bigquery audit store: read table %s: %w", spec.name, err)
			}
		} else if err != nil {
			return fmt.Errorf("bigquery audit store: read table %s: %w", spec.name, err)
		}
		if err := conforms(spec, existing); err != nil {
			return fmt.Errorf("bigquery audit store: table %s.%s: %w", s.dataset, spec.name, err)
		}
	}
	return nil
}

// conforms reports how an existing table differs from what the store writes.
func conforms(spec tableSpec, existing *bigquery.TableMetadata) error {
	if !existing.ExpirationTime.IsZero() {
		return fmt.Errorf("table expires at %s; whole-table expiration must be disabled", existing.ExpirationTime.UTC().Format(time.RFC3339))
	}
	partitioning := existing.TimePartitioning
	if partitioning == nil || partitioning.Field != "occurred_at" {
		return errors.New("is not partitioned on occurred_at")
	}
	if partitioning.Type != bigquery.DayPartitioningType {
		return fmt.Errorf("partitions use %q granularity, want DAY on occurred_at", partitioning.Type)
	}
	if partitioning.Expiration != spec.expiration {
		return fmt.Errorf("partitions expire after %s, the configured window is %s", partitioning.Expiration, spec.expiration)
	}
	columns := make(map[string]*bigquery.FieldSchema, len(existing.Schema))
	for _, field := range existing.Schema {
		columns[field.Name] = field
	}
	for _, want := range spec.schema {
		got, ok := columns[want.Name]
		if !ok {
			return fmt.Errorf("has no column %s", want.Name)
		}
		if got.Type != want.Type || got.Repeated {
			return fmt.Errorf("column %s is %s, want %s", want.Name, got.Type, want.Type)
		}
		if got.Required && !want.Required {
			return fmt.Errorf("column %s is REQUIRED but the writer can supply NULL", want.Name)
		}
		delete(columns, want.Name)
	}
	for _, field := range existing.Schema {
		if _, extra := columns[field.Name]; extra && field.Required && strings.TrimSpace(field.DefaultValueExpression) == "" {
			return fmt.Errorf("extra REQUIRED column %s has no default and is not supplied by the writer", field.Name)
		}
	}
	return nil
}

// BatchRows is the rows a batch appends: one events-table row per record, and
// a details-table row for each content-class record, whose details go there
// instead of its events row.
func BatchRows(batch business.AuditBatch) (events, details []Row) {
	for _, record := range batch.Records {
		events = append(events, EventRow(batch.DeploymentID, record))
		if record.Retention != business.RetentionSecurity {
			details = append(details, DetailRow(batch.DeploymentID, record))
		}
	}
	return events, details
}

// AppendAuditBatch implements business.AuditStoreWriter: the batch's rows
// (BatchRows) streamed to the details and events tables, each table in as few
// requests as hold at most maxRowsPerInsert rows and maxRequestBytes bytes.
//
// The details go first and the events only once every details row is accepted,
// because a content-class event keeps its details nowhere but the details table:
// an events row written beside a details row BigQuery then refuses would be an
// event with an empty payload, in a table every read starts from, for an event
// the relay is about to set aside. A details row whose events row then fails is
// harmless instead — no read starts from the details table — and the relay
// delivers the batch again. So when the details are not all accepted, no events
// row is written in this call: the events of the rows that were accepted follow
// when the relay sends the batch again without the rows refused.
//
// What a failure says about the batch is what the relay acts on, so the error
// is classified here, where BigQuery's answer is known:
//
//   - A row that cannot be sent in any request (a *RowTooLargeError), or whose
//     values cannot be encoded, is returned as a business.PermanentRowRejection
//     naming its event. Every request is planned before the first is sent, so
//     such a row fails the batch before anything of it is written.
//   - A row BigQuery reports back with the reason "invalid" (insertAll lists
//     each row it refused, with the reason) is returned the same way: the value
//     is wrong for its column, and sending it again gives the same answer. The
//     batch's other rows, which BigQuery reports as "stopped", are not refused;
//     the relay sends them again without the refused rows.
//   - A request every row of which BigQuery refuses as invalid for one and the
//     same reason names no row: a table changed under the writer refuses every
//     row alike, and the rows are as deliverable as they were. It is returned as
//     a failure of the request, which the relay retries, so that the whole stream
//     is not set aside for a cause that is not in it (business.RefusedForOneReason).
//   - Every other failure is returned as it came — a request that failed (any
//     HTTP error, a throttle, a quota, a timeout, a dead backend), a row
//     reported with any other reason, an error of a kind not known here — and
//     the relay retries the batch.
func (s *Store) AppendAuditBatch(ctx context.Context, batch business.AuditBatch) error {
	eventRows, detailRows := BatchRows(batch)
	events, eventsRefused := planRequests(EventsTable, eventRows)
	details, detailsRefused := planRequests(DetailsTable, detailRows)
	if refused := append(eventsRefused, detailsRefused...); len(refused) > 0 {
		return errors.Join(refused...)
	}
	if err := put(ctx, s.details, details); err != nil {
		return fmt.Errorf("bigquery audit store: append to %s: %w", DetailsTable, err)
	}
	if err := put(ctx, s.events, events); err != nil {
		return fmt.Errorf("bigquery audit store: append to %s: %w", EventsTable, err)
	}
	return nil
}

// rowBytes is the size a row adds to a request: its values as BigQuery encodes
// them, its insert id, and the envelope around them.
func rowBytes(row Row) (int, error) {
	encoded, err := json.Marshal(row.Values)
	if err != nil {
		return 0, fmt.Errorf("encode the row of event %s: %w", row.InsertID, err)
	}
	return len(encoded) + len(row.InsertID) + rowEnvelopeBytes, nil
}

// planRequests splits rows, in order, into requests of at most maxRowsPerInsert
// rows and maxRequestBytes bytes each. A row that does not fit a request alone
// (a *RowTooLargeError) and a row whose values cannot be encoded are returned
// as refusals of that row, and left out of every request.
func planRequests(table string, rows []Row) (requests [][]bigquery.ValueSaver, refused []error) {
	var current []bigquery.ValueSaver
	currentBytes := 0
	for _, row := range rows {
		size, encodeErr := rowBytes(row)
		if encodeErr != nil {
			refused = append(refused, &business.PermanentRowRejection{
				EventID: row.InsertID,
				Cause:   fmt.Errorf("bigquery audit store: append to %s: %w", table, encodeErr),
			})
			continue
		}
		if size > maxRequestBytes {
			refused = append(refused, &business.PermanentRowRejection{
				EventID: row.InsertID,
				Cause:   &RowTooLargeError{Table: table, EventID: row.InsertID, Size: size, Limit: maxRequestBytes},
			})
			continue
		}
		if len(current) == maxRowsPerInsert || currentBytes+size > maxRequestBytes {
			requests = append(requests, current)
			current, currentBytes = nil, 0
		}
		current = append(current, row)
		currentBytes += size
	}
	if len(current) > 0 {
		requests = append(requests, current)
	}
	return requests, refused
}

func put(ctx context.Context, inserter rowInserter, requests [][]bigquery.ValueSaver) error {
	for _, request := range requests {
		if err := inserter.Put(ctx, request); err != nil {
			return classifyPutError(err, request)
		}
	}
	return nil
}

// reasonInvalid is the reason BigQuery gives a streamed row whose own content
// it refuses: a value that does not fit its column. The other reasons a row
// carries — "stopped" for a row that was fine but sent beside an invalid one,
// "timeout", "backendError", "internalError", "rateLimitExceeded",
// "quotaExceeded" — say nothing against the row.
const reasonInvalid = "invalid"

// classifyPutError turns what Put returned for request into what the relay
// needs. A PutMultiError lists every row BigQuery refused with the reasons it
// gave; each row it gave the reason "invalid" is a *business.PermanentRowRejection,
// and the error also carries the original, so the rows that were merely stopped,
// and any that failed for another reason, are described and retried. Every other
// error is the request's, not a row's, and is returned as it came.
//
// When every row of a request of two or more is refused as invalid for one and
// the same reason, that is the table's, not the rows': none of them is named, so
// the relay retries the batch instead of setting every row aside.
func classifyPutError(err error, request []bigquery.ValueSaver) error {
	var multi bigquery.PutMultiError
	if !errors.As(err, &multi) || len(multi) == 0 {
		return err
	}
	failures := []error{describePutError(err)}
	var refused []error
	for _, row := range multi {
		if row.InsertID == "" || !rowIsInvalid(row) {
			continue
		}
		refused = append(refused, &business.PermanentRowRejection{
			EventID: row.InsertID,
			Cause:   fmt.Errorf("bigquery audit store: the row was refused as invalid: %v", row.Errors),
			// What BigQuery said of the row, which for a table that changed is the
			// same for every row: the row's place in the request is not part of it.
			Reason: fmt.Sprint(row.Errors),
		})
	}
	if reason, whole := business.RefusedForOneReason(errors.Join(refused...), insertIDs(request)); whole {
		return fmt.Errorf("bigquery audit store: every one of the %d rows of a request was refused as invalid for one reason, which is the table's and not the rows': %s: %w", len(request), reason, failures[0])
	}
	return errors.Join(append(refused, failures...)...)
}

// insertIDs is the insert id of each row of a request, which is its event id.
func insertIDs(request []bigquery.ValueSaver) []string {
	ids := make([]string, 0, len(request))
	for _, saver := range request {
		if row, ok := saver.(Row); ok {
			ids = append(ids, row.InsertID)
		}
	}
	return ids
}

// rowIsInvalid reports whether BigQuery gave the row the reason "invalid".
func rowIsInvalid(row bigquery.RowInsertionError) bool {
	for _, rowErr := range row.Errors {
		var byPointer *bigquery.Error
		var byValue bigquery.Error
		switch {
		case errors.As(rowErr, &byPointer):
			if byPointer.Reason == reasonInvalid {
				return true
			}
		case errors.As(rowErr, &byValue):
			if byValue.Reason == reasonInvalid {
				return true
			}
		}
	}
	return false
}

// describePutError names the first rejected row: a PutMultiError's own message
// says only how many rows failed.
func describePutError(err error) error {
	var multi bigquery.PutMultiError
	if errors.As(err, &multi) && len(multi) > 0 {
		first := multi[0]
		return fmt.Errorf("%w (row %d, insert id %s: %v)", err, first.RowIndex, first.InsertID, first.Errors)
	}
	return err
}

// Row is one streaming-insert row keyed by its insert id.
type Row struct {
	InsertID string
	Values   map[string]bigquery.Value
}

// Save implements bigquery.ValueSaver.
func (r Row) Save() (map[string]bigquery.Value, string, error) {
	return r.Values, r.InsertID, nil
}

// EventRow is a record's events-table row.
//
// Its insert id is the event id. BigQuery collapses a repeated insert id only
// within a short window, so a batch redelivered after a restart can write an
// event twice; that is accepted (ADR 0009), not prevented here: every read of
// these tables returns each event once by event id, as archive readers do.
// Committed write streams with offsets would prevent it, at the cost of
// stream state the relay would have to carry — machinery for no compliance gain.
func EventRow(deploymentID string, record business.AuditRecord) Row {
	entry := record.Entry
	values := map[string]bigquery.Value{
		"event_id":        entry.ID,
		"deployment_id":   deploymentID,
		"org_id":          nullable(entry.OrgID),
		"actor_id":        nullable(entry.ActorID),
		"actor_type":      entry.ActorType,
		"event_type":      string(entry.EventType),
		"schema_version":  entry.SchemaVersion,
		"resource":        entry.Resource,
		"resource_id":     nullable(entry.ResourceID),
		"occurred_at":     entry.CreatedAt.UTC().Format(timestampLayout),
		"ip_address":      nullable(entry.IPAddress),
		"impersonated_by": nullable(entry.ImpersonatedBy),
		"is_impersonated": entry.IsImpersonated,
		"client_id":       nullable(entry.ClientID),
		"retention_class": string(record.Retention),
		"details_sha256":  record.DetailsSHA256,
		"details":         nil,
	}
	if record.Retention == business.RetentionSecurity {
		values["details"] = record.Details
	}
	return Row{InsertID: entry.ID, Values: values}
}

// DetailRow is a content-class record's details-table row, keyed and
// deduplicated on read exactly as EventRow is.
func DetailRow(deploymentID string, record business.AuditRecord) Row {
	entry := record.Entry
	return Row{InsertID: entry.ID, Values: map[string]bigquery.Value{
		"event_id":       entry.ID,
		"deployment_id":  deploymentID,
		"org_id":         nullable(entry.OrgID),
		"event_type":     string(entry.EventType),
		"occurred_at":    entry.CreatedAt.UTC().Format(timestampLayout),
		"details_sha256": record.DetailsSHA256,
		"details":        record.Details,
	}}
}

func nullable(value string) bigquery.Value {
	if value == "" {
		return nil
	}
	return value
}

func isNotFound(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}

func isAlreadyExists(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict
}
