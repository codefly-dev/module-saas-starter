-- Deleting an organization archives it (#973). The row stays, so the audit
-- trail, approval decisions and everything else that names the organization keep
-- their referent; `archived_at` records when it stopped being usable and
-- `archived_by` who stopped it.
--
-- Archiving removes every membership in the same transaction, and that is what
-- makes an archived organization unusable: every request-path authorization —
-- the membership cache, organization selection at login, refresh, switching,
-- Work Context minting — resolves through `organization_members`, so none of them
-- needs to learn about archive state. What this migration adds is the guarantee
-- that the emptiness holds: no writer may insert or move a membership into an
-- archived organization, whichever path it takes (an administrator adding a
-- member, an invitation being accepted, SSO just-in-time provisioning, a fixture).
-- One trigger enforces it below every one of them rather than a check in each.

ALTER TABLE public.organizations
    ADD COLUMN archived_at timestamp with time zone,
    ADD COLUMN archived_by uuid;

CREATE FUNCTION public.refuse_archived_organization_membership() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM public.organizations
         WHERE id = NEW.org_id
           AND archived_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'organization % is archived', NEW.org_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION public.refuse_archived_organization_membership() FROM PUBLIC;
ALTER FUNCTION public.refuse_archived_organization_membership() OWNER TO app_control_plane;

CREATE TRIGGER organization_members_refuse_archived_organization
    BEFORE INSERT OR UPDATE OF org_id ON public.organization_members
    FOR EACH ROW EXECUTE FUNCTION public.refuse_archived_organization_membership();
