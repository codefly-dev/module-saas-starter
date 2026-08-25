-- Reverse of 102: recreate audit_export_configs in the state it held at
-- version 101 — table + index (migration 19) with forced tenant RLS and
-- the org_id-only policy (migration 23 as hardened by migration 68).
--
-- The full state is restored, not just the bare table, so a rollback past
-- this point leaves the earlier downs consistent: 68's `ALTER POLICY
-- audit_export_configs_tenant` (no IF EXISTS) requires the policy to be
-- present, and 23's down expects RLS enabled before it disables it.

CREATE TABLE IF NOT EXISTS audit_export_configs (
    id              UUID PRIMARY KEY,
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    bucket          TEXT NOT NULL,
    region          TEXT NOT NULL DEFAULT 'us-east-1',
    endpoint        TEXT,
    prefix          TEXT NOT NULL DEFAULT '',
    access_key_id     TEXT NOT NULL,
    secret_access_key TEXT NOT NULL,
    cadence_minutes INT NOT NULL DEFAULT 60 CHECK (cadence_minutes >= 5),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    last_exported_at TIMESTAMPTZ,
    last_error      TEXT,
    last_error_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id)
);

CREATE INDEX IF NOT EXISTS idx_audit_export_configs_enabled_due
    ON audit_export_configs (last_exported_at NULLS FIRST)
    WHERE enabled;

ALTER TABLE audit_export_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_export_configs FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_export_configs_tenant ON audit_export_configs
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
