// Package clickhousestore is the ClickHouse adapter of the audit store swap
// (ADR 0009): both halves of business.AuditStore, and the history read-back
// (business.AuditHistoryReader).
//
// It keeps the same two tables as the BigQuery adapter, with the same rows: an
// events table holding every event's envelope, the SHA-256 of its canonical
// details, and the details themselves for security-class events; and a details
// table holding the details of content-class events. Every row carries the
// deployment id; a platform event has no organization. Where BigQuery keeps a
// NULL, ClickHouse keeps an empty string — no column here holds a value whose
// empty form means something else.
//
// The tables live in the database the DSN names, which the deployment creates;
// the store creates the tables when they are missing (MergeTree, or
// ReplicatedMergeTree on a cluster), partitioned by month, and expires each
// tier with a table TTL at its window. The service's ClickHouse user needs
// CREATE TABLE, INSERT and SELECT on that database and nothing more: no ALTER,
// DELETE, UPDATE, TRUNCATE or DROP, so it cannot rewrite the store of record.
// Its credentials ride in the DSN, which comes from the deployment's secret and
// is never logged.
//
// Inserts are batched: one insert per table per relay batch, never a row at a
// time (a single-row insert creates a data part per insert and ends in
// too-many-parts errors). Reads run in ClickHouse wherever ClickHouse computes
// exactly what the Postgres reads compute, and in the service (auditeval)
// where it does not; reader.go says which.
package clickhousestore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Table names. Both live in the DSN's database.
const (
	EventsTable  = "audit_events"
	DetailsTable = "audit_event_details"
)

// Conn is what the store uses of a clickhouse-go native connection;
// clickhouse.Open's driver.Conn satisfies it.
type Conn interface {
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
	Exec(ctx context.Context, query string, args ...any) error
	PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error)
}

// Config names where the store writes and what it reads.
type Config struct {
	// Database is the database the connection uses, as its DSN names it. It
	// must already exist: the store creates tables in it, never the database.
	Database string
	// Cluster, when set, creates both tables ON CLUSTER as ReplicatedMergeTree,
	// coordinated by ClickHouse Keeper under the servers' default replica path
	// and name. Empty creates them as MergeTree on the one server.
	Cluster string
	// DeploymentID confines every read to this deployment's events.
	DeploymentID string
	// EventsRetention is the events table's TTL: the compliance window, in
	// whole days.
	EventsRetention time.Duration
	// ContentDetailRetention is the details table's TTL: how long a
	// content-class event's full details are kept, in whole days.
	ContentDetailRetention time.Duration
}

// clusterPattern is a cluster name as remote_servers declares one, or a macro
// that names it ({cluster}). It is the one name a statement carries as text,
// since ON CLUSTER takes no parameter, so it is held to these characters.
var clusterPattern = regexp.MustCompile(`^(?:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|\{[A-Za-z0-9_]{1,64}\})$`)

// ValidCluster reports whether name can be a configured cluster.
func ValidCluster(name string) bool { return clusterPattern.MatchString(name) }

// Store appends relay batches to the events and details tables and reads them.
type Store struct {
	conn         Conn
	database     string
	cluster      string
	deploymentID string
	eventsDays   int
	detailDays   int
	readSettings clickhouse.Settings
	// inService, when set, is told each time an aggregation is evaluated in the
	// service rather than in ClickHouse; tests use it to prove which ran.
	inService func()
}

// New builds a store over conn.
func New(conn Conn, cfg Config) (*Store, error) {
	if conn == nil {
		return nil, errors.New("clickhouse audit store: connection is required")
	}
	if strings.TrimSpace(cfg.Database) == "" {
		return nil, errors.New("clickhouse audit store: database is required")
	}
	if strings.TrimSpace(cfg.DeploymentID) == "" {
		return nil, errors.New("clickhouse audit store: deployment id is required")
	}
	if cfg.Cluster != "" && !ValidCluster(cfg.Cluster) {
		return nil, fmt.Errorf("clickhouse audit store: cluster %q is not a valid cluster name", cfg.Cluster)
	}
	eventsDays, err := wholeDays("events retention", cfg.EventsRetention)
	if err != nil {
		return nil, err
	}
	detailDays, err := wholeDays("content detail retention", cfg.ContentDetailRetention)
	if err != nil {
		return nil, err
	}
	return &Store{
		conn:         conn,
		database:     cfg.Database,
		cluster:      cfg.Cluster,
		deploymentID: cfg.DeploymentID,
		eventsDays:   eventsDays,
		detailDays:   detailDays,
		// Pinned per read, whatever the user's profile says, because each one
		// changes an answer.
		readSettings: clickhouse.Settings{
			// The numeric reading of a payload string parses to the double
			// float8in returns only with ClickHouse's precise parser.
			"precise_float_parsing": 1,
			// The JSON parser that rounds a JSON number as float8in does; the
			// other one does not.
			"allow_simdjson": 1,
			// An unmatched details row reads as '' — no details — not NULL.
			"join_use_nulls": 0,
		},
	}, nil
}

