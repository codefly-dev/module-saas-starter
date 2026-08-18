-- Typed audit-event registry (issue #170, ADR 0003).
--
-- audit_event_types is the database projection of the Go event catalog in
-- pkg/business/audit_registry.go. It is global reference data (no org_id), so
-- it is not tenant-scoped: readable by every tenant for the search/UI facet,
-- writable only by the control plane, which reconciles it from the code
-- catalog on startup (SyncAuditEventTypes). The rls-migration-gate ignores it
-- precisely because it carries no tenant column.

CREATE TABLE IF NOT EXISTS audit_event_types (
    name           TEXT PRIMARY KEY,
    version        INT NOT NULL DEFAULT 1,
    category       TEXT NOT NULL,
    owner          TEXT NOT NULL,
    payload_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    deprecated     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_event_types_category ON audit_event_types (category);

-- Per-relation grants are explicit from migration 61 onward.
GRANT SELECT ON audit_event_types TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON audit_event_types TO app_control_plane;
