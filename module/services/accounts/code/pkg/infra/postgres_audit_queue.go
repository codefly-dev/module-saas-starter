package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5/pgxpool"
)

// auditRelayLeaseKey names the advisory lock one relay holds for the length of
// a delivery, so replicas take turns rather than deliver the same rows twice
// or interleave one organization's events.
const auditRelayLeaseKey = "audit-relay:audit_event_queue"

// NewAuditRelayPool opens the audit relay's own pool, on the control-plane
// login assuming app_job_worker — the one role that may read and delete queued
// audit events. It is separate from the job workers' pool because a delivery
// holds its transaction open across the archive and warehouse writes, and a
// slow warehouse must not take a connection the job workers are waiting for.
func NewAuditRelayPool(ctx context.Context) (*pgxpool.Pool, error) {
	w := wool.Get(ctx).In("NewAuditRelayPool")
	connection, err := storeConnection(ctx, controlPlaneConnectionKey)
	if err != nil {
		return nil, w.Wrapf(err, "failed to get connection string")
	}
	return NewAuditRelayPoolFromURL(ctx, connection)
}

// NewAuditRelayPoolFromURL is the explicit-URL variant for integration tests.
func NewAuditRelayPoolFromURL(ctx context.Context, connectionURL string) (*pgxpool.Pool, error) {
	return newWorkerPoolFromURL(ctx, connectionURL, workerPoolConfig{
		role:            jobWorkerDatabaseRole,
		applicationName: "accounts-audit-relay",
		logScope:        "NewAuditRelayPool",
	})
}

// PostgresAuditQueue is the relay's side of audit_event_queue
// (business.AuditQueue).
type PostgresAuditQueue struct {
	pool *pgxpool.Pool
}

// NewPostgresAuditQueue drains the queue over pool, whose connections must
// assume app_job_worker (NewAuditRelayPool).
func NewPostgresAuditQueue(pool *pgxpool.Pool) (*PostgresAuditQueue, error) {
	if pool == nil {
		return nil, errors.New("audit queue: pool is required")
	}
	return &PostgresAuditQueue{pool: pool}, nil
}

// Snapshot reads the queue's depth and oldest row for relay-lag telemetry.
// The worker pool has the queue's cross-organization SELECT grant; request
// traffic never receives this pool.
func (q *PostgresAuditQueue) Snapshot(ctx context.Context) (business.AuditQueueSnapshot, error) {
	var depth int64
	var oldest *time.Time
	err := q.pool.QueryRow(ctx, `SELECT count(*), min(enqueued_at) FROM public.audit_event_queue`).Scan(&depth, &oldest)
	if err != nil {
		return business.AuditQueueSnapshot{}, fmt.Errorf("audit queue: observe: %w", err)
	}
	return business.AuditQueueSnapshot{Depth: depth, OldestEnqueuedAt: oldest}, nil
}

// auditQueueSelectSQL reads the oldest queued events whose writing
// transactions are below the snapshot's xmin — every transaction that old has
// finished, so nothing still in flight can later commit a row ahead of these.
const auditQueueSelectSQL = `
	SELECT seq, id::text, event_type, schema_version,
	       COALESCE(actor_id::text, ''), actor_type, resource,
	       COALESCE(resource_id, ''), COALESCE(org_id::text, ''), payload,
	       COALESCE(ip_address, ''), created_at,
	       COALESCE(impersonated_by::text, ''), is_impersonated,
	       COALESCE(client_id, ''), enqueued_at
	FROM public.audit_event_queue
	WHERE xact_id < pg_snapshot_xmin(pg_current_snapshot())
	ORDER BY seq
	LIMIT $1`

// Drain implements business.AuditQueue. The whole delivery runs in one
// transaction holding the relay lease: read the batch, hand it to deliver,
// delete exactly its rows, commit. A delivery that fails, a crash, or a lost
// connection rolls the transaction back and leaves every row queued.
func (q *PostgresAuditQueue) Drain(
	ctx context.Context,
	limit int,
	deliver func(ctx context.Context, events []business.QueuedAuditEvent) (bool, error),
) (int, error) {
	if limit < 1 {
		return 0, fmt.Errorf("audit queue: limit must be positive, got %d", limit)
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op

	var leased bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, auditRelayLeaseKey).Scan(&leased); err != nil {
		return 0, fmt.Errorf("audit queue: take relay lease: %w", err)
	}
	if !leased {
		return 0, nil
	}

	rows, err := tx.Query(ctx, auditQueueSelectSQL, limit)
	if err != nil {
		return 0, fmt.Errorf("audit queue: read: %w", err)
	}
	var events []business.QueuedAuditEvent
	for rows.Next() {
		var (
			event     business.QueuedAuditEvent
			eventType string
			payload   []byte
		)
		entry := &event.Entry
		if err := rows.Scan(
			&event.Seq, &entry.ID, &eventType, &entry.SchemaVersion,
			&entry.ActorID, &entry.ActorType, &entry.Resource,
			&entry.ResourceID, &entry.OrgID, &payload,
			&entry.IPAddress, &entry.CreatedAt,
			&entry.ImpersonatedBy, &entry.IsImpersonated,
			&entry.ClientID, &event.EnqueuedAt,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("audit queue: scan: %w", err)
		}
		entry.EventType = business.EventType(eventType)
		entry.CreatedAt = entry.CreatedAt.UTC()
		event.EnqueuedAt = event.EnqueuedAt.UTC()
		if entry.Payload, err = decodeQueuedPayload(payload); err != nil {
			rows.Close()
			return 0, fmt.Errorf("audit queue: payload of event %s: %w", entry.ID, err)
		}
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("audit queue: read: %w", err)
	}

	delivered, err := deliver(ctx, events)
	if err != nil || !delivered {
		return 0, err
	}

	seqs := make([]int64, len(events))
	for i, event := range events {
		seqs[i] = event.Seq
	}
	tag, err := tx.Exec(ctx, `DELETE FROM public.audit_event_queue WHERE seq = ANY($1)`, seqs)
	if err != nil {
		return 0, fmt.Errorf("audit queue: delete delivered rows: %w", err)
	}
	if tag.RowsAffected() != int64(len(seqs)) {
		// Only the lease holder deletes, so this is a broken invariant, not a race.
		return 0, fmt.Errorf("audit queue: deleted %d of %d delivered rows", tag.RowsAffected(), len(seqs))
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("audit queue: commit delivered batch: %w", err)
	}
	return len(events), nil
}

// decodeQueuedPayload decodes a queued payload keeping each number's text
// (json.Number), so the canonical details carry exactly what Postgres stored.
func decodeQueuedPayload(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}
