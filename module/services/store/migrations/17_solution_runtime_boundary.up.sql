-- The host assigns a solution's runtime boundary (issue #1015).
--
-- A runtime task's boundary is the task_id of the Work Context that admitted
-- it. Every Work Context minted for a registered solution is sealed under the
-- boundary stored here, so a run a page admits stays reachable across the mints
-- of one session instead of only under the context that admitted it.
--
-- The column is the whole safety property. The host assigns the value: the
-- DEFAULT fires on INSERT, the registry's upsert never lists this column in its
-- ON CONFLICT ... DO UPDATE, and no request field reaches it. So a solution can
-- neither choose its own boundary nor replace it with another solution's, and
-- the value survives a tombstone — a reactivated registration keeps naming the
-- runs it already admitted.
--
-- Existing rows take a fresh boundary each, exactly as a new row would: nothing
-- that ran before this migration was reachable under a stable boundary anyway,
-- so there is no prior value to preserve.
ALTER TABLE public.solution_registrations
    ADD COLUMN runtime_boundary uuid DEFAULT gen_random_uuid() NOT NULL;
