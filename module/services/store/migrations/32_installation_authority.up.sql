-- Consent points to the durable registry binding, not its reusable route alias.
-- target_id retains the continuous-presence identity (migration 23). Principal
-- is the deterministic ModulePrincipalID from delivered authority, NOT the
-- per-installation agent. No new registry or synthetic principal rows are made.
ALTER TABLE public.installations
    ADD COLUMN kind text NOT NULL DEFAULT 'solution' CHECK (kind IN ('module', 'solution')),
    ADD COLUMN registry_binding_id text REFERENCES public.solution_host_bindings(binding_id),
    ADD COLUMN registry_principal_id uuid,
    ADD COLUMN scope jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(scope) = 'object'),
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);

-- Old rows granted agent authority, not module capability authority. Empty scope
-- is the only provable backfill. Never infer consent from the current envelope.
ALTER TABLE public.installations NO FORCE ROW LEVEL SECURITY;
UPDATE public.installations i
   SET registry_binding_id = t.binding_id,
       kind = COALESCE(r.declared_kind, 'solution')
  FROM public.solution_targets t
  LEFT JOIN public.solution_registrations r ON r.declared_target_id = t.id
 WHERE t.id = i.target_id;
ALTER TABLE public.installations FORCE ROW LEVEL SECURITY;

ALTER TABLE public.solution_targets ADD CONSTRAINT solution_targets_id_binding_unique UNIQUE (id, binding_id);
ALTER TABLE public.installations
    ADD CONSTRAINT installations_registry_target_fkey FOREIGN KEY (target_id, registry_binding_id)
        REFERENCES public.solution_targets(id, binding_id),
    ADD CONSTRAINT installations_active_registry CHECK (status <> 'active' OR registry_binding_id IS NOT NULL);
CREATE UNIQUE INDEX installations_active_principal ON public.installations(org_id, registry_principal_id)
    WHERE status = 'active' AND registry_principal_id IS NOT NULL;

-- The store also serves agent-only installs. Derive the registry reference
-- from their target, while leaving principal NULL and scope empty. This grants
-- no module authority and is not a guess at a module principal.
CREATE FUNCTION public.installation_registry_on_insert() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE target_binding text; target_kind text;
BEGIN
    SELECT t.binding_id, r.declared_kind INTO target_binding, target_kind
    FROM public.solution_targets t LEFT JOIN public.solution_registrations r ON r.declared_target_id=t.id
    WHERE t.id=NEW.target_id;
    IF NEW.registry_binding_id IS NULL THEN NEW.registry_binding_id := target_binding; END IF;
    IF target_kind IS NOT NULL AND NEW.kind <> target_kind THEN
        RAISE EXCEPTION 'installation kind does not match the registry' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER installation_registry_on_insert BEFORE INSERT ON public.installations
    FOR EACH ROW EXECUTE FUNCTION public.installation_registry_on_insert();

CREATE FUNCTION public.installation_authority_revision() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER SET search_path = public, pg_temp AS $$
BEGIN
    IF NEW.org_id IS DISTINCT FROM OLD.org_id OR NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.registry_binding_id IS DISTINCT FROM OLD.registry_binding_id
       OR NEW.registry_principal_id IS DISTINCT FROM OLD.registry_principal_id THEN
        RAISE EXCEPTION 'installation registry identity and organization are immutable' USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.status = 'revoked' AND NEW.status <> 'revoked' THEN
        RAISE EXCEPTION 'revoked installation cannot be reactivated; install again' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.scope IS DISTINCT FROM OLD.scope OR NEW.status IS DISTINCT FROM OLD.status THEN
        NEW.revision := OLD.revision + 1;
    ELSIF NEW.revision <> OLD.revision THEN
        RAISE EXCEPTION 'installation revision is lifecycle-owned' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER installation_authority_revision BEFORE UPDATE ON public.installations
    FOR EACH ROW EXECUTE FUNCTION public.installation_authority_revision();

-- Consumer authority survives retries and expired leases. It is not producer
-- provenance, and cannot be supplied via caller-controlled job attributes.
ALTER TABLE public.job_messages ADD COLUMN consumer_authority jsonb;

-- Revocation must fence already leased work as well as queued work.
CREATE OR REPLACE FUNCTION public.job_state_transition_is_valid(from_state text, to_state text) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path = pg_catalog AS $$
 SELECT CASE from_state
 WHEN 'pending' THEN to_state IN ('processing','canceled')
 WHEN 'processing' THEN to_state IN ('retrying','succeeded','dead_letter','canceled')
 WHEN 'retrying' THEN to_state IN ('processing','canceled') ELSE FALSE END;
$$;

-- Runs with ONLY the worker's job rights. OLD/NEW come from the tenant-scoped
-- installation trigger; there is no callable cancellation accepting arbitrary
-- tenant selectors. Processing leases are fenced and ordering keys released.
CREATE FUNCTION public.cancel_installation_work() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
    IF NEW.revision = OLD.revision THEN RETURN NEW; END IF;
    WITH canceled AS (
        UPDATE public.job_messages
           SET state = 'canceled', completed_at = clock_timestamp(),
               lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL, heartbeat_at = NULL,
               last_error_code = 'revoked_authority', last_error_message = 'installation authority changed'
         WHERE ((attributes->>'codefly.installation_id' = OLD.id::text
           AND attributes->>'codefly.organization_id' = OLD.org_id::text)
           OR (consumer_authority->>'codefly.installation_id' = OLD.id::text
           AND consumer_authority->>'codefly.organization_id' = OLD.org_id::text))
           AND state IN ('pending', 'processing', 'retrying')
         RETURNING id
    )
    UPDATE public.job_attempts a SET finished_at = clock_timestamp(), outcome = 'canceled',
        error_code = 'revoked_authority', error_message = 'installation authority changed'
      FROM canceled c WHERE a.job_id = c.id AND a.finished_at IS NULL;
    RETURN NEW;
