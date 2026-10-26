ALTER TABLE public.installations ADD COLUMN exposure_revision bigint NOT NULL DEFAULT 0 CHECK (exposure_revision >= 0);
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


-- Exposing or removing a team at an installation root changes that
-- installation's authority through the existing lifecycle revision owner.
CREATE FUNCTION public.installation_exposure_changed() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER SET search_path = public, pg_temp AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND
       (NEW.org_id, NEW.subject_kind, NEW.subject_id, NEW.scope_path, NEW.role_id, NEW.expires_at)
       IS NOT DISTINCT FROM
       (OLD.org_id, OLD.subject_kind, OLD.subject_id, OLD.scope_path, OLD.role_id, OLD.expires_at) THEN RETURN NULL; END IF;
    IF TG_OP <> 'INSERT' AND OLD.subject_kind = 'team' THEN
        UPDATE installations i SET exposure_revision = exposure_revision + 1
        FROM scope_nodes n WHERE n.id=i.root_scope_node_id
          AND i.org_id=OLD.org_id AND n.scope_path=OLD.scope_path AND i.status <> 'revoked';
    END IF;
    IF TG_OP <> 'DELETE' AND NEW.subject_kind = 'team'
       AND (TG_OP = 'INSERT' OR (NEW.org_id, NEW.scope_path) IS DISTINCT FROM (OLD.org_id, OLD.scope_path)) THEN
        UPDATE installations i SET exposure_revision = exposure_revision + 1
        FROM scope_nodes n WHERE n.id=i.root_scope_node_id
          AND i.org_id=NEW.org_id AND n.scope_path=NEW.scope_path AND i.status <> 'revoked';
    END IF;
    RETURN NULL;
END;
$$;
CREATE TRIGGER installation_exposure_changed AFTER INSERT OR UPDATE OR DELETE ON public.scope_grants
    FOR EACH ROW EXECUTE FUNCTION public.installation_exposure_changed();
