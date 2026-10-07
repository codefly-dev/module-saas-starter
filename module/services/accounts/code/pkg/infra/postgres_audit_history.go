package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5/pgconn"
)

// audit_events as the one-time history copy reads and retires it
// (business.AuditHistorySource). Every method runs on the control-plane
// transaction the caller opened: the copy spans every organization.

// auditPartitionsSQL lists the monthly partitions of audit_events — the ones
// audit_events_ensure_partition creates — with the bounds Postgres holds for
// each, parsed back from its own rendering of them so no time zone is assumed.
// The copy verifies the partitions this lists and drops those same ones by name,
// through audit_events_drop_verified_partitions, which reads the same bounds.
const auditPartitionsSQL = `
	SELECT c.relname,
	       (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[1]::timestamptz,
	       (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[2]::timestamptz
	FROM pg_inherits i
	JOIN pg_class c ON c.oid = i.inhrelid
	JOIN pg_class p ON p.oid = i.inhparent
	WHERE p.oid = 'public.audit_events'::regclass
	  AND c.relname ~ '^audit_events_[0-9]{4}_[0-9]{2}$'
	ORDER BY 2`

// ListAuditPartitions implements business.AuditHistorySource.
func (s *PostgresStore) ListAuditPartitions(ctx context.Context) ([]business.AuditHistoryPartition, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, auditPartitionsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.AuditHistoryPartition
	for rows.Next() {
		var partition business.AuditHistoryPartition
		var from, to *time.Time
		if err := rows.Scan(&partition.Name, &from, &to); err != nil {
			return nil, err
		}
		if from == nil || to == nil {
			return nil, fmt.Errorf("audit partition %s has no range bounds", partition.Name)
		}
		partition.From, partition.To = from.UTC(), to.UTC()
		out = append(out, partition)
	}
	return out, rows.Err()
}

// auditHistorySelectSQL reads rows as the queue does (auditQueueSelectSQL):
// every nullable column as text, so an entry carries exactly what a live
// event of the same row would.
const auditHistorySelectSQL = `
	SELECT id::text, event_type, schema_version,
	       COALESCE(actor_id::text, ''), actor_type, resource,
	       COALESCE(resource_id, ''), COALESCE(org_id::text, ''), payload,
	       COALESCE(ip_address, ''), created_at,
	       COALESCE(impersonated_by::text, ''), is_impersonated,
	       COALESCE(client_id, '')
	FROM public.audit_events
	WHERE created_at >= $1 AND created_at < $2`

