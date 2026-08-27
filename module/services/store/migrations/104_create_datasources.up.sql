-- Per-organization datasource connections (epic #277, connection half).
--
-- One row per org+repo records a GitHub source an org wants ingested: the
-- repository slug, optional path filters, the target documents collection, and
-- the access credential. credential_secret_ref holds a SecretCipher envelope
-- (cfs1:vault-transit:...), a reference into the secret provider — never a
-- plaintext token.
--
-- sync_status is the connection-side view: 'pending' once a sync is requested,
-- back to 'idle' when the ingest worker (documents) drains the request through
-- the jobs seam. Org-scoped config, RLS like org_identity_providers.

CREATE TABLE IF NOT EXISTS datasources (
    id                     UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id                 UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind                   TEXT NOT NULL DEFAULT 'github' CHECK (kind IN ('github')),
    repo                   TEXT NOT NULL,
    paths                  TEXT[] NOT NULL DEFAULT '{}',
    collection             TEXT NOT NULL,
    credential_secret_ref  TEXT NOT NULL,
    sync_status            TEXT NOT NULL DEFAULT 'idle'
                               CHECK (sync_status IN ('idle', 'pending')),
    last_sync_requested_at TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, repo)
);

-- Own-org listing hot path.
CREATE INDEX IF NOT EXISTS idx_datasources_org_id ON datasources (org_id);

-- RLS: request-scoped own-org visibility only (direct-org_id recipe). The
-- policy never trusts a client-settable GUC; any cross-tenant maintenance runs
-- as app_control_plane, whose BYPASSRLS is a database capability, not something
-- a caller can manufacture with set_config().
ALTER TABLE datasources ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasources FORCE  ROW LEVEL SECURITY;
CREATE POLICY datasources_tenant ON datasources
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic reads,
-- creates, and updates its own org's sources. Removal is not exposed yet, so no
-- DELETE grant is added.
REVOKE ALL PRIVILEGES ON datasources FROM app_tenant;
GRANT SELECT, INSERT, UPDATE ON datasources TO app_tenant;

-- The control plane owns any cross-tenant maintenance and retention deletes, so
-- it holds the full DML set new tables require (migration 67 convention).
GRANT SELECT, INSERT, UPDATE, DELETE ON datasources TO app_control_plane;
