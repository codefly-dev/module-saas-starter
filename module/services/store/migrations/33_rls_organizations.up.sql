-- Phase 2F — RLS on organizations.
--
-- This is the most subtle of the per-tenant tables: the row IS the
-- tenant. The natural policy compares the row's id (not org_id) to
-- the current setting. Self-referential tenant scope.
--
-- Cross-tenant readers that legitimately exist:
--   * `ListOrganizationsForUser` — used in the org switcher; reads
--     across all orgs the user is a member of. WithBypass.
--   * Bootstrap `CreateOrganization` — the org doesn't exist when
--     we INSERT it, so app.current_org_id can't be set first.
--     Already wrapped in WithBypass (see service.go).
--   * Platform admin views — already use bypass.
--
-- Per-tenant readers (the request path's GetOrganization for the
-- caller's own org) run inside WithOrgTx with id=current setting.
--
-- Policy:
--   * SELECT: id matches OR bypass. Other orgs are physically
--     invisible.
--   * INSERT/UPDATE/DELETE: same. Inserts run under bypass; updates
--     run under WithOrgTx scoped to the org being updated.

ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE organizations FORCE  ROW LEVEL SECURITY;

CREATE POLICY organizations_self ON organizations
    USING (
        current_setting('app.bypass', true) = '1'
        OR id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR id::text = current_setting('app.current_org_id', true)
    );
