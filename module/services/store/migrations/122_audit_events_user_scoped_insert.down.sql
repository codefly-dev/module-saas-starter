-- Restore the org-only WITH CHECK: NULL-org audit rows become writable by the
-- control plane alone.
ALTER POLICY audit_events_tenant ON audit_events
    USING (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    );
