-- Dropping the column discards every solution's boundary seed. Re-applying the
-- migration draws new ones, so a run still executing under a boundary derived
-- from an old seed becomes unreachable from any page (issue #1015; stated in
-- module/SOLUTION_REGISTRATION.md §6). There is nothing to preserve it with:
-- the seed is the only input the derivation has.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT IF EXISTS solution_registrations_runtime_boundary_key;

ALTER TABLE public.solution_registrations DROP COLUMN IF EXISTS runtime_boundary;
