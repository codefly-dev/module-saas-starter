-- Reversible: the kind is a copy of a field in the applied document, which is
-- stored whole and canonically on solution_host_bindings, so dropping the column
-- discards no fact this host cannot read again. Re-applying recovers it from the
-- next generation of every binding, and the four-column whole-or-absent
-- constraint migration 23 defined is what the registry had before.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT IF EXISTS solution_registrations_declared_kind_check;

ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_declared_whole;

ALTER TABLE public.solution_registrations
    DROP COLUMN declared_kind;

ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_declared_whole
        CHECK ((num_nonnulls(declared_binding_id, declared_generation, declared_release, declared_target_id) = ANY (ARRAY[0, 4])));
