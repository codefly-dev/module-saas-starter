-- Restores the schema shape the baseline left, and nothing more.
--
-- Said plainly, because a down migration that looks complete and is not is worse
-- than one that says so: the route alias each installation named is NOT
-- recoverable here. It was dropped with the column, and the only record of which
-- alias a target served is the target row, whose alias may have moved since. So
-- `solution_identifier` comes back empty for every row, and the status this
-- migration revoked stays revoked.
--
-- That is the honest consequence of a destructive cutover, and it is why the
-- forward direction records `revoked_reason`: the reverse cannot reconstruct
-- consent, so the forward direction has to leave evidence of what it ended.

ALTER TABLE public.solution_registrations
    DROP CONSTRAINT IF EXISTS solution_registrations_declared_whole;
ALTER TABLE public.solution_registrations
    DROP COLUMN IF EXISTS declared_target_id;
ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_declared_whole
        CHECK ((num_nonnulls(declared_binding_id, declared_generation, declared_release) = ANY (ARRAY[0, 3])));

DROP TRIGGER IF EXISTS installations_target_live_on_update ON public.installations;
DROP TRIGGER IF EXISTS installations_target_live_on_insert ON public.installations;
DROP FUNCTION IF EXISTS public.installations_target_must_be_live();

DROP TRIGGER IF EXISTS installations_target_immutable ON public.installations;
DROP FUNCTION IF EXISTS public.installations_target_is_immutable();

DROP INDEX IF EXISTS public.idx_installations_target;
DROP INDEX IF EXISTS public.idx_installations_active_target;

ALTER TABLE public.installations
    DROP CONSTRAINT IF EXISTS installations_revoked_reason_only_when_revoked,
    DROP CONSTRAINT IF EXISTS installations_active_requires_target,
    DROP CONSTRAINT IF EXISTS installations_target_id_fkey;

ALTER TABLE public.installations
    ADD COLUMN solution_identifier text NOT NULL DEFAULT '';

ALTER TABLE public.installations
    ALTER COLUMN solution_identifier DROP DEFAULT;

ALTER TABLE public.installations
    DROP COLUMN revoked_reason,
    DROP COLUMN target_id;

CREATE UNIQUE INDEX idx_installations_active_solution
    ON public.installations USING btree (org_id, solution_identifier)
    WHERE (status = 'active'::text);
