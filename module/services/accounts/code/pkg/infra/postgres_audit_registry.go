package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// SyncAuditEventTypes upserts the code catalog into the audit_event_types
// projection table. Run under the control plane at startup so the DB facet and
// the Go registry never drift. Types no longer in the catalog are marked
// deprecated rather than deleted, so historical rows keep a resolvable type.
//
// The table also holds the types registered solutions declared, owned by
// "solution:<id>" (business.SolutionAuditOwnerPrefix). They were never in the
// code catalog, so the sync leaves them alone: deprecating them at every boot
// would retire a live solution's vocabulary.
//
// A catalog type that collides with one — the same name, or a namespace a
// solution holds — is refused with business.ErrAuditCatalogCollision before
// anything is written, never resolved by reassigning the row: historical events
// of that type were written under the solution's schema, and a release that
// silently took it over would reinterpret them. The caller fails boot on it.
func (s *PostgresStore) SyncAuditEventTypes(ctx context.Context, defs []business.AuditEventDefinition) error {
	q := s.getQueryExecutor(ctx)
	names := make([]string, 0, len(defs))
	namespaces := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, string(d.Type))
		namespaces = append(namespaces, d.Namespace)
	}
	var collidingName, collidingOwner string
	err := q.QueryRow(ctx, `
		SELECT name, owner FROM audit_event_types
		WHERE starts_with(owner, $3) AND (name = ANY($1) OR namespace = ANY($2))
		ORDER BY name LIMIT 1`,
		names, namespaces, business.SolutionAuditOwnerPrefix).Scan(&collidingName, &collidingOwner)
	switch {
	case err == nil:
		return fmt.Errorf("%w: the code catalog registers a type named or namespaced like %q, which %q declared",
			business.ErrAuditCatalogCollision, collidingName, collidingOwner)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	for _, d := range defs {
		// The owner condition is the same refusal for a declaration admitted
		// after the check above: the row is left alone and the sync fails.
		// The projected visibility is the compiled catalog's: EffectiveVisibility
		// reads it from the composed event catalog for a code-owned type. Writing
		// it here is what lets one table answer "may this leave the platform" for
		// the whole registry, instead of a reader having to know which half a
		// name came from.
		tag, err := q.Exec(ctx, `
			INSERT INTO audit_event_types (name, namespace, version, category, owner, visibility, payload_schema, deprecated, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, FALSE, NOW())
			ON CONFLICT (name) DO UPDATE SET
				namespace = EXCLUDED.namespace,
				version = EXCLUDED.version,
				category = EXCLUDED.category,
				owner = EXCLUDED.owner,
				visibility = EXCLUDED.visibility,
				payload_schema = EXCLUDED.payload_schema,
				deprecated = FALSE,
				updated_at = NOW()
			WHERE NOT starts_with(audit_event_types.owner, $8)`,
			string(d.Type), d.Namespace, d.Version, string(d.Category), d.Owner,
			d.EffectiveVisibility(), d.PayloadSchemaJSON(),
			business.SolutionAuditOwnerPrefix)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: the code catalog registers %q, which a solution declared",
				business.ErrAuditCatalogCollision, d.Type)
		}
	}
	_, err = q.Exec(ctx,
		`UPDATE audit_event_types SET deprecated = TRUE, updated_at = NOW()
		 WHERE name <> ALL($1) AND NOT starts_with(owner, $2)`,
		names, business.SolutionAuditOwnerPrefix)
	return err
}

func (s *PostgresStore) ListAuditEventTypes(ctx context.Context) ([]business.AuditEventTypeRow, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx,
		`SELECT name, namespace, version, category, owner, deprecated FROM audit_event_types ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.AuditEventTypeRow
	for rows.Next() {
		var r business.AuditEventTypeRow
		if err := rows.Scan(&r.Name, &r.Namespace, &r.Version, &r.Category, &r.Owner, &r.Deprecated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnsureAuditPartitions provisions the current month plus the next `months`
// (and the previous month, to absorb clock skew / late writes) via the
// SECURITY DEFINER maintenance function, then brings every partition's
// row-level security back in step with the parent's. Retention keeps far more
// months than this window provisions, and a partition queried by name is
// checked against its own policies, so a policy change on audit_events must
// reach the older partitions too. Called at startup and on each retention tick.
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
	if _, err := q.Exec(ctx, `SELECT audit_events_secure_all_partitions()`); err != nil {
		return fmt.Errorf("secure audit partitions: %w", err)
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
