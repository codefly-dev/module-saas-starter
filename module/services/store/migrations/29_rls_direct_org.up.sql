-- Phase 2B — RLS on the remaining direct-org_id tables.
--
-- All six tables here use a non-nullable org_id and have Service
-- methods that take orgID directly — same simple recipe as
-- audit_export_configs.
--
-- Tables: org_settings, invitations, organization_members,
-- subscriptions, entitlement_overrides, usage_records.
--
-- Skipped (need follow-up migrations):
--   - teams + team_members (direct + JOIN)
--   - audit_events, roles, role_assignments, notifications
--     (nullable org_id; need polymorphic policies for built-in /
--      global rows)
--   - api_keys (already handled in migration 28)

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'org_settings',
        'invitations',
        'organization_members',
        'subscriptions',
        'entitlement_overrides',
        'usage_records'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %I_tenant ON %I
                USING (
                    org_id::text = current_setting('app.current_org_id', true)
                    OR current_setting('app.bypass', true) = '1'
                )
                WITH CHECK (
                    org_id::text = current_setting('app.current_org_id', true)
                    OR current_setting('app.bypass', true) = '1'
                )
        $f$, t, t);
    END LOOP;
END $$;
