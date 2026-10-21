-- Presentation copied from applied presence, including headless solutions.
-- Group membership has no relation to installation targets, grants or scopes.
ALTER TABLE public.solution_registrations
    ADD COLUMN solution_group jsonb,
    ADD CONSTRAINT solution_registrations_group_is_presentation
        CHECK (solution_group IS NULL OR
               (declared_kind = 'solution' AND tombstoned_at IS NULL AND
                jsonb_typeof(solution_group) = 'object'));
