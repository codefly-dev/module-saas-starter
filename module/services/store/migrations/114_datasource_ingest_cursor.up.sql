-- Ingest cursor for GitHub-driven datasources (issue #487). The sync path
-- records only last_synced_at (migration 104), so nothing knows which commit was
-- last turned into a change set. Without that cursor a redelivered, out-of-order,
-- or missed push cannot be told apart from new work, and a force push cannot be
-- reconciled. This adds the cursor the change-set compiler advances once every op
-- of a delivery is durably enqueued, plus the schedule the periodic reconcile
-- reads.
--
-- last_ingested_commit is the head commit fully enqueued as a change set;
-- last_delivery_id is the provenance of the last applied delivery. next_reconcile_at
-- NULL disables the periodic snapshot for the source (reconcile_interval 0 on
-- connect); a non-null value is when the reconcile scheduler next considers it.
--
-- Classification (DATABASE_AUTHORITY.md): datasource_sources stays a TENANT
-- relation with the same RLS policy (migration 104) and the same grants — these
-- are new columns on an existing table, so the table-level GRANTs already cover
-- them. app_tenant reads/writes its own org's rows; app_control_plane already
-- holds UPDATE (migration 104), which the leased change-set/reconcile worker uses
-- to advance the cursor and bump next_reconcile_at without tenant context.

ALTER TABLE datasource_sources
    ADD COLUMN last_ingested_commit TEXT,
    ADD COLUMN last_ingested_at     TIMESTAMPTZ,
    ADD COLUMN last_delivery_id     TEXT,
    ADD COLUMN reconcile_interval   INTERVAL NOT NULL DEFAULT '30 minutes',
    ADD COLUMN next_reconcile_at    TIMESTAMPTZ;

-- The reconcile scheduler selects active sources due for a reconcile ordered by
-- next_reconcile_at; a partial index keeps that sweep cheap and skips paused
-- sources and those with reconcile disabled (next_reconcile_at NULL).
CREATE INDEX idx_datasource_sources_reconcile
    ON datasource_sources (next_reconcile_at)
    WHERE status = 'active';
