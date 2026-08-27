-- Connected datasources (issue #273/#274) — the "connection" half of datasource
-- ingestion. One row per tenant-declared external Source (today a GitHub repo +
-- paths) whose contents are pulled and kept fresh, then ingested into the
-- documents store as versioned Entries.
--
-- credential_secret_ref and webhook_secret_ref hold SecretCipher envelopes
-- (cfs1:vault-transit:...) — references into the secret provider, never a
-- plaintext access token or signing secret. Each envelope is bound to the row's
-- id via its purpose (github-connector:<id>, github-webhook:<id>) so ciphertext
-- cannot be replayed across sources.
--
-- RLS is the direct-org_id recipe (migration 68/92): request traffic sees only
-- its own org's sources. The inbound GitHub webhook receiver is unauthenticated
-- and has no tenant context, so it resolves a Source by id — and decrypts its
-- signing secret — through the audited app_control_plane role (BYPASSRLS is a
-- database capability, not a client-settable GUC), exactly like the pre-auth
-- identity-provider discovery reads.

CREATE TABLE IF NOT EXISTS datasource_sources (
    id                    UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id                UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    provider              TEXT NOT NULL CHECK (provider IN ('github')),
    repo                  TEXT NOT NULL,
    paths                 TEXT[] NOT NULL DEFAULT '{}',
    branch                TEXT,
    target_collection     TEXT NOT NULL,
    credential_secret_ref TEXT NOT NULL,
    webhook_secret_ref    TEXT,
    status                TEXT NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active', 'paused')),
    last_synced_at        TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ListSources is org-scoped; the RLS predicate already filters by org, but the
-- index keeps the ordered listing cheap as sources-per-org grows.
CREATE INDEX IF NOT EXISTS idx_datasource_sources_org
    ON datasource_sources (org_id, created_at DESC);

-- RLS: request-scoped own-org visibility only. The policy expresses just own-org
-- access and never trusts a client-settable GUC. Cross-tenant work — the
-- unauthenticated webhook receiver's by-id lookup — assumes app_control_plane,
-- whose BYPASSRLS is an explicit database capability.
ALTER TABLE datasource_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasource_sources FORCE  ROW LEVEL SECURITY;
CREATE POLICY datasource_sources_tenant ON datasource_sources
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic may read,
-- create, update, and delete its own org's sources. DeleteSource is a real row
-- delete (the credential must not linger), so DELETE is granted.
REVOKE ALL PRIVILEGES ON datasource_sources FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_sources TO app_tenant;

-- The control plane performs the cross-tenant, unauthenticated webhook by-id
-- lookup (source + signing secret) and owns cascade cleanup, so it holds the
-- full DML set new tables require (migration 67 convention).
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_sources TO app_control_plane;
