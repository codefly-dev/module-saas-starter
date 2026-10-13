-- Downgrade never turns unacknowledged consent into usable authority.
DROP TRIGGER record_installation_step ON public.job_messages;
DROP FUNCTION public.record_installation_step();
DROP FUNCTION public.installation_step_lease(uuid,text,uuid);
DROP FUNCTION public.installation_step_job_current(uuid,jsonb);
ALTER TABLE public.installations NO FORCE ROW LEVEL SECURITY;
UPDATE public.installations SET status='revoked', revoked_at=clock_timestamp(),
    revoked_reason='installation lifecycle downgrade', failure_reason=NULL
    WHERE status IN ('pending','failed');
ALTER TABLE public.installations FORCE ROW LEVEL SECURITY;
ALTER TABLE public.installations
    DROP CONSTRAINT installations_failure_reason,
    DROP COLUMN install_job_id,
    DROP COLUMN install_attempts,
    DROP COLUMN failure_reason,
    ALTER COLUMN status SET DEFAULT 'active',
    DROP CONSTRAINT installations_status_check,
    ADD CONSTRAINT installations_status_check CHECK (status IN ('active','revoked')),
    DROP CONSTRAINT installations_active_requires_target,
    ADD CONSTRAINT installations_active_requires_target CHECK (status <> 'active' OR target_id IS NOT NULL),
    DROP CONSTRAINT installations_active_registry,
    ADD CONSTRAINT installations_active_registry CHECK (status <> 'active' OR registry_binding_id IS NOT NULL);
DROP INDEX public.installations_active_principal;
CREATE UNIQUE INDEX installations_active_principal ON public.installations(org_id, registry_principal_id)
    WHERE status='active' AND registry_principal_id IS NOT NULL;
DROP INDEX public.idx_installations_active_target;
CREATE UNIQUE INDEX idx_installations_active_target ON public.installations(org_id,target_id) WHERE status='active';
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
    IF NEW.scope IS DISTINCT FROM OLD.scope OR NEW.status IS DISTINCT FROM OLD.status THEN
        NEW.revision := OLD.revision + 1;
    ELSIF NEW.revision <> OLD.revision THEN
        RAISE EXCEPTION 'installation revision is lifecycle-owned' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION public.installations_target_must_be_live()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    closed bigint;
    found boolean;
BEGIN
    IF NEW.status <> 'active' OR NEW.target_id IS NULL THEN
        RETURN NEW;
    END IF;
    -- FOR UPDATE, because reading the target without locking it lets this
    -- insert race a withdrawal: the trigger reads "open", a concurrent
    -- transaction closes the target, and both commit — leaving an ACTIVE
    -- installation of a withdrawn target, which is the state the trigger exists
    -- to make unreachable.
    --
    -- Withdrawal closes a target with an UPDATE, which takes the same row lock,
    -- so the two serialize: whichever reaches the row first wins and the other
    -- sees its committed result. Locking the target rather than the
    -- installation is what makes this work — the two transactions touch
    -- different installation rows and would never contend there.
    SELECT t.closed_generation, TRUE INTO closed, found
      FROM public.solution_targets t
     WHERE t.id = NEW.target_id
       FOR UPDATE;
    IF NOT COALESCE(found, FALSE) THEN
        RAISE EXCEPTION 'solution target % does not exist', NEW.target_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF closed IS NOT NULL THEN
        RAISE EXCEPTION
            'solution target % was withdrawn at generation %, so it cannot be installed: a replacement presence has its own target',
            NEW.target_id, closed
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;


DROP TRIGGER installations_target_live_on_update ON public.installations;
CREATE TRIGGER installations_target_live_on_update BEFORE UPDATE ON public.installations
    FOR EACH ROW WHEN (NEW.status='active' AND OLD.status<>'active')
    EXECUTE FUNCTION public.installations_target_must_be_live();
