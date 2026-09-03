-- User-owned dashboards — the configuration half of the dashboards model
-- (ADR 0007). One row is a named, owned dashboard: a serializable spec
-- (DashboardDef, validated on write) plus the lifecycle a bare spec literal
-- never had. Per-user view state (preferences) is NOT here; it composes onto
-- users.settings. The solution/template scope is not a row either — it stays
-- manifest-delivered, which is what makes it a read-only baseline.

CREATE TABLE dashboards (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    owner_id   UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    spec       JSONB NOT NULL,
    visibility TEXT NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'org')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT dashboards_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT dashboards_spec_object CHECK (
        jsonb_typeof(spec) = 'object'
        AND octet_length(spec::text) <= 262144
    )
);

-- The owner's own-collection read and the org-shared read are the two hot
-- paths; both are org-partitioned first.
CREATE INDEX idx_dashboards_org_owner ON dashboards (org_id, owner_id);
CREATE INDEX idx_dashboards_org_shared ON dashboards (org_id)
    WHERE visibility = 'org';

-- RLS: request-scoped own-org visibility only (direct-org_id recipe, migrations
-- 105/104). The policy never trusts a client-settable bypass GUC; cross-tenant
-- background work assumes app_control_plane, whose BYPASSRLS is a database
-- capability a caller cannot manufacture with set_config(). Owner-vs-admin edit
-- and org-shared read distinctions are enforced in the accounts handler layer;
-- RLS is the tenant boundary.
ALTER TABLE dashboards ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboards FORCE  ROW LEVEL SECURITY;
CREATE POLICY dashboards_tenant ON dashboards
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic reads,
-- writes, and deletes dashboards within its own org.
REVOKE ALL PRIVILEGES ON dashboards FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON dashboards TO app_tenant;

-- The control plane holds the full DML set new tables require (migration 67
-- convention; default privileges grant it nothing).
GRANT SELECT, INSERT, UPDATE, DELETE ON dashboards TO app_control_plane;
