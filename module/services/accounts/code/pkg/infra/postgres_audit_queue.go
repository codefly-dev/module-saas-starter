package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// Snapshot reads the queue's depth, its oldest row and the size of the
// quarantine for relay telemetry, in one statement so the three agree. The
// worker pool has the queue's and the quarantine's cross-organization SELECT
// grant; request traffic never receives this pool.
func (q *PostgresAuditQueue) Snapshot(ctx context.Context) (business.AuditQueueSnapshot, error) {
	var snapshot business.AuditQueueSnapshot
	err := q.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM public.audit_event_queue),
		       (SELECT min(enqueued_at) FROM public.audit_event_queue),
		       (SELECT count(*) FROM public.audit_event_quarantine)`,
	).Scan(&snapshot.Depth, &snapshot.OldestEnqueuedAt, &snapshot.Quarantined)
	if err != nil {
		return business.AuditQueueSnapshot{}, fmt.Errorf("audit queue: observe: %w", err)
	}
	return snapshot, nil
}

// auditQueueSelectSQL reads the oldest queued events whose writing
// transactions took their ids before the oldest transaction still running
// (the snapshot's xmin): every transaction that old has finished, so its rows
// are complete. The gate is transaction visibility, not sequence order. A row
// can carry a lower sequence number than one already handed over when its
// transaction took its id later; that is harmless, because both stores key a
// record by event id and every read orders by event time.
//
// The same gate holds the whole queue behind a write transaction left open (a
// stuck session, a long migration), and behind rows whose transaction ids are
// ahead of the cluster's after a logical restore into a new cluster. Both
// stall delivery without losing anything; MEASUREMENT_RUNBOOKS.md (`audit-relay`)
// says how to tell them apart.
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
// transaction holding the relay lease: read the batch, hand it to deliver, then
// apply what deliver decided — delete the delivered rows and move the
// quarantined ones into audit_event_quarantine, in the same transaction — and
// commit. A delivery that fails, a crash, or a lost connection rolls the
// transaction back and leaves every row queued.
func (q *PostgresAuditQueue) Drain(
	ctx context.Context,
	limit int,
	deliver func(ctx context.Context, events []business.QueuedAuditEvent) (business.AuditDeliveryOutcome, error),
) (business.AuditDrainResult, error) {
	var none business.AuditDrainResult
	if limit < 1 {
		return none, fmt.Errorf("audit queue: limit must be positive, got %d", limit)
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return none, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op

	var leased bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, auditRelayLeaseKey).Scan(&leased); err != nil {
		return none, fmt.Errorf("audit queue: take relay lease: %w", err)
	}
	if !leased {
		return none, nil
	}

	rows, err := tx.Query(ctx, auditQueueSelectSQL, limit)
	if err != nil {
		return none, fmt.Errorf("audit queue: read: %w", err)
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
			return none, fmt.Errorf("audit queue: scan: %w", err)
		}
		entry.EventType = business.EventType(eventType)
		entry.CreatedAt = entry.CreatedAt.UTC()
		event.EnqueuedAt = event.EnqueuedAt.UTC()
		if entry.Payload, err = decodeQueuedPayload(payload); err != nil {
			rows.Close()
			return none, fmt.Errorf("audit queue: payload of event %s: %w", entry.ID, err)
		}
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return none, fmt.Errorf("audit queue: read: %w", err)
	}

	outcome, err := deliver(ctx, events)
	if err != nil {
		return none, err
	}
	if len(outcome.Delivered)+len(outcome.Quarantined) == 0 {
		return none, outcome.Pending
	}
	if err := checkOutcomeNamesOnlyTheseRows(outcome, events); err != nil {
		return none, err
	}

	// The writes are acknowledged, so recording them is worth finishing even when
	// the delivery used up its own deadline.
	bookkeeping, cancel := context.WithTimeout(context.WithoutCancel(ctx), business.AuditRelayBookkeepingTimeout)
	defer cancel()

	if len(outcome.Delivered) > 0 {
		tag, err := tx.Exec(bookkeeping, `DELETE FROM public.audit_event_queue WHERE seq = ANY($1)`, outcome.Delivered)
		if err != nil {
			return none, fmt.Errorf("audit queue: delete delivered rows: %w", err)
		}
		if tag.RowsAffected() != int64(len(outcome.Delivered)) {
			// Only the lease holder deletes, so this is a broken invariant, not a race.
			return none, fmt.Errorf("audit queue: deleted %d of %d delivered rows", tag.RowsAffected(), len(outcome.Delivered))
		}
	}
	if len(outcome.Quarantined) > 0 {
		seqs := make([]int64, len(outcome.Quarantined))
		reasons := make([]string, len(outcome.Quarantined))
		for i, set := range outcome.Quarantined {
			// The relay bounds a reason already; the queue holds the constraint
			// (text without NUL, valid UTF-8), so it does not trust any caller to,
			// and a reason it cannot store would roll back the delivered rows too,
			// on every pass.
			seqs[i], reasons[i] = set.Seq, business.AuditQuarantineReason(set.Reason)
		}
		tag, err := tx.Exec(bookkeeping, auditQueueQuarantineSQL, seqs, reasons)
		if err != nil {
			return none, fmt.Errorf("audit queue: quarantine rows: %w", err)
		}
		if tag.RowsAffected() != int64(len(seqs)) {
			return none, fmt.Errorf("audit queue: quarantined %d of %d rows", tag.RowsAffected(), len(seqs))
		}
	}
	if err := tx.Commit(bookkeeping); err != nil {
		return none, fmt.Errorf("audit queue: commit delivered batch: %w", err)
	}
	return business.AuditDrainResult{Delivered: len(outcome.Delivered), Quarantined: len(outcome.Quarantined)}, outcome.Pending
}

// auditQueueQuarantineSQL moves rows out of the queue into the quarantine in a
// single statement, so a row is in exactly one of the two whatever happens:
// the DELETE's rows feed the INSERT, and a failure of either undoes both.
const auditQueueQuarantineSQL = `
	WITH moved AS (
		DELETE FROM public.audit_event_queue WHERE seq = ANY($1::bigint[]) RETURNING *
	)
	INSERT INTO public.audit_event_quarantine (
		seq, xact_id, id, event_type, schema_version, actor_id, actor_type,
		resource, resource_id, org_id, payload, ip_address, created_at,
		impersonated_by, is_impersonated, client_id, enqueued_at, error
	)
	SELECT m.seq, m.xact_id, m.id, m.event_type, m.schema_version, m.actor_id, m.actor_type,
	       m.resource, m.resource_id, m.org_id, m.payload, m.ip_address, m.created_at,
	       m.impersonated_by, m.is_impersonated, m.client_id, m.enqueued_at, why.error
	FROM moved m
	JOIN unnest($1::bigint[], $2::text[]) AS why(seq, error) ON why.seq = m.seq`

// checkOutcomeNamesOnlyTheseRows refuses an outcome that names a row this drain
// did not read, or names one twice: applying it would delete or move a row no
// one delivered.
func checkOutcomeNamesOnlyTheseRows(outcome business.AuditDeliveryOutcome, events []business.QueuedAuditEvent) error {
	read := make(map[int64]bool, len(events))
	for _, event := range events {
		read[event.Seq] = true
	}
	named := func(seq int64) error {
		if !read[seq] {
			return fmt.Errorf("audit queue: the outcome names row %d, which this drain did not read", seq)
		}
		delete(read, seq)
		return nil
	}
	for _, seq := range outcome.Delivered {
		if err := named(seq); err != nil {
			return err
		}
	}
	for _, set := range outcome.Quarantined {
		if err := named(set.Seq); err != nil {
			return err
		}
	}
	return nil
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