func wholeDays(name string, window time.Duration) (int, error) {
	const day = 24 * time.Hour
	if window < day || window%day != 0 {
		return 0, fmt.Errorf("clickhouse audit store: %s must be a whole number of days, at least one; got %s", name, window)
	}
	return int(window / day), nil
}

var (
	_ business.AuditStore         = (*Store)(nil)
	_ business.AuditHistoryReader = (*Store)(nil)
)

// Column is one column of a table the store creates.
type Column struct {
	Name    string
	Type    string
	Comment string
}

// EventsColumns is the events table: every event's envelope, the SHA-256 of
// its canonical details, and the details themselves for security-class events.
func EventsColumns() []Column {
	return []Column{
		{Name: "event_id", Type: "String"},
		{Name: "deployment_id", Type: "LowCardinality(String)"},
		{Name: "org_id", Type: "String", Comment: "Empty for a platform event."},
		{Name: "actor_id", Type: "String", Comment: "Empty when the event names no actor."},
		{Name: "actor_type", Type: "LowCardinality(String)"},
		{Name: "event_type", Type: "LowCardinality(String)"},
		{Name: "schema_version", Type: "Int64"},
		{Name: "resource", Type: "LowCardinality(String)", Comment: "The target's resource type."},
		{Name: "resource_id", Type: "String", Comment: "The target's id; empty when none."},
		{Name: "occurred_at", Type: "DateTime64(6, 'UTC')"},
		{Name: "ip_address", Type: "String"},
		{Name: "impersonated_by", Type: "String"},
		{Name: "is_impersonated", Type: "Bool"},
		{Name: "client_id", Type: "String"},
		{Name: "retention_class", Type: "LowCardinality(String)"},
		{Name: "details_sha256", Type: "String", Comment: "Lowercase hex SHA-256 of the canonical details string."},
		{Name: "details", Type: "String", Comment: "Canonical details JSON of a security-class event; empty for a content-class one."},
	}
}

// DetailsColumns is the details table: the full details of content-class
// events, kept for the content window.
func DetailsColumns() []Column {
	return []Column{
		{Name: "event_id", Type: "String"},
		{Name: "deployment_id", Type: "LowCardinality(String)"},
		{Name: "org_id", Type: "String", Comment: "Empty for a platform event."},
		{Name: "event_type", Type: "LowCardinality(String)"},
		{Name: "occurred_at", Type: "DateTime64(6, 'UTC')"},
		{Name: "details_sha256", Type: "String"},
		{Name: "details", Type: "String", Comment: "Canonical details JSON."},
	}
}

// tableSpec is what Ensure creates and what it requires of a table it finds.
type tableSpec struct {
	name    string
	comment string
	columns []Column
	days    int
}

func (s *Store) tableSpecs() []tableSpec {
	return []tableSpec{
		{
			name:    EventsTable,
			comment: "Audit store of record: one row per audit event (ADR 0009).",
			columns: EventsColumns(),
			days:    s.eventsDays,
		},
		{
			name:    DetailsTable,
			comment: "Full details of content-class audit events, kept for the content window (ADR 0009).",
			columns: DetailsColumns(),
			days:    s.detailDays,
		},
	}
}

// CreateStatements are the statements Ensure runs for a missing table, in
// table order.
func (s *Store) CreateStatements() []string {
	specs := s.tableSpecs()
	out := make([]string, len(specs))
	for i, spec := range specs {
		out[i] = s.createStatement(spec)
	}
	return out
}

// createStatement is one table's CREATE TABLE: partitioned by month, sorted
// by deployment, organization and time — every read names the deployment and,
// but for the platform read, the organization, and the activity list walks
// time newest first — and expiring rows at the table's window. Every word of
// it is the store's own: column comments and the window are constants and
// configuration the store validated, and the cluster matched clusterPattern.
func (s *Store) createStatement(spec tableSpec) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS " + spec.name)
	engine := "MergeTree"
	if s.cluster != "" {
		b.WriteString(" ON CLUSTER '" + s.cluster + "'")
		engine = "ReplicatedMergeTree"
	}
	b.WriteString("\n(\n")
	for i, column := range spec.columns {
		b.WriteString("    " + column.Name + " " + column.Type)
		if column.Comment != "" {
			b.WriteString(" COMMENT " + quoteLiteral(column.Comment))
		}
		if i < len(spec.columns)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(")\nENGINE = " + engine + "\n")
	b.WriteString("PARTITION BY toYYYYMM(occurred_at)\n")
	b.WriteString("ORDER BY (deployment_id, org_id, occurred_at, event_id)\n")
	b.WriteString("TTL occurred_at + INTERVAL " + strconv.Itoa(spec.days) + " DAY\n")
	b.WriteString("COMMENT " + quoteLiteral(spec.comment))
	return b.String()
}

