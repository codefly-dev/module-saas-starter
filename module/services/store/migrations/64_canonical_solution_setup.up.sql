-- A canonical solution setup job is linked to its shared consent receipt.
-- This is a job-custody predicate, not authority to activate the installation.
CREATE OR REPLACE FUNCTION public.installation_step_job_current(job uuid, stamp jsonb)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT CASE WHEN stamp->>'canonical.installation' = 'true' THEN
      EXISTS (SELECT 1 FROM public.authority_core_installations i
        WHERE i.id=stamp->>'codefly.installation_id'
          AND i.organization_id::text=stamp->>'codefly.organization_id'
          AND i.revision::text=stamp->>'codefly.installation_revision'
          AND i.status='pending'
          AND i.consent->>'OperationID'=stamp->>'canonical.operation'
          AND i.consent->>'Intent'=stamp->>'canonical.intent'
          AND i.consent->'host_plan'->>'SetupJobID'=job::text)
    ELSE
      EXISTS (SELECT 1 FROM public.installations i
        WHERE i.install_job_id=job AND i.id::text=stamp->>'codefly.installation_id'
          AND i.org_id::text=stamp->>'codefly.organization_id'
          AND i.revision::text=stamp->>'codefly.installation_revision'
          AND i.status IN ('pending','active'))
    END;
$$;

-- Durable success stays on the job. Only Authority Install, after its final
-- serializable prerequisite check, may activate a canonical solution.
DROP TRIGGER record_installation_step ON public.job_messages;
CREATE TRIGGER record_installation_step AFTER UPDATE OF state ON public.job_messages
    FOR EACH ROW WHEN (NEW.state IS DISTINCT FROM OLD.state
        AND COALESCE(NEW.attributes->>'canonical.installation','') <> 'true')
    EXECUTE FUNCTION public.record_installation_step();
-- Declared no-standing authority is consent metadata, not an empty grant.
-- It is immutable for this installation; changing machine authority requires
-- a separately reviewed lifecycle change, never a marker update.
ALTER TABLE public.installations ADD COLUMN no_standing_authority boolean NOT NULL DEFAULT false;
ALTER TABLE public.installations ADD CONSTRAINT installation_no_standing_module CHECK (NOT no_standing_authority OR kind='module');

CREATE FUNCTION public.lock_module_no_standing_declaration(target uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE selected boolean;
BEGIN
    PERFORM public.lock_installation_approval_target(target);
    SELECT m.declarations->>'installation_role'='none' INTO selected
      FROM public.solution_targets t JOIN public.solution_host_bindings b ON b.binding_id=t.binding_id
      JOIN public.authority_core_modules m ON m.name=t.solution_id
      WHERE t.id=target AND t.closed_generation IS NULL AND NOT b.applied_removed
        AND m.kind='module' AND m.generation=b.applied_generation FOR SHARE OF m;
    RETURN COALESCE(selected,false);
END;
$$;
REVOKE ALL ON FUNCTION public.lock_module_no_standing_declaration(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.lock_module_no_standing_declaration(uuid) TO app_tenant, app_control_plane;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.lock_module_no_standing_declaration(uuid) OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;

CREATE FUNCTION public.installation_no_standing_immutable() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER SET search_path = public, pg_temp AS $$
BEGIN
    IF NEW.no_standing_authority IS DISTINCT FROM OLD.no_standing_authority THEN
        RAISE EXCEPTION 'installation machine selection is immutable' USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER installation_no_standing_immutable BEFORE UPDATE ON public.installations
    FOR EACH ROW EXECUTE FUNCTION public.installation_no_standing_immutable();

CREATE FUNCTION public.installation_no_standing_grant() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER SET search_path = public, pg_temp AS $$
BEGIN
    IF NEW.subject_kind='principal' AND EXISTS (
      SELECT 1 FROM public.installations i JOIN public.scope_nodes n ON n.id=i.root_scope_node_id
      WHERE i.org_id=NEW.org_id AND i.agent_principal_id=NEW.subject_id
        AND i.no_standing_authority AND i.status <> 'revoked' AND n.scope_path=NEW.scope_path
    ) THEN
        RAISE EXCEPTION 'the installation declares no standing machine grant' USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER installation_no_standing_grant BEFORE INSERT OR UPDATE ON public.scope_grants
    FOR EACH ROW EXECUTE FUNCTION public.installation_no_standing_grant();

