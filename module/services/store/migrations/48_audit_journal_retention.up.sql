-- Retention reads only committed audit delivery receipts. Source-operation
-- responses remain tenant-only, and no control-plane receipt mutation is added.
GRANT SELECT ON public.codefly_effect_receipts TO app_control_plane;
CREATE POLICY audit_delivery_retention_receipts ON public.codefly_effect_receipts
    FOR SELECT TO app_control_plane
    USING (CURRENT_USER = 'app_control_plane'::name
        AND method = '/saas.accounts.v1.AuditService/DeliverAuditEvents');

-- The existing permissive control-plane policy still governs its other
-- commands. A restrictive DELETE policy limits this new grant to old internal
-- audit rows even when another permissive policy also matches the caller.
-- Hours keep the thirty-day age independent of the session's time zone/DST.
GRANT DELETE ON public.domain_events TO app_control_plane;
CREATE POLICY audit_journal_retention_delete ON public.domain_events
    AS RESTRICTIVE FOR DELETE TO app_control_plane
    USING (type = 'saas.audit.recorded.v1'
        AND created_at < statement_timestamp() - interval '720 hours');