// quoteLiteral quotes one of the store's own constant strings.
func quoteLiteral(text string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(text) + "'"
}

// unknownDatabase is ClickHouse's UNKNOWN_DATABASE error code.
const unknownDatabase = 81

// Ensure verifies the database exists, creates each table that is missing,
// and refuses a table it finds that does not match: the service's user holds
// no ALTER, so a mismatch is the deployment's to fix, and a table expiring at
// a window other than the configured one would keep events or content longer
// or shorter than the deployment declared.
func (s *Store) Ensure(ctx context.Context) error {
	var current string
	var found uint64
	err := s.queryRow(ctx, "SELECT currentDatabase(), count() FROM system.databases WHERE name = currentDatabase()", nil, &current, &found)
	if err != nil {
		return s.ensureError("read the current database", err)
	}
	if found == 0 {
		return s.ensureError("", nil)
	}
	if current != s.database {
		return fmt.Errorf("clickhouse audit store: the connection uses database %q, the configured one is %q", current, s.database)
	}
	for _, spec := range s.tableSpecs() {
		existing, err := s.describe(ctx, spec.name)
		if err != nil {
			return s.ensureError("read table "+spec.name, err)
		}
		if existing == nil {
			if err := s.conn.Exec(ctx, s.createStatement(spec)); err != nil {
				return s.ensureError("create table "+spec.name, err)
			}
			// Another replica may have created it first; read back what exists.
			if existing, err = s.describe(ctx, spec.name); err != nil {
				return s.ensureError("read table "+spec.name, err)
			}
			if existing == nil {
				return fmt.Errorf("clickhouse audit store: table %s.%s was not created", s.database, spec.name)
			}
		}
		if err := conforms(spec, existing); err != nil {
			return fmt.Errorf("clickhouse audit store: table %s.%s: %w", s.database, spec.name, err)
		}
	}
	return nil
}

// ensureError describes a failure of Ensure: the database missing — whether
// the connection could not open it or found it gone — or what failed.
func (s *Store) ensureError(doing string, err error) error {
	var exception *clickhouse.Exception
	if err == nil || (errors.As(err, &exception) && exception.Code == unknownDatabase) {
		return fmt.Errorf("clickhouse audit store: database %q does not exist; the deployment creates it", s.database)
	}
	return fmt.Errorf("clickhouse audit store: %s: %w", doing, err)
}

// tableShape is what system.tables and system.columns say of a table.
type tableShape struct {
	engineFull string
	columns    map[string]string
}

func (s *Store) describe(ctx context.Context, table string) (*tableShape, error) {
	p := &sqlParams{}
	rows, err := s.conn.Query(ctx, "SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = "+p.text(table), p.args...)
	if err != nil {
		return nil, err
	}
	var shape *tableShape
	for rows.Next() {
		shape = &tableShape{columns: map[string]string{}}
		if err := rows.Scan(&shape.engineFull); err != nil {
			_ = rows.Close()
			return nil, err
		}
	}
	if err := closeRows(rows); err != nil || shape == nil {
		return nil, err
	}
	p = &sqlParams{}
	rows, err = s.conn.Query(ctx, "SELECT name, type FROM system.columns WHERE database = currentDatabase() AND table = "+p.text(table), p.args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			_ = rows.Close()
			return nil, err
		}
		shape.columns[name] = typ
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return shape, nil
}

// ttlClause is the TTL clause of an engine_full, up to its SETTINGS.
var ttlClause = regexp.MustCompile(` TTL (.+?)(?: SETTINGS |$)`)

// ttlDays is the one TTL the store writes, as ClickHouse prints it back
// (occurred_at + toIntervalDay(N)) or as the statement spelled it.
var ttlDays = regexp.MustCompile(`^occurred_at \+ (?:toIntervalDay\((\d+)\)|INTERVAL (\d+) DAY)$`)

