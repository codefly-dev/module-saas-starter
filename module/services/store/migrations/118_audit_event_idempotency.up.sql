-- Audit-emit idempotency guard (issue #511). A module emits audit events over
-- ModuleEmitAuditEvent, and a retried RPC (client timeout, at-least-once queue
-- redelivery) must not write the same compliance event twice. audit_events
-- itself cannot carry the unique constraint: it is range-partitioned by
-- created_at (migration 97), so any unique index must include created_at — which
-- differs across retries and so never collides. This separate, non-partitioned
-- guard table holds the dedup key instead.
--
-- The emitter, inside the same transaction that writes the audit row, first
-- INSERTs (org_id, event_type, idempotency_key) here with ON CONFLICT DO NOTHING.
-- A first emit inserts the guard row and proceeds to write the event; a duplicate
-- inserts zero rows and the emitter returns success without writing again, so the
-- guard and the audit row commit atomically — a rolled-back audit write also
-- rolls back its guard, keeping them consistent.
--
-- Scoping is per tenant so one tenant's idempotency keys can never suppress
-- another tenant's events: the key is (org_id, event_type, idempotency_key), not
-- a global (event_type, idempotency_key). System-scoped emits (empty tenant) have
-- no org and are written under the control plane; they use the all-zero sentinel
-- org id below so the primary key stays NOT NULL and their duplicates still
-- collapse (a NULL org column could not be part of the primary key, and a UNIQUE
-- index treats NULLs as distinct — either would silently disable dedup for
-- system events). The sentinel is not a generated organization id, so it can
-- never collide with a real tenant's rows.
--
-- Classification (DATABASE_AUTHORITY.md): a TENANT relation, forced RLS on
-- app.current_org_id with the exact app_tenant/app_control_plane grants of the
-- installations/audit_events convention (migrations 112, 97). app_tenant writes
-- its own org's guard rows on the tenant emit path; app_control_plane writes the
-- sentinel-org system rows on the control-plane emit path.

CREATE TABLE IF NOT EXISTS audit_event_idempotency (
    org_id          UUID NOT NULL,
    event_type      TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, event_type, idempotency_key)
);

ALTER TABLE audit_event_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_event_idempotency FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_event_idempotency_tenant ON audit_event_idempotency
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- The guard is append-only at runtime: it is inserted and read (via the INSERT's
-- ON CONFLICT), never updated or deleted on the request path. Grant only
-- SELECT + INSERT, mirroring audit_events (migration 97).
REVOKE ALL PRIVILEGES ON audit_event_idempotency FROM app_tenant;
GRANT SELECT, INSERT ON audit_event_idempotency TO app_tenant;
GRANT SELECT, INSERT ON audit_event_idempotency TO app_control_plane;
