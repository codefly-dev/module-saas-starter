-- Datasource sources (issue #273): one durable connection from an organization
-- to an external system. saas-starter owns the connection surface; the
-- documents module owns ingestion. This table is the persistence spine both
-- build on — the GitHub connector (#274) stores its credential envelope in
-- credential_secret_ref and the webhook receiver (#275) resolves deliveries to
-- a row here.
--
-- config holds connector-specific settings (for GitHub: repository, branch,
-- paths) so the table stays connector-agnostic as new connectors are added.
--
-- credential_secret_ref holds a SecretCipher envelope (cfs1:vault-transit:...),
-- a reference into the secret provider — never a plaintext credential. It is
-- populated by the GitHub connector in #274; sources start without one.

CREATE TABLE IF NOT EXISTS datasource_sources (
    id                     UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id                 UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connector              TEXT NOT NULL CHECK (connector IN ('github')),
    display_name           TEXT NOT NULL,
    target_collection      TEXT NOT NULL,
    config                 JSONB NOT NULL DEFAULT '{}'::jsonb,
    credential_secret_ref  TEXT,
    status                 TEXT NOT NULL DEFAULT 'pending'
                               CHECK (status IN ('pending', 'active', 'disabled', 'error')),
    last_sync_requested_at TIMESTAMPTZ,
    last_synced_at         TIMESTAMPTZ,
    last_sync_error        TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- List and the org-scoped RLS lookups both filter by org_id.
CREATE INDEX IF NOT EXISTS idx_datasource_sources_org
    ON datasource_sources (org_id, created_at DESC);

-- RLS: request-scoped visibility only (direct-org_id recipe, migration 68). The
-- policy expresses just own-org access and never trusts a client-settable GUC.
-- Any cross-tenant work assumes the app_control_plane role, whose BYPASSRLS is
-- a database capability, not something a caller can manufacture.
ALTER TABLE datasource_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasource_sources FORCE  ROW LEVEL SECURITY;
CREATE POLICY datasource_sources_tenant ON datasource_sources
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic may read,
-- create, update, and delete its own org's sources.
REVOKE ALL PRIVILEGES ON datasource_sources FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_sources TO app_tenant;

-- The control plane owns any cross-tenant maintenance and retention deletes, so
-- it holds the full DML set new tables require (migration 67 convention).
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_sources TO app_control_plane;
