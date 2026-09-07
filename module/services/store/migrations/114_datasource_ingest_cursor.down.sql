-- Reverse 114_datasource_ingest_cursor.

DROP INDEX IF EXISTS idx_datasource_sources_reconcile;

ALTER TABLE datasource_sources
    DROP COLUMN IF EXISTS last_ingested_commit,
    DROP COLUMN IF EXISTS last_ingested_at,
    DROP COLUMN IF EXISTS last_delivery_id,
    DROP COLUMN IF EXISTS reconcile_interval,
    DROP COLUMN IF EXISTS next_reconcile_at;
