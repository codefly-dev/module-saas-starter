-- Per-source delivery ordinal (issue #511). A datasource module consumes change
-- sets and snapshots off a shared queue and needs to order them per source
-- without trusting wall-clock timestamps or commit topology: a strictly
-- increasing integer stamped on every emitted payload lets a consumer order the
-- payload stream per source and reject a stale or out-of-order replay (an ordinal
-- at or below the highest it has already applied). next_ordinal is the next value
-- to hand out; the compiler allocates one atomically (UPDATE ...
-- next_ordinal = next_ordinal + 1 RETURNING) per emitted payload, so ordinals are
-- strictly increasing per source even across concurrent workers. Strictly
-- increasing is the ONLY guarantee, not contiguity: a wasted allocation leaves a
-- gap — a crash before the enqueue, or an idempotent re-enqueue on redelivery,
-- which keeps the first payload with its original ordinal and discards the
-- retry's freshly-drawn one. Because retries make gaps routine, a consumer must
-- NOT read a gap as a dropped payload; only a repeated or backward ordinal would
-- signal a fault, and the allocating UPDATE's row lock makes neither possible.
--
-- Classification (DATABASE_AUTHORITY.md): a new column on datasource_sources,
-- which stays a TENANT relation with the RLS policy and grants from migration
-- 104 — the table-level GRANTs already cover it. app_control_plane already holds
-- UPDATE (migration 104), which the leased change-set/reconcile worker uses to
-- allocate an ordinal without tenant context, the same path that advances the
-- ingest cursor (migration 114).

ALTER TABLE datasource_sources
    ADD COLUMN next_ordinal BIGINT NOT NULL DEFAULT 1;
