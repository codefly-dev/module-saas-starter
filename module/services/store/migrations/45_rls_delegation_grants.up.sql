-- Phase 2C — RLS on delegation_grants (tenant-scoped: org_id NOT NULL).
-- Visible/writable only when acting in the grant's org, or System bypass.

ALTER TABLE delegation_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE delegation_grants FORCE  ROW LEVEL SECURITY;

CREATE POLICY delegation_grants_tenant ON delegation_grants
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );
