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

-- SECURITY INVOKER, deliberately: the check reads `organizations` as the role
-- writing the membership. Only two roles may write one — `app_tenant`, whose
-- write is admitted only into the organization its transaction is scoped to
-- (`organization_members_tenant`), which `organizations_self` lets it read; and
-- `app_control_plane`, which reads every organization. Either sees the row it
-- needs. A definer owned by the migrator would not: `organizations` forces row
-- security, so under a control-plane transaction the owner would read no row
-- and the guard would pass an archived organization. It also keeps ownership
-- with the migrator, as migration 13 does — a managed migrator may not hand a
-- function to `app_control_plane`, which holds no CREATE on `public`.
CREATE FUNCTION public.refuse_archived_organization_membership() RETURNS trigger
    LANGUAGE plpgsql SECURITY INVOKER
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

CREATE TRIGGER organization_members_refuse_archived_organization
    BEFORE INSERT OR UPDATE OF org_id ON public.organization_members
    FOR EACH ROW EXECUTE FUNCTION public.refuse_archived_organization_membership();
