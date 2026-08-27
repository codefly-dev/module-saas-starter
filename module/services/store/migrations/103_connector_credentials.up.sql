-- Per-source connector credential store (datasource ingestion, issue #274).
--
-- One row per datasource records the secret a connector needs to authenticate
-- to the upstream provider — for GitHub, the App credential (app id,
-- installation id, RSA private key) from which an installation access token is
-- minted. saas-starter owns "connection" (auth/creds/webhook); the Source it
-- keys against is defined by the Datasource/Connector contract (issue #273), so
-- source_id is carried as an opaque id with no FK yet.
--
-- secret_encrypted holds a SecretCipher envelope (cfs1:vault-transit:...), a
-- reference into the secret provider — never a plaintext credential. The
-- envelope is purpose-bound to the row via 'github-connector:<source_id>', so
-- ciphertext cannot be replayed across sources.
--
-- Org-scoped and RLS-isolated like org_identity_providers (migration 92):
-- visible/writable only when acting in the row's org; the control plane reaches
-- rows under its BYPASSRLS role (app_control_plane).

CREATE TABLE IF NOT EXISTS connector_credentials (
    id               UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id           UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    source_id        UUID NOT NULL,
    provider         TEXT NOT NULL CHECK (provider IN ('github')),
    secret_encrypted TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_id)
);

-- RLS: request-scoped own-org visibility only (direct-org_id recipe). The
-- FOR ALL policy admits every verb so the rls-migration-gate stays satisfied;
-- the explicit GRANTs below are what actually restrict each role's verbs. The
-- policy must never trust a client-settable GUC; cross-tenant background work
-- runs under app_control_plane, whose BYPASSRLS is a database capability a
-- caller cannot manufacture with set_config().
ALTER TABLE connector_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_credentials FORCE  ROW LEVEL SECURITY;
CREATE POLICY connector_credentials_tenant ON connector_credentials
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic reads,
-- stores, rotates, and deletes its own org's credentials.
REVOKE ALL PRIVILEGES ON connector_credentials FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON connector_credentials TO app_tenant;

-- The control plane performs any cross-tenant background reads (sync,
-- webhook re-fetch) and retention deletes, so it holds the full DML set new
-- tables require (migration 67 convention; default privileges grant it nothing).
GRANT SELECT, INSERT, UPDATE, DELETE ON connector_credentials TO app_control_plane;
