package business

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The audit store swap (ADR 0009). Under AUDIT_SINK=postgres (the default) and
// AUDIT_SINK=both the audit store of record is the audit_events table, exactly
// as before. Under a swap value a warehouse is the store of record instead:
// the emitter writes each event into the audit_event_queue table on the
// caller's transaction, and AuditRelay drains that queue into the warehouse and
// a locked object-storage archive.

// AuditSinkMode is the value of AUDIT_SINK.
type AuditSinkMode string

const (
	// AuditSinkPostgres keeps every audit record in audit_events. The default.
	AuditSinkPostgres AuditSinkMode = "postgres"
	// AuditSinkBoth is ADR 0006's tee: audit_events stays the store of record
	// and each organization's events are also posted to an HTTP endpoint.
	AuditSinkBoth AuditSinkMode = "both"
	// AuditSinkBigQuery is a swap value: BigQuery is the store of record and
	// Postgres keeps only the transactional queue.
	AuditSinkBigQuery AuditSinkMode = "bigquery"
	// AuditSinkClickHouse is a swap value: ClickHouse is the store of record and
	// Postgres keeps only the transactional queue.
	AuditSinkClickHouse AuditSinkMode = "clickhouse"
)

// ParseAuditSinkMode reads an AUDIT_SINK value. Empty is the default.
func ParseAuditSinkMode(raw string) (AuditSinkMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(AuditSinkPostgres):
		return AuditSinkPostgres, nil
	case string(AuditSinkBoth):
		return AuditSinkBoth, nil
	case string(AuditSinkBigQuery):
		return AuditSinkBigQuery, nil
	case string(AuditSinkClickHouse):
		return AuditSinkClickHouse, nil
	case "external":
		return "", errors.New("AUDIT_SINK=external is not permitted: no external destination can join the transaction " +
			"a change commits in, so it cannot receive the change's record atomically; use AUDIT_SINK=both to copy " +
			"records to an HTTP endpoint, or a swap value (bigquery or clickhouse), which commits each record to a Postgres queue first")
	default:
		return "", fmt.Errorf("AUDIT_SINK must be %s, %s, %s or %s", AuditSinkPostgres, AuditSinkBoth, AuditSinkBigQuery, AuditSinkClickHouse)
	}
}

// Swaps reports whether the mode makes a warehouse the audit store of record,
// so the emitter writes the queue instead of audit_events.
func (m AuditSinkMode) Swaps() bool {
	return m == AuditSinkBigQuery || m == AuditSinkClickHouse
}

// AuditRecorder writes the record of one event on the caller's transaction and
// nothing more: the audit_events row, or under a swap value the queue row. It
// publishes no domain event and enqueues no tee job. It is the path for the
// writers that run outside the Service — the authentication resolver and the
// role catalog import — which recorded their events with no fan-out before the
// swap, and keep doing exactly that. DurableAuditEmitter implements it.
type AuditRecorder interface {
	RecordTx(ctx context.Context, entry AuditEntry) error
}

// AuditStoreWriter is the write half of AuditStore.
type AuditStoreWriter interface {
	// AppendAuditBatch appends one relay batch, in its order. It is
	// at-least-once: a batch that failed, or whose queue rows were not deleted
	// afterwards, is delivered again. The store keys every record by its event
	// id and may hold an event more than once after a redelivery; every read of
	// the store returns each event once by event id, exactly as every reader of
	// the archive does.
	AppendAuditBatch(ctx context.Context, batch AuditBatch) error
}

// AuditStore is the audit store of record under a swap value (ADR 0009): one
// interface carrying the write half, which the relay uses, and the read half —
// the activity list, aggregation and export (AuditReader) and the
// readable-source query (AuditSourceSyncReader) — which the service uses. The
// relay depends on AuditStoreWriter alone and the service on the read halves
// alone.
type AuditStore interface {
	AuditStoreWriter
	AuditReader
	AuditSourceSyncReader
}

// AuditArchive writes a relay batch to the locked object-storage archive as one
// object. An object is never overwritten: a batch delivered again is written as
// a new object, and archive readers deduplicate by event id.
type AuditArchive interface {
	WriteAuditBatch(ctx context.Context, batch AuditBatch) error
}

// AuditBatch is one delivery: the records of a run of queued events, oldest
// first. The order is not a guarantee across batches (see AuditQueue.Drain): a
// store keys a record by event id and a read orders by event time.
type AuditBatch struct {
	// ID is unique to this delivery attempt; the archive names its object by it.
	ID string
	// DeploymentID names the deployment the events were written in.
	DeploymentID string
	// ComposedAt is when the relay composed the batch.
	ComposedAt time.Time
	Records    []AuditRecord
}

// AuditRecord is one event as a store of record keeps it.
type AuditRecord struct {
	// Entry is the event's envelope as it was committed. Its Payload is not read
	// downstream: Details is the one serialization of it.
	Entry AuditEntry
	// Retention decides where the full details go (AuditRetentionClass).
	Retention AuditRetentionClass
	// Details is the canonical JSON of the event's payload (CanonicalAuditDetails).
	Details string
	// DetailsSHA256 is the lowercase hex SHA-256 of Details.
	DetailsSHA256 string
}

// NewAuditRecord builds the record of entry under class.
func NewAuditRecord(entry AuditEntry, class AuditRetentionClass) (AuditRecord, error) {
	details, err := CanonicalAuditDetails(entry.Payload)
	if err != nil {
		return AuditRecord{}, fmt.Errorf("audit: canonical details of event %s: %w", entry.ID, err)
	}
	return AuditRecord{
		Entry:         entry,
		Retention:     class,
		Details:       details,
		DetailsSHA256: AuditDetailsSHA256(details),
	}, nil
}

// CanonicalAuditDetails is the one serialization of an event's details that a
// store of record keeps and hashes: JSON with object keys sorted at every
// level, no insignificant whitespace, and no HTML escaping. An empty payload is
// "{}", as audit_events stores it. Numbers keep the text they were read with
// when the payload was decoded with json.Number, which the queue does.
//
// Every store keeps the string itself, never a re-encoding of it, so a reader
// verifies a record by hashing the stored string — it never has to reproduce
// this function.
func CanonicalAuditDetails(payload map[string]any) (string, error) {
	if len(payload) == 0 {
		return "{}", nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// AuditDetailsSHA256 is the lowercase hex SHA-256 of a canonical details string.
func AuditDetailsSHA256(details string) string {
	sum := sha256.Sum256([]byte(details))
	return hex.EncodeToString(sum[:])
}
