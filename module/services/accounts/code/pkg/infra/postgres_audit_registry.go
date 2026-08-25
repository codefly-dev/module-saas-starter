package infra

import (
	"context"
	"fmt"
	"time"

	"accounts/pkg/business"
)

// SyncAuditEventTypes upserts the code catalog into the audit_event_types
// projection table. Run under the control plane at startup so the DB facet and
// the Go registry never drift. Types no longer in the catalog are marked
// deprecated rather than deleted, so historical rows keep a resolvable type.
func (s *PostgresStore) SyncAuditEventTypes(ctx context.Context, defs []business.AuditEventDefinition) error {
	q := s.getQueryExecutor(ctx)
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, string(d.Type))
		_, err := q.Exec(ctx, `
			INSERT INTO audit_event_types (name, version, category, owner, payload_schema, deprecated, updated_at)
			VALUES ($1, $2, $3, $4, $5, FALSE, NOW())
			ON CONFLICT (name) DO UPDATE SET
				version = EXCLUDED.version,
				category = EXCLUDED.category,
				owner = EXCLUDED.owner,
				payload_schema = EXCLUDED.payload_schema,
				deprecated = FALSE,
				updated_at = NOW()`,
			string(d.Type), d.Version, string(d.Category), d.Owner, d.PayloadSchemaJSON())
		if err != nil {
			return err
		}
	}
	_, err := q.Exec(ctx,
		`UPDATE audit_event_types SET deprecated = TRUE, updated_at = NOW() WHERE name <> ALL($1)`,
		names)
	return err
}

func (s *PostgresStore) ListAuditEventTypes(ctx context.Context) ([]business.AuditEventTypeRow, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx,
		`SELECT name, version, category, owner, deprecated FROM audit_event_types ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.AuditEventTypeRow
	for rows.Next() {
		var r business.AuditEventTypeRow
		if err := rows.Scan(&r.Name, &r.Version, &r.Category, &r.Owner, &r.Deprecated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnsureAuditPartitions provisions the current month plus the next `months`
// (and the previous month, to absorb clock skew / late writes) via the
// SECURITY DEFINER maintenance function. Called at startup and on each
// retention tick.
func (s *PostgresStore) EnsureAuditPartitions(ctx context.Context, months int) error {
	q := s.getQueryExecutor(ctx)
	base := time.Now().UTC()
	for m := -1; m <= months; m++ {
		month := base.AddDate(0, m, 0)
		first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
		if _, err := q.Exec(ctx, `SELECT audit_events_ensure_partition($1::date)`, first); err != nil {
			return fmt.Errorf("ensure audit partition %s: %w", first.Format("2006-01"), err)
		}
	}
	return nil
}

// DropAuditPartitionsBefore drops every monthly partition whose entire range is
// older than `before`, returning the number dropped. This is how audit
// retention runs — DDL that never trips the append-only trigger.
func (s *PostgresStore) DropAuditPartitionsBefore(ctx context.Context, before time.Time) (int64, error) {
	q := s.getQueryExecutor(ctx)
	var dropped int64
	if err := q.QueryRow(ctx, `SELECT audit_events_drop_partitions_before($1)`, before).Scan(&dropped); err != nil {
		return 0, err
	}
	return dropped, nil
}
