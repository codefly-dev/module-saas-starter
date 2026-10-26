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
    IF NEW.exposure_revision IS DISTINCT FROM OLD.exposure_revision OR NEW.scope IS DISTINCT FROM OLD.scope OR (NEW.status = 'revoked' AND OLD.status <> 'revoked') THEN
        NEW.revision := OLD.revision + 1;
    ELSIF NEW.revision <> OLD.revision THEN
        RAISE EXCEPTION 'installation revision is lifecycle-owned' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
