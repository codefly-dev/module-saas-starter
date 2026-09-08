-- Per-source delivery ordinal (issue #511). A datasource module consumes change
-- sets and snapshots off a shared queue and needs to order them per source
-- without trusting wall-clock timestamps or commit topology: a strictly
-- increasing integer stamped on every emitted payload lets a consumer order the
-- payload stream per source and detect a dropped payload. next_ordinal is the
-- next value to hand out; the compiler allocates one atomically (UPDATE ...
-- next_ordinal = next_ordinal + 1 RETURNING) per emitted payload, so ordinals are
-- strictly increasing per source even across concurrent workers. Strictly
-- increasing is the guarantee, not density: a wasted allocation (a crash before
-- the enqueue, or an idempotent re-enqueue on redelivery) leaves a harmless gap,
-- never a repeated or backward ordinal.
--
-- Classification (DATABASE_AUTHORITY.md): a new column on datasource_sources,
-- which stays a TENANT relation with the RLS policy and grants from migration
-- 104 — the table-level GRANTs already cover it. app_control_plane already holds
-- UPDATE (migration 104), which the leased change-set/reconcile worker uses to
-- allocate an ordinal without tenant context, the same path that advances the
-- ingest cursor (migration 114).

ALTER TABLE datasource_sources
    ADD COLUMN next_ordinal BIGINT NOT NULL DEFAULT 1;
