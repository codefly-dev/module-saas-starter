DROP INDEX IF EXISTS public.solution_registrations_declared_binding;

ALTER TABLE public.solution_registrations
    DROP CONSTRAINT IF EXISTS solution_registrations_declared_binding_id_check,
    DROP CONSTRAINT IF EXISTS solution_registrations_declared_generation_check,
    DROP CONSTRAINT IF EXISTS solution_registrations_declared_whole;

ALTER TABLE public.solution_registrations
    DROP COLUMN IF EXISTS declared_release,
    DROP COLUMN IF EXISTS declared_generation,
    DROP COLUMN IF EXISTS declared_binding_id;

DROP TABLE IF EXISTS public.solution_host_bindings;
