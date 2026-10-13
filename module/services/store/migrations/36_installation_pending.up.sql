-- HOST-INST-002..006: consent exists before it becomes usable. The revoked
-- timestamp equivalence is deliberately unchanged. Failed consent still occupies
-- both uniqueness keys until explicitly uninstalled.
ALTER TABLE public.installations
    DROP CONSTRAINT installations_status_check,
    ADD CONSTRAINT installations_status_check CHECK (status IN ('pending','active','failed','revoked')),
    ALTER COLUMN status SET DEFAULT 'pending',
    ADD COLUMN failure_reason text,
    ADD COLUMN install_attempts integer NOT NULL DEFAULT 0 CHECK (install_attempts >= 0),
    ADD COLUMN install_job_id uuid REFERENCES public.job_messages(id),
    ADD CONSTRAINT installations_failure_reason CHECK (
        (failure_reason IS NULL OR (status = 'failed' AND octet_length(failure_reason) BETWEEN 1 AND 512))
        AND (status <> 'failed' OR failure_reason IS NOT NULL)),
    DROP CONSTRAINT installations_active_requires_target,
    ADD CONSTRAINT installations_active_requires_target CHECK (status = 'revoked' OR target_id IS NOT NULL),
    DROP CONSTRAINT installations_active_registry,
    ADD CONSTRAINT installations_active_registry CHECK (status = 'revoked' OR registry_binding_id IS NOT NULL);
DROP INDEX public.installations_active_principal;
CREATE UNIQUE INDEX installations_active_principal ON public.installations(org_id, registry_principal_id)
    WHERE status IN ('pending','active','failed') AND registry_principal_id IS NOT NULL;
DROP INDEX public.idx_installations_active_target;
CREATE UNIQUE INDEX idx_installations_active_target ON public.installations(org_id, target_id)
    WHERE status IN ('pending','active','failed');

-- Acknowledgement and a manual retry belong to the SAME consent revision.
-- Scope changes and revocation still fence every ordinary use and deferred job.
CREATE OR REPLACE FUNCTION public.installation_authority_revision() RETURNS trigger
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
    IF NEW.scope IS DISTINCT FROM OLD.scope OR (NEW.status = 'revoked' AND OLD.status <> 'revoked') THEN
        NEW.revision := OLD.revision + 1;
    ELSIF NEW.revision <> OLD.revision THEN
        RAISE EXCEPTION 'installation revision is lifecycle-owned' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

-- Pending inserts must serialize with withdrawal just as active inserts did.
CREATE OR REPLACE FUNCTION public.installations_target_must_be_live() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE closed bigint;
BEGIN
    IF NEW.status = 'revoked' THEN RETURN NEW; END IF;
    -- SHARE conflicts with withdrawal's UPDATE while admitting concurrent
    -- installations of the same target in different organizations.
    SELECT closed_generation INTO closed FROM public.solution_targets
        WHERE id=NEW.target_id FOR SHARE;
    IF NOT FOUND OR closed IS NOT NULL THEN
        RAISE EXCEPTION 'solution target is absent or withdrawn' USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER installations_target_live_on_update ON public.installations;
CREATE TRIGGER installations_target_live_on_update BEFORE UPDATE ON public.installations
    FOR EACH ROW WHEN (NEW.status IN ('pending','active') AND NEW.status IS DISTINCT FROM OLD.status)
    EXECUTE FUNCTION public.installations_target_must_be_live();

-- The common job system owns leases, attempts, backoff, and exhaustion. Project
-- its outcome in that SAME transaction, including expiry of the last lease.
-- Only the current job may publish a result; a retry or newer revision fences a
-- late acknowledgement. A revision replay never takes an active installation
-- out of service. No caller receives job-table access through this trigger.
CREATE FUNCTION public.record_installation_step() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
    IF NEW.queue <> 'installation-step' OR NEW.source <> 'saas.installation'
       OR NEW.topic <> 'installation.step' OR NEW.scope_kind <> 'tenant' THEN
        RETURN NEW;
    END IF;
    -- Withdrawal locks target before installation. Keep the same order when
    -- an acknowledgement transitions pending consent to active.
    PERFORM 1 FROM public.solution_targets t
      WHERE t.id IN (SELECT target_id FROM public.installations
                     WHERE install_job_id=NEW.id AND org_id=NEW.organization_id)
      ORDER BY t.id FOR SHARE;
    UPDATE public.installations
       SET install_attempts = NEW.attempt_count,
           status = CASE WHEN NEW.state = 'succeeded' THEN 'active'
                         WHEN NEW.state = 'dead_letter' AND status = 'pending' THEN 'failed'
                         ELSE status END,
           failure_reason = CASE WHEN NEW.state = 'dead_letter' AND status = 'pending'
               THEN CASE WHEN NEW.last_error_code = 'installation.refused'
                              AND octet_length(NEW.last_error_message) BETWEEN 1 AND 512
                         THEN NEW.last_error_message
                         ELSE 'The installation step could not be completed after repeated attempts.' END
               ELSE NULL END
     WHERE install_job_id = NEW.id AND org_id = NEW.organization_id
       AND id::text = NEW.attributes->>'installation_step.id'
       AND revision::text = NEW.attributes->>'installation_step.revision'
       AND status IN ('pending','active');
    RETURN NEW;
END;
$$;
CREATE TRIGGER record_installation_step AFTER UPDATE OF state ON public.job_messages
    FOR EACH ROW WHEN (NEW.state IS DISTINCT FROM OLD.state)
    EXECUTE FUNCTION public.record_installation_step();

-- A fixed-queue lease check for the install consumer. This exposes no payloads
-- or other queues and grants the control plane no general worker privileges.
-- Setup carries cancellation attribution but is not an active module capability.
-- Only the exact run attached by the lifecycle transaction may be claimed.
CREATE FUNCTION public.installation_step_job_current(job uuid, stamp jsonb)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT EXISTS (SELECT 1 FROM public.installations i
      WHERE i.install_job_id=job AND i.id::text=stamp->>'codefly.installation_id'
        AND i.org_id::text=stamp->>'codefly.organization_id'
        AND i.revision::text=stamp->>'codefly.installation_revision'
        AND i.status IN ('pending','active'));
$$;

CREATE FUNCTION public.installation_step_lease(job uuid, worker text, token uuid)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT EXISTS (SELECT 1 FROM public.job_messages j
      WHERE j.id=job AND j.queue='installation-step' AND j.source='saas.installation'
        AND j.topic='installation.step' AND j.scope_kind='tenant'
        AND j.state='processing' AND j.lease_owner=worker AND j.lease_token=token
        AND j.lease_expires_at > clock_timestamp());
$$;

-- All objects exist before ACLs and transfers. Schema CREATE is held only for
-- each individual transfer and is never left on a runtime role.
REVOKE ALL ON FUNCTION public.record_installation_step() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.installation_step_job_current(uuid,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.installation_step_job_current(uuid,jsonb) TO app_job_worker;
REVOKE ALL ON FUNCTION public.installation_step_lease(uuid,text,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.installation_step_lease(uuid,text,uuid) TO app_control_plane;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.record_installation_step() OWNER TO app_control_plane;
ALTER FUNCTION public.installation_step_job_current(uuid,jsonb) OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
GRANT CREATE ON SCHEMA public TO app_job_worker;
ALTER FUNCTION public.installation_step_lease(uuid,text,uuid) OWNER TO app_job_worker;
REVOKE CREATE ON SCHEMA public FROM app_job_worker;
