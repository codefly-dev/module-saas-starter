-- The host assigns a solution's runtime boundary (issue #1015).
--
-- A runtime task's boundary is the task_id of the Work Context that admitted
-- it. Every Work Context minted for a registered solution is sealed under a
-- boundary derived from the seed stored here, so a run a page admits stays
-- reachable across the mints of one session instead of only under the context
-- that admitted it.
--
-- This column is the SEED, not the boundary itself. The minted boundary is
-- derived per organization (business.SolutionRuntimeBoundary, a UUIDv5 of the
-- seed and the org id), so one tenant's boundary is not another's and the seed
-- never leaves this host. Two solutions must never share a seed, which is what
-- UNIQUE enforces: it is also the index the mint's collision check reads, and
-- that check is what stops any other caller naming a boundary as its own
-- task_id.
--
-- The host assigns the value: the DEFAULT fires on INSERT, the registry's
-- upsert never lists this column in its ON CONFLICT ... DO UPDATE, and no
-- request field reaches it. So a solution can neither choose its own seed nor
-- replace it with another solution's, and the value survives a tombstone — a
-- reactivated registration keeps naming the runs it already admitted.
--
-- Two consequences an operator must know. Deleting the row and registering
-- afresh draws a NEW seed, so every run still executing under the old boundary
-- becomes unreachable from a page; so does the down migration below. And runs
-- admitted before this migration were reachable only under their own admitting
-- context — none of them had a boundary that outlived it — so nothing that was
-- reachable becomes unreachable here. Both are stated in
-- module/SOLUTION_REGISTRATION.md §5 and §6.
ALTER TABLE public.solution_registrations
    ADD COLUMN runtime_boundary uuid DEFAULT gen_random_uuid() NOT NULL;

-- Distinctness is a safety property, not hygiene: the mint refuses a
-- caller-named task_id that matches any stored seed, and two registrations
-- sharing one would make that refusal ambiguous about which solution it
-- protected.
ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_runtime_boundary_key UNIQUE (runtime_boundary);
