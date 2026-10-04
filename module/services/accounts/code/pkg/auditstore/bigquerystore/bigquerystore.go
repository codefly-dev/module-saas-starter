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
	"errors"
	"fmt"
	"net/http"
	"slices"
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
// content longer or shorter than the deployment declared.
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
	partitioning := existing.TimePartitioning
	if partitioning == nil || partitioning.Field != "occurred_at" {
		return errors.New("is not partitioned on occurred_at")
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
// (BatchRows) streamed to the events and details tables.
func (s *Store) AppendAuditBatch(ctx context.Context, batch business.AuditBatch) error {
	eventRows, detailRows := BatchRows(batch)
	events := make([]bigquery.ValueSaver, len(eventRows))
	for i, row := range eventRows {
		events[i] = row
	}
	details := make([]bigquery.ValueSaver, len(detailRows))
	for i, row := range detailRows {
		details[i] = row
	}
	if err := putInChunks(ctx, s.events, events); err != nil {
		return fmt.Errorf("bigquery audit store: append to %s: %w", EventsTable, err)
	}
	if err := putInChunks(ctx, s.details, details); err != nil {
		return fmt.Errorf("bigquery audit store: append to %s: %w", DetailsTable, err)
	}
	return nil
}

func putInChunks(ctx context.Context, inserter rowInserter, rows []bigquery.ValueSaver) error {
	for chunk := range slices.Chunk(rows, maxRowsPerInsert) {
		if err := inserter.Put(ctx, chunk); err != nil {
			return describePutError(err)
		}
	}
	return nil
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
