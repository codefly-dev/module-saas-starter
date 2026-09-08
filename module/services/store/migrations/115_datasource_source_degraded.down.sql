-- Restore any degraded source to active before narrowing the CHECK back, so the
-- constraint re-add does not fail on existing rows. Degrading cleared
-- next_reconcile_at to drop the row from the reconcile sweep; reactivating has to
-- restore that schedule from the source's interval, otherwise a rollback would
-- silently strand every once-degraded source out of reconcile forever with no
-- 'degraded' status left to explain why.
UPDATE datasource_sources
   SET status = 'active',
       next_reconcile_at = CASE WHEN reconcile_interval > INTERVAL '0'
                                THEN NOW() + reconcile_interval END
 WHERE status = 'degraded';

ALTER TABLE datasource_sources
    DROP COLUMN status_reason,
    DROP CONSTRAINT datasource_sources_status_check,
    ADD CONSTRAINT datasource_sources_status_check
        CHECK (status IN ('active', 'paused'));
