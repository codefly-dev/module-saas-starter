-- Rollback removes canonical setup delivery support; pending canonical attempts
-- must not be served by the older host. It does not turn them into legacy installs.
CREATE OR REPLACE FUNCTION public.installation_step_job_current(job uuid, stamp jsonb)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT EXISTS (SELECT 1 FROM public.installations i
      WHERE i.install_job_id=job AND i.id::text=stamp->>'codefly.installation_id'
        AND i.org_id::text=stamp->>'codefly.organization_id'
        AND i.revision::text=stamp->>'codefly.installation_revision'
        AND i.status IN ('pending','active'));
$$;

DROP TRIGGER record_installation_step ON public.job_messages;
CREATE TRIGGER record_installation_step AFTER UPDATE OF state ON public.job_messages
    FOR EACH ROW WHEN (NEW.state IS DISTINCT FROM OLD.state)
    EXECUTE FUNCTION public.record_installation_step();
DROP TRIGGER installation_no_standing_immutable ON public.installations;
DROP TRIGGER installation_no_standing_grant ON public.scope_grants;
DROP FUNCTION public.installation_no_standing_grant();
DROP FUNCTION public.installation_no_standing_immutable();
DROP FUNCTION public.lock_module_no_standing_declaration(uuid);
ALTER TABLE public.installations DROP CONSTRAINT installation_no_standing_module;
ALTER TABLE public.installations DROP COLUMN no_standing_authority;