// ReadAuditHistory implements business.AuditHistorySource.
func (s *PostgresStore) ReadAuditHistory(ctx context.Context, from, to time.Time, after *business.AuditHistoryCursor, limit int) ([]business.AuditEntry, error) {
	if limit < 1 {
		return nil, fmt.Errorf("audit history: limit must be positive, got %d", limit)
	}
	query := auditHistorySelectSQL
	args := []any{from, to}
	if after != nil {
		query += ` AND (created_at, id) > ($3, $4::uuid)`
		args = append(args, after.CreatedAt, after.ID)
	}
	query += fmt.Sprintf(` ORDER BY created_at, id LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	var out []business.AuditEntry
	err := s.visitAuditHistory(ctx, query, args, 0, func(entry business.AuditEntry) error {
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, err
}

// StreamAuditHistory hands rows on in source digest order without retaining a
// decoded page. A busy window can stop the scan immediately and be retried as
// halves; even one row's decoded containers are bounded before allocation.
func (s *PostgresStore) StreamAuditHistory(ctx context.Context, from, to time.Time, maxBytes int64, visit func(business.AuditEntry) error) error {
	if maxBytes <= 0 {
		return errors.New("audit history: streaming byte budget must be positive")
	}
	return s.visitAuditHistory(ctx, auditHistorySelectSQL+` ORDER BY created_at, id`, []any{from, to}, maxBytes, visit)
}

func (s *PostgresStore) visitAuditHistory(ctx context.Context, query string, args []any, maxBytes int64, visit func(business.AuditEntry) error) error {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entry business.AuditEntry
		var eventType string
		var payload []byte
		if err := rows.Scan(
			&entry.ID, &eventType, &entry.SchemaVersion,
			&entry.ActorID, &entry.ActorType, &entry.Resource,
			&entry.ResourceID, &entry.OrgID, &payload,
			&entry.IPAddress, &entry.CreatedAt,
			&entry.ImpersonatedBy, &entry.IsImpersonated,
			&entry.ClientID,
		); err != nil {
			return err
		}
		entry.EventType = business.EventType(eventType)
		entry.CreatedAt = entry.CreatedAt.UTC()
		if maxBytes > 0 {
			remaining := maxBytes - business.AuditHistoryEntryBytes(entry)
			entry.Payload, err = decodeBoundedHistoryPayload(payload, remaining)
		} else {
			entry.Payload, err = decodeQueuedPayload(payload)
		}
		if err != nil {
			return fmt.Errorf("audit history: payload of event %s: %w", entry.ID, err)
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	return rows.Err()
}

// decodeBoundedHistoryPayload counts the raw row and decoded container entries
// as it decodes. Checking only raw JSON length misses small JSON arrays that
// allocate many maps or interfaces; checking after Decode is too late.
func decodeBoundedHistoryPayload(raw []byte, limit int64) (map[string]any, error) {
	held := int64(len(raw))
	take := func(n int64) error {
		held += n
		if held > limit {
			return business.ErrAuditHistoryWindowTooLarge
		}
		return nil
	}
	if err := take(0); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func() (any, error)
	value = func() (any, error) {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				if err := take(64); err != nil {
					return nil, err
				}
				object := map[string]any{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return nil, err
					}
					text, ok := key.(string)
					if !ok {
						return nil, errors.New("audit history: expected a JSON object key")
					}
					if err := take(64 + int64(len(text))); err != nil {
						return nil, err
					}
					item, err := value()
					if err != nil {
						return nil, err
					}
					object[text] = item
				}
				_, err := decoder.Token()
				return object, err
			case '[':
				if err := take(24); err != nil {
					return nil, err
				}
				var array []any
				for decoder.More() {
					if err := take(16); err != nil {
						return nil, err
					}
					item, err := value()
					if err != nil {
						return nil, err
					}
					array = append(array, item)
				}
				_, err := decoder.Token()
				return array, err
			default:
				return nil, errors.New("audit history: unexpected JSON delimiter")
			}
		}
		n := int64(16)
		switch text := token.(type) {
		case string:
			n += int64(len(text))
		case json.Number:
			n += int64(len(text))
		}
		if err := take(n); err != nil {
			return nil, err
		}
		return token, nil
	}
	decoded, err := value()
	if err != nil {
		return nil, err
	}
	if decoded == nil {
		return nil, nil
	}
	payload, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("audit history: payload must be a JSON object")
	}
	return payload, nil
}

// CountAuditHistory implements business.AuditHistorySource.
func (s *PostgresStore) CountAuditHistory(ctx context.Context, from, to time.Time) (map[string]int64, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT COALESCE(org_id::text, ''), count(*)
		FROM public.audit_events
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var org string
		var n int64
		if err := rows.Scan(&org, &n); err != nil {
			return nil, err
		}
		counts[org] = n
	}
	return counts, rows.Err()
}

// CountAuditHistoryByTable implements business.AuditHistorySource. It reads
// through the parent, so partition pruning keeps it to the partitions that
// overlap [from, to) and the default partition, and tableoid names the table a
// row sits in.
func (s *PostgresStore) CountAuditHistoryByTable(ctx context.Context, from, to time.Time) ([]business.AuditHistoryTableRows, error) {
	query := `
		SELECT tableoid::regclass::text, count(*)
		FROM public.audit_events
		WHERE created_at < $1`
	args := []any{to}
	if !from.IsZero() {
		query += ` AND created_at >= $2`
		args = append(args, from)
	}
	query += ` GROUP BY tableoid ORDER BY 1`
	rows, err := s.getQueryExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.AuditHistoryTableRows
	for rows.Next() {
		var table business.AuditHistoryTableRows
		if err := rows.Scan(&table.Table, &table.Rows); err != nil {
			return nil, err
		}
		out = append(out, table)
	}
	return out, rows.Err()
}

// The SQLSTATE audit_events_drop_verified_partitions raises when it refuses,
// and the one PostgreSQL raises when a lock wait ends (lock_timeout).
const (
	auditDropRefusedState = "AH001"
	lockNotAvailableState = "55P03"
	auditDropRefusedLead  = "audit history drop refused: "
)

// DropVerifiedAuditPartitions implements business.AuditHistorySource. The
// database function locks audit_events and each named partition, confirms from
// the catalog that each is a partition whose upper bound is at or before the
// cutoff, recounts each against the number it was verified with, and only then
// drops exactly those partitions, all in the caller's transaction.
func (s *PostgresStore) DropVerifiedAuditPartitions(ctx context.Context, cutoff time.Time, partitions []business.AuditHistoryDropTarget) ([]string, error) {
	names := make([]string, 0, len(partitions))
	rows := make([]int64, 0, len(partitions))
	for _, partition := range partitions {
		names = append(names, partition.Name)
		rows = append(rows, partition.Rows)
	}
	var dropped []string
	err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT public.audit_events_drop_verified_partitions($1, $2::text[], $3::bigint[])`, cutoff, names, rows).Scan(&dropped)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case auditDropRefusedState:
			return nil, &business.AuditHistoryDropRefusal{Reason: strings.TrimPrefix(pgErr.Message, auditDropRefusedLead)}
		case lockNotAvailableState:
			return nil, &business.AuditHistoryDropRefusal{Reason: "audit_events is locked by another transaction that did not finish in time; nothing was dropped"}
		}
	}
	if err != nil {
		return nil, err
	}
	return dropped, nil
}

var _ business.AuditHistorySource = (*PostgresStore)(nil)
var _ business.AuditHistoryStreamingSource = (*PostgresStore)(nil)
