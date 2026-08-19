-- Hierarchical scope registry + hierarchical role grants + per-record share
-- overlay (issue #178; RFC-0001 + RFC-0002). Closes gap analysis gaps 1, 2, 5, 6.
--
-- All three are TENANT relations: forced RLS on app.current_org_id, exact
-- app_tenant grants, full DML to app_control_plane. RLS here is only the tenant
-- floor — subject resolution happens in CheckAccess's query WHERE clause,
-- exactly as role_assignments does today. This is strictly additive:
-- role_assignments and CheckPermission are untouched.

-- ltree gives materialized-path ancestor tests (@>) with a GiST index.
-- btree_gist lets org_id and scope_path share one GiST index so the tenant
-- filter and the ancestor test are one index probe. Both are trusted extensions.
CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ── 1. Typed scope registry (closes gap 6) ───────────────────────────────
-- One row per node in an org's scope tree. Path labels are org-rooted, e.g.
-- 'foundation.solution_42.customer_7'. A node whose resource_id is set is a
-- *placed record* (a leaf identifying a real product row); a node with NULL
-- resource identity is a structural node (foundation/solution/customer/...).
--
-- LABEL ENCODING (RFC-0001 open-question 1, decided): ltree labels are the
-- lowercase set [a-z0-9_] joined by dots. Raw UUIDs are NOT valid labels
-- (hyphens); encode them by lowercasing and replacing '-' with '_'. The CHECK
-- enforces the charset at the boundary so an ill-formed path can never be stored.
CREATE TABLE IF NOT EXISTS scope_nodes (
    id            UUID  DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id        UUID  NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    scope_path    LTREE NOT NULL,
    kind          TEXT  NOT NULL,          -- product-defined node type, e.g. 'solution','customer','record'
    label         TEXT  NOT NULL,          -- human display name
    resource_type TEXT,                    -- NULL for structural nodes
    resource_id   TEXT,                    -- NULL for structural nodes; set when the node is a placed record
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, scope_path),
    CONSTRAINT scope_nodes_label_charset
        CHECK (scope_path::text ~ '^[a-z0-9_]+(\.[a-z0-9_]+)*$'),
    -- A placement carries both resource columns or neither.
    CONSTRAINT scope_nodes_resource_paired
        CHECK ((resource_type IS NULL) = (resource_id IS NULL))
);
-- Combined GiST: tenant filter + ancestor/descendant operators in one probe.
CREATE INDEX IF NOT EXISTS idx_scope_nodes_path
    ON scope_nodes USING GIST (org_id, scope_path);
-- A product row maps to exactly one scope node, so CheckAccess resolves the
-- record's TRUE path from (resource_type, resource_id) deterministically.
CREATE UNIQUE INDEX IF NOT EXISTS idx_scope_nodes_resource
    ON scope_nodes (org_id, resource_type, resource_id)
    WHERE resource_id IS NOT NULL;

-- ── 2. Hierarchical role grants (closes gaps 1 & 5) ──────────────────────
-- "principal (or team) holds role at this scope node." A grant at an ancestor
-- node inherits to the whole subtree via the @> ancestor test in CheckAccess.
-- role_id reuses the existing roles/role_permissions machinery, so a grant's
-- capabilities resolve through the same (resource, action) rows as flat RBAC.
CREATE TABLE IF NOT EXISTS scope_grants (
    id            UUID  DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id        UUID  NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    subject_id    UUID  NOT NULL,                 -- principal id OR team id
    subject_kind  TEXT  NOT NULL CHECK (subject_kind IN ('principal', 'team')),
    scope_path    LTREE NOT NULL,
    role_id       UUID  NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    granted_by    UUID  REFERENCES principals(id),
    expires_at    TIMESTAMPTZ,                     -- NULL = standing
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, subject_id, subject_kind, scope_path, role_id)
);
CREATE INDEX IF NOT EXISTS idx_scope_grants_subject
    ON scope_grants (org_id, subject_id, subject_kind);
CREATE INDEX IF NOT EXISTS idx_scope_grants_path
    ON scope_grants USING GIST (org_id, scope_path);

-- ── 3. Per-record share overlay (closes gap 2) ───────────────────────────
-- "subject may act on THIS specific record, across the ownership boundary."
-- resource_type is the same vocabulary as role_permissions.resource. resource_id
-- is opaque to the starter (the product's row id). This is the durable ACL that
-- delegation_grants (ephemeral JIT elevation) deliberately is not. Additive
-- only: a share grants access, never removes anyone else's (no per-record deny).
CREATE TABLE IF NOT EXISTS record_shares (
    id            UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    subject_id    UUID NOT NULL,
    subject_kind  TEXT NOT NULL CHECK (subject_kind IN ('principal', 'team')),
    role_id       UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    granted_by    UUID REFERENCES principals(id),
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, resource_type, resource_id, subject_id, subject_kind, role_id)
);
CREATE INDEX IF NOT EXISTS idx_record_shares_subject
    ON record_shares (org_id, subject_id, subject_kind);
CREATE INDEX IF NOT EXISTS idx_record_shares_resource
    ON record_shares (org_id, resource_type, resource_id);

-- ── RLS: tenant floor only (subject filtering is in CheckAccess) ─────────
ALTER TABLE scope_nodes   ENABLE ROW LEVEL SECURITY;
ALTER TABLE scope_nodes   FORCE  ROW LEVEL SECURITY;
ALTER TABLE scope_grants  ENABLE ROW LEVEL SECURITY;
ALTER TABLE scope_grants  FORCE  ROW LEVEL SECURITY;
ALTER TABLE record_shares ENABLE ROW LEVEL SECURITY;
ALTER TABLE record_shares FORCE  ROW LEVEL SECURITY;

CREATE POLICY scope_nodes_tenant ON scope_nodes
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
CREATE POLICY scope_grants_tenant ON scope_grants
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
CREATE POLICY record_shares_tenant ON record_shares
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- ── Exact grants (migration 92 convention, per DATABASE_AUTHORITY.md) ─────
-- Request traffic manages its own org's scope tree, grants, and shares
-- (create/read/update/revoke), so app_tenant gets the full DML set. The
-- control plane holds the same set for cross-tenant maintenance.
REVOKE ALL PRIVILEGES ON scope_nodes, scope_grants, record_shares FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_nodes   TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_grants  TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON record_shares TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_nodes   TO app_control_plane;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_grants  TO app_control_plane;
GRANT SELECT, INSERT, UPDATE, DELETE ON record_shares TO app_control_plane;
