ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_group_is_presentation,
    DROP COLUMN solution_group;