END;
$$;
CREATE TRIGGER cancel_installation_work AFTER UPDATE ON public.installations
    FOR EACH ROW EXECUTE FUNCTION public.cancel_installation_work();

-- Exact, bounded predicates for the isolated worker and transactional producer;
-- no job role receives SELECT over tenant installations or the envelope.
CREATE FUNCTION public.module_job_authority_current(stamp jsonb) RETURNS boolean
LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT EXISTS (
        SELECT 1 FROM public.installations i
         WHERE i.id::text = stamp->>'codefly.installation_id'
           AND i.org_id::text = stamp->>'codefly.organization_id'
           AND i.registry_principal_id::text = stamp->>'codefly.principal_id'
           AND i.registry_binding_id = stamp->>'codefly.binding_id'
           AND i.revision::text = stamp->>'codefly.installation_revision'
           AND i.status = 'active' AND i.scope->>'capabilities' = 'true'
           AND (current_setting('role', true) = 'app_job_worker' OR (
                current_setting('role', true) = 'app_tenant' AND (
                    i.org_id::text = NULLIF(current_setting('app.current_org_id', true), '') OR
                    EXISTS (SELECT 1 FROM public.organization_members m
                      WHERE m.org_id = i.org_id
                        AND m.user_id::text = NULLIF(current_setting('app.current_user_id', true), '')))))
    );
$$;

CREATE FUNCTION public.lock_module_job_authority(stamp jsonb) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
    PERFORM 1 FROM public.installations i
     WHERE i.id::text = stamp->>'codefly.installation_id'
       AND i.org_id::text = stamp->>'codefly.organization_id'
       AND i.registry_principal_id::text = stamp->>'codefly.principal_id'
       AND i.registry_binding_id = stamp->>'codefly.binding_id'
       AND i.revision::text = stamp->>'codefly.installation_revision'
       AND i.status = 'active' AND i.scope->>'capabilities' = 'true'
           AND (current_setting('role', true) = 'app_job_worker' OR (
                current_setting('role', true) = 'app_tenant' AND (
                    i.org_id::text = NULLIF(current_setting('app.current_org_id', true), '') OR
                    EXISTS (SELECT 1 FROM public.organization_members m
                      WHERE m.org_id = i.org_id
                        AND m.user_id::text = NULLIF(current_setting('app.current_user_id', true), '')))))
       FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'revoked_authority' USING ERRCODE = 'check_violation'; END IF;
END;
$$;

-- Tenant writes can validate the revision without reading platform policy.
CREATE FUNCTION public.check_installation_envelope(expected bigint) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
    PERFORM pg_advisory_xact_lock_shared(hashtextextended('module-envelope-installation',0));
    IF (SELECT COALESCE(max(revision),0) FROM public.module_envelopes) <> expected THEN
        RAISE EXCEPTION 'module envelope changed during installation' USING ERRCODE = 'serialization_failure';
    END IF;
END;
$$;

-- The envelope is one policy subject: a tenancy replacement and every resulting
-- uninstall commit under the same externally witnessed receipt.
ALTER TABLE public.policy_log_commits DROP CONSTRAINT policy_log_commits_subject_kind_check;
ALTER TABLE public.policy_log_commits ADD CONSTRAINT policy_log_commits_subject_kind_check
    CHECK (subject_kind IN ('principal','binding','installation','team_membership','scope_grant','organization','module_envelope'));

-- Every object exists before any ACL moves: CREATE TRIGGER needs EXECUTE, and once
-- the migrator no longer owns a definer its GRANT or REVOKE is a no-op. Then each
-- definer takes its canonical owner, with no schema creation authority left at runtime.
REVOKE ALL ON FUNCTION public.installation_registry_on_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.cancel_installation_work() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.module_job_authority_current(jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.module_job_authority_current(jsonb) TO app_job_worker, app_tenant;
REVOKE ALL ON FUNCTION public.lock_module_job_authority(jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.lock_module_job_authority(jsonb) TO app_job_worker, app_tenant;
REVOKE ALL ON FUNCTION public.check_installation_envelope(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.check_installation_envelope(bigint) TO app_tenant, app_control_plane;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.installation_registry_on_insert() OWNER TO app_control_plane;
ALTER FUNCTION public.module_job_authority_current(jsonb) OWNER TO app_control_plane;
ALTER FUNCTION public.lock_module_job_authority(jsonb) OWNER TO app_control_plane;
ALTER FUNCTION public.check_installation_envelope(bigint) OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
GRANT CREATE ON SCHEMA public TO app_job_worker;
ALTER FUNCTION public.cancel_installation_work() OWNER TO app_job_worker;
REVOKE CREATE ON SCHEMA public FROM app_job_worker;
