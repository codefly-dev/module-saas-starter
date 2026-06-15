-- Phase 2C — RLS on principals (the polymorphic identity table).
--
-- principals is mixed-scope: agents/services carry org_id (tenant-scoped);
-- humans carry org_id = NULL (their row id = users.uuid) and are reachable via
-- organization_members. So the policy is a union of three visibilities, plus the
-- explicit system bypass:
--   * app.bypass = '1'                          -- System() — privileged/point-lookup paths
--   * org_id = app.current_org_id               -- an agent/service in the tenant
--   * id = app.current_user_id                  -- a human seeing themselves
--   * a human visible via org membership         -- the ListPrincipals "all kinds" join
--
-- WITH CHECK (writes) omits the membership clause: inserting/altering a row
-- requires acting as its org (agents/services), as the row itself (a human), or
-- System (registration/bootstrap). FORCE so the app role is subject too.

ALTER TABLE principals ENABLE ROW LEVEL SECURITY;
ALTER TABLE principals FORCE  ROW LEVEL SECURITY;

CREATE POLICY principals_access ON principals
    USING (
        current_setting('app.bypass', true) = '1'
        OR org_id::text = current_setting('app.current_org_id', true)
        OR id::text     = current_setting('app.current_user_id', true)
        OR EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.user_id = principals.id
              AND om.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR org_id::text = current_setting('app.current_org_id', true)
        OR id::text     = current_setting('app.current_user_id', true)
    );
