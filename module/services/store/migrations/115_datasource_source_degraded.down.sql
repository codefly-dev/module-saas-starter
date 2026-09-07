-- Restore any degraded source to active before narrowing the CHECK back, so the
-- constraint re-add does not fail on existing rows.
UPDATE datasource_sources SET status = 'active' WHERE status = 'degraded';

ALTER TABLE datasource_sources
    DROP COLUMN status_reason,
    DROP CONSTRAINT datasource_sources_status_check,
    ADD CONSTRAINT datasource_sources_status_check
        CHECK (status IN ('active', 'paused'));
