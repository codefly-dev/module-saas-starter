-- Installation identity (issue #474): the composition that lets a solution act
-- as itself with signed, attributable, revocable authority — the "application
-- permission" half of the model beside delegated ("on behalf of a user") flows.
--
-- An installation binds four things that already exist independently but nothing
-- composed: the per-org agent Principal (the actor identity, migration 36 + the
-- ceiling of migration 108), a solution scope node (the authority root, migration
-- 98), the agent's standing scope grant at that node, and — new here — an OWNER
-- OF RECORD: a reassignable, accountable human org admin. principals.created_by is
-- authorship, not accountability; when the human who connected a solution leaves
-- the org, authorship cannot answer "who is accountable now". owner_principal_id
-- and co_owner_principal_ids do.
--
-- installations is a TENANT relation: forced RLS on app.current_org_id, exact
-- app_tenant/app_control_plane grants, matching the scope_nodes/scope_grants
-- convention of migration 98.

CREATE TABLE IF NOT EXISTS installations (
    id                    UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id                UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- The agent Principal that acts under this installation. Principals are never
    -- hard-deleted (they are revoked), so no cascade — a revoked principal must
    -- still resolve for the fail-closed mint path and the audit trail.
    agent_principal_id    UUID NOT NULL REFERENCES principals(id),
    solution_identifier   TEXT NOT NULL,
    -- The accountable human of record. An org admin at install time; kept current
    -- by TransferInstallationOwnership, never silently reassigned. Co-owners are a
    -- succession set: any one of them being a current org admin keeps the
    -- installation healthy and eligible to mint.
    owner_principal_id    UUID NOT NULL REFERENCES principals(id),
    co_owner_principal_ids UUID[] NOT NULL DEFAULT '{}',
    -- The authority root: a kind='solution' scope node. The agent's standing grant
    -- lives at this node and every boundary a headless task writes to is a
    -- descendant of it.
    root_scope_node_id    UUID NOT NULL REFERENCES scope_nodes(id),
    status                TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at            TIMESTAMPTZ,
    -- At most one active installation per (org, solution): re-installing the same
    -- solution is idempotent, not a second row. Revoked rows are left out so a
    -- solution can be reinstalled after an uninstall.
    CONSTRAINT installations_status_revoked_consistency
        CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_installations_active_solution
    ON installations (org_id, solution_identifier)
    WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_installations_agent
    ON installations (org_id, agent_principal_id);

ALTER TABLE installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE installations FORCE  ROW LEVEL SECURITY;

CREATE POLICY installations_tenant ON installations
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact grants (migration 92/98 convention). Request traffic manages its own
-- org's installations (install/read/transfer/revoke), so app_tenant gets the
-- full DML set; the control plane holds the same for cross-tenant maintenance.
REVOKE ALL PRIVILEGES ON installations FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON installations TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON installations TO app_control_plane;
