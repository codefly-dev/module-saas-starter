-- Phase 2D — RLS on audit_events.
--
-- audit_events has nullable org_id: tenant-scoped events have a
-- concrete org_id, while system events (e.g. cron, gdpr ops, login
-- failures with no resolved org) have org_id IS NULL.
--
-- Policy: rows visible iff (org_id matches current_setting) OR bypass.
-- NULL-org rows are visible ONLY under bypass — they're inherently
-- cross-tenant in nature (system / audit-readers / GDPR exports) and
-- writers (the AsyncAuditEmitter goroutine) opt into bypass when
-- entry.OrgID is empty.
--
-- WITH CHECK has the same shape, so a per-tenant write path
-- (WithOrgTx) cannot insert a row carrying the wrong org_id; the
-- system-events path (WithBypass) can write any org_id including
-- NULL.

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_events_tenant ON audit_events
    USING (
        current_setting('app.bypass', true) = '1'
        OR (org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true))
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR (org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true))
    );
