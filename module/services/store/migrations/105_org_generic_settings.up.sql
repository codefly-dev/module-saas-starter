-- Org-scoped generic typed settings — the organization analogue of
-- users.settings (migrations 22/80). One row per organization holds sparse
-- ProtoJSON for saas.accounts.v1.OrganizationSettings, whose product-contributed
-- fields live in the composed saas.composed.org_settings.v1.Settings container.
-- Adding an org setting is proto + code generation only; no column migration.
--
-- The recursive deep-merge / path-prune functions (migration 80) are schema-
-- agnostic and shared with the per-user surface. Storage stays sparse: an absent
-- leaf resolves to its catalog default; explicit presence (including false, "",
-- 0) is an override. This table is distinct from org_settings, the fixed-column
-- branding surface (migration 16).

CREATE TABLE IF NOT EXISTS org_generic_settings (
    org_id     UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    settings   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT org_generic_settings_typed_json_object CHECK (
        jsonb_typeof(settings) = 'object'
        AND octet_length(settings::text) <= 131072
    )
);

-- Mirrors idx_users_settings_gin: keeps jsonb-key filters over org settings
-- cheap as the number of tenants grows.
CREATE INDEX IF NOT EXISTS idx_org_generic_settings_gin
    ON org_generic_settings USING GIN (settings);

-- RLS: request-scoped own-org visibility only (direct-org_id recipe, migrations
-- 68/92/104). The policy never trusts a client-settable GUC; cross-tenant
-- background work assumes app_control_plane, whose BYPASSRLS is a database
-- capability a caller cannot manufacture with set_config().
ALTER TABLE org_generic_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_generic_settings FORCE  ROW LEVEL SECURITY;
CREATE POLICY org_generic_settings_tenant ON org_generic_settings
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic reads and
-- upserts its own org's settings. clear_mask prunes JSON paths in place, so a
-- row DELETE is never needed.
REVOKE ALL PRIVILEGES ON org_generic_settings FROM app_tenant;
GRANT SELECT, INSERT, UPDATE ON org_generic_settings TO app_tenant;

-- The control plane holds the full DML set new tables require (migration 67
-- convention; default privileges grant it nothing).
GRANT SELECT, INSERT, UPDATE, DELETE ON org_generic_settings TO app_control_plane;
