-- Rollback removes canonical setup delivery support; pending canonical attempts
-- must not be served by the older host. It does not turn them into legacy installs.
-- Owned by app_control_plane (migration 36): only its owner may replace it.
GRANT CREATE ON SCHEMA public TO app_control_plane;
SET LOCAL ROLE app_control_plane;
CREATE OR REPLACE FUNCTION public.installation_step_job_current(job uuid, stamp jsonb)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT EXISTS (SELECT 1 FROM public.installations i
      WHERE i.install_job_id=job AND i.id::text=stamp->>'codefly.installation_id'
        AND i.org_id::text=stamp->>'codefly.organization_id'
        AND i.revision::text=stamp->>'codefly.installation_revision'
        AND i.status IN ('pending','active'));
$$;
RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;

-- The trigger is the table owner's; its function is app_control_plane's, so
-- the migrator holds EXECUTE on it only while it recreates the trigger.
SET LOCAL ROLE app_control_plane;
GRANT EXECUTE ON FUNCTION public.record_installation_step() TO SESSION_USER;
RESET ROLE;
DROP TRIGGER record_installation_step ON public.job_messages;
CREATE TRIGGER record_installation_step AFTER UPDATE OF state ON public.job_messages
    FOR EACH ROW WHEN (NEW.state IS DISTINCT FROM OLD.state)
    EXECUTE FUNCTION public.record_installation_step();
SET LOCAL ROLE app_control_plane;
REVOKE EXECUTE ON FUNCTION public.record_installation_step() FROM SESSION_USER;
RESET ROLE;
DROP TRIGGER installation_no_standing_immutable ON public.installations;
DROP TRIGGER installation_no_standing_grant ON public.scope_grants;
DROP FUNCTION public.installation_no_standing_grant();
DROP FUNCTION public.installation_no_standing_immutable();
SET LOCAL ROLE app_control_plane;
DROP FUNCTION public.lock_module_no_standing_declaration(uuid);
RESET ROLE;
ALTER TABLE public.installations DROP CONSTRAINT installation_no_standing_module;
ALTER TABLE public.installations DROP COLUMN no_standing_authority;

