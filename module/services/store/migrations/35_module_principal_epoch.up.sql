-- A module principal's epoch in an organization: the value every Work Context it
-- owns is sealed to (core/workcontext WorkSealV1.principal_epoch), and the
-- producer epoch the module authority already reads (LiveModuleAuthority reads
-- principal_authorization_revisions for the installation's registry principal).
--
-- A person's row is written by the membership triggers. A module principal is
-- declared by the deployment rather than registered, so nothing wrote one: the
-- read answered 0, which names no epoch, and core refuses to mint against it.
-- Installing a module, and every change to its installation's revision (scope or
-- status), now advances its principal's authorization revision in that
-- organization, so the epoch exists from the first installation and moves with
-- the consent it is held through.

CREATE FUNCTION public.installation_principal_epoch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF NEW.registry_principal_id IS NOT NULL
       AND (TG_OP = 'INSERT' OR NEW.revision IS DISTINCT FROM OLD.revision) THEN
        PERFORM public.bump_principal_authorization_revision(NEW.org_id, NEW.registry_principal_id);
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER installation_principal_epoch AFTER INSERT OR UPDATE ON public.installations
    FOR EACH ROW EXECUTE FUNCTION public.installation_principal_epoch();

-- Every module installation already active gets its principal's epoch now, so a
-- capability can be minted for it without waiting for its next change. Run as
-- the control plane, whose explicit-rows policies read every organization's
-- installations, through a function that exists only for this statement.
CREATE FUNCTION public.installation_principal_epoch_backfill() RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    PERFORM public.bump_principal_authorization_revision(i.org_id, i.registry_principal_id)
       FROM public.installations i
      WHERE i.status = 'active' AND i.registry_principal_id IS NOT NULL;
END;
$$;

REVOKE ALL ON FUNCTION public.installation_principal_epoch() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.installation_principal_epoch_backfill() FROM PUBLIC;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.installation_principal_epoch() OWNER TO app_control_plane;
ALTER FUNCTION public.installation_principal_epoch_backfill() OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;

SET LOCAL ROLE app_control_plane;
SELECT public.installation_principal_epoch_backfill();
RESET ROLE;
DROP FUNCTION public.installation_principal_epoch_backfill();
