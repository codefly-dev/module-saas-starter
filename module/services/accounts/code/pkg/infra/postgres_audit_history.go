package infra

import (
	"context"
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
	rows, err := s.getQueryExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.AuditEntry
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
			return nil, err
		}
		entry.EventType = business.EventType(eventType)
		entry.CreatedAt = entry.CreatedAt.UTC()
		if entry.Payload, err = decodeQueuedPayload(payload); err != nil {
			return nil, fmt.Errorf("audit history: payload of event %s: %w", entry.ID, err)
		}
		out = append(out, entry)
	}
	return out, rows.Err()
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
