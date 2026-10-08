-- Drop the runtime-boundary seed column (issue #952).
--
-- `runtime_boundary` was a PER-REGISTRATION random (migration
-- `17_solution_runtime_boundary`, `DEFAULT gen_random_uuid()`), and that is the
-- wrong lifetime for a boundary: a solution withdrawn and registered afresh drew
-- a new one and orphaned every run filed under the old, while a binding that
-- never moved could have its seed replaced by a write it did not make. The
-- boundary is now derived from the DELIVERED PRESENCE BINDING — which survives
-- re-registration and is terminal with its tombstone — per organization
-- (`business.SolutionRuntimeBoundary`, a UUIDv5 of the binding id and the org
-- id). Nothing reads this column: the seed read selects `declared_binding_id`
-- and the collision check reads binding ids.
--
-- The UNIQUE constraint goes with it. It existed to keep two registrations from
-- sharing a seed, so that the mint's refusal of a caller-named task_id was
-- unambiguous about which solution it protected. Binding ids are unique by
-- `solution_targets` identity, so the property it bought is now a property of
-- the thing being hashed.
--
-- Dropping the column also drops the constraint; both statements are written out
-- so that a reader of the ledger sees the index leave, and `IF EXISTS` keeps the
-- file idempotent against a database where migration 17's own down had run.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT IF EXISTS solution_registrations_runtime_boundary_key;

ALTER TABLE public.solution_registrations
    DROP COLUMN IF EXISTS runtime_boundary;