// conforms reports how an existing table differs from what the store writes
// and reads: a column missing or of another type, or rows that expire at
// another window or by another rule.
func conforms(spec tableSpec, existing *tableShape) error {
	for _, want := range spec.columns {
		got, ok := existing.columns[want.Name]
		if !ok {
			return fmt.Errorf("has no column %s", want.Name)
		}
		if got != want.Type {
			return fmt.Errorf("column %s is %s, want %s", want.Name, got, want.Type)
		}
	}
	want := fmt.Sprintf("occurred_at + INTERVAL %d DAY", spec.days)
	clause := ttlClause.FindStringSubmatch(existing.engineFull)
	if clause == nil {
		return fmt.Errorf("has no TTL; the configured window is %s", want)
	}
	days := ttlDays.FindStringSubmatch(strings.TrimSpace(clause[1]))
	if days == nil {
		return fmt.Errorf("expires rows by %q, the configured window is %s", clause[1], want)
	}
	got := days[1] + days[2]
	if got != strconv.Itoa(spec.days) {
		return fmt.Errorf("rows expire after %s days, the configured window is %d days", got, spec.days)
	}
	return nil
}

// insertStatement is the INSERT a batch of rows of a table is sent with.
func insertStatement(table string, columns []Column) string {
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = column.Name
	}
	return "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ")"
}

// BatchRows is the rows a batch appends, in each table's column order: one
// events-table row per record, and a details-table row for each content-class
// record, whose details go there instead of its events row — the rows
// bigquerystore.BatchRows writes, with an empty string where BigQuery keeps a
// NULL.
func BatchRows(batch business.AuditBatch) (events, details [][]any) {
	for _, record := range batch.Records {
		events = append(events, EventRow(batch.DeploymentID, record))
		if record.Retention != business.RetentionSecurity {
			details = append(details, DetailRow(batch.DeploymentID, record))
		}
	}
	return events, details
}

// EventRow is a record's events-table row, in EventsColumns order.
func EventRow(deploymentID string, record business.AuditRecord) []any {
	entry := record.Entry
	details := ""
	if record.Retention == business.RetentionSecurity {
		details = record.Details
	}
	return []any{
		entry.ID, deploymentID, entry.OrgID, entry.ActorID, entry.ActorType, string(entry.EventType),
		int64(entry.SchemaVersion), entry.Resource, entry.ResourceID, entry.CreatedAt.UTC(), entry.IPAddress,
		entry.ImpersonatedBy, entry.IsImpersonated, entry.ClientID, string(record.Retention), record.DetailsSHA256, details,
	}
}

// DetailRow is a content-class record's details-table row, in DetailsColumns
// order.
func DetailRow(deploymentID string, record business.AuditRecord) []any {
	entry := record.Entry
	return []any{entry.ID, deploymentID, entry.OrgID, string(entry.EventType), entry.CreatedAt.UTC(), record.DetailsSHA256, record.Details}
}

// AppendAuditBatch implements business.AuditStoreWriter: the batch's rows
// (BatchRows) as one insert into the events table and, when the batch holds
// content-class events, one into the details table. Each insert is one block,
// written atomically; a failure between the two leaves the events without
// their details until the relay delivers the batch again, and every read
// returns each event once by event id.
func (s *Store) AppendAuditBatch(ctx context.Context, batch business.AuditBatch) error {
	events, details := BatchRows(batch)
	if err := s.insert(ctx, EventsTable, EventsColumns(), events); err != nil {
		return err
	}
	return s.insert(ctx, DetailsTable, DetailsColumns(), details)
}

func (s *Store) insert(ctx context.Context, table string, columns []Column, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, insertStatement(table, columns))
	if err != nil {
		return fmt.Errorf("clickhouse audit store: append to %s: %w", table, err)
	}
	defer func() {
		if !batch.IsSent() {
			_ = batch.Abort()
		}
	}()
	for _, row := range rows {
		if err := batch.Append(row...); err != nil {
			return fmt.Errorf("clickhouse audit store: append to %s: event %v: %w", table, row[0], err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("clickhouse audit store: append to %s: %w", table, err)
	}
	return nil
}

// queryRow runs a statement expected to return at most one row and scans it.
func (s *Store) queryRow(ctx context.Context, statement string, args []any, dest ...any) error {
	rows, err := s.conn.Query(ctx, statement, args...)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		if found {
			continue
		}
		found = true
		if err := rows.Scan(dest...); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	if !found {
		return errors.New("no row")
	}
	return nil
}

// closeRows closes rows and reports the first error the iteration met.
func closeRows(rows driver.Rows) error {
	err := rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	return err
}

func sortStrings(values []string) { sort.Strings(values) }
