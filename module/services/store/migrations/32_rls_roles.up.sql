-- Phase 2E — RLS on roles + role_assignments.
--
-- Both have nullable org_id (built-in roles have org_id IS NULL,
-- tenant-scoped custom roles have a concrete org_id). Policy is
-- polymorphic:
--
--   * SELECT: built-in (NULL) rows are globally visible — they're
--     the standard menu of available roles. Tenant rows visible
--     only when their org matches the current setting (or bypass).
--   * INSERT/UPDATE/DELETE: WITH CHECK requires org match — built-in
--     rows can only be written under bypass (migrations seed them
--     under superuser, which inherits bypass via WithBypass).
--
-- This shape mirrors the schema's intent: built-ins are read by
-- everyone, written by no-one (except migrations).
--
-- For role_assignments, the same policy fires on each row. Built-in
-- roles get assigned with concrete org_id (the user's org), so most
-- assignments are tenant-scoped; some platform-level assignments
-- (e.g. super_admin) may use NULL org_id and require bypass.

ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE roles FORCE  ROW LEVEL SECURITY;

CREATE POLICY roles_polymorphic ON roles
    USING (
        current_setting('app.bypass', true) = '1'
        OR org_id IS NULL
        OR org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR (org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true))
    );

ALTER TABLE role_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_assignments FORCE  ROW LEVEL SECURITY;

CREATE POLICY role_assignments_polymorphic ON role_assignments
    USING (
        current_setting('app.bypass', true) = '1'
        OR org_id IS NULL
        OR org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR (org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true))
    );

-- Note: role_permissions is NOT RLS-protected. It's a child table
-- (role_id FK) of roles; the JOIN-via-role policy is enforced
-- transitively when callers read through role_permissions JOIN
-- roles. Direct reads against role_permissions alone are safe
-- because the resource:action pairs are not tenant-secret data
-- (an attacker who learned that "editor" has "users:write" learns
-- nothing about another tenant). The N+1 risk avoided.
