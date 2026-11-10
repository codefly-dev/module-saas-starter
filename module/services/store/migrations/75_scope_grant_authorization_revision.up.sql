-- Removing or changing a person's or team's scope grant moves the
-- authorization revision.
--
-- A scope grant is read by the authority core as the role `scope-grant:<id>`
-- (an organization role), so a capability issued while it stood carries the
-- organization's revision at that moment. Nothing moved that revision when the
-- grant went: RevokeScope deleted the row and every capability issued under it
-- stayed valid until it lapsed. The other RBAC tables (role_assignments,
-- organization_members, teams) already bump it in a trigger, which is the one
-- place no writer can forget; this puts scope_grants with them, and like the
-- role_assignments trigger it fires BEFORE the row change (the target row is
-- locked first, then the revision, the same order that trigger takes).
--
-- Only UPDATE and DELETE move it. Granting more ends nothing that was issued,
-- and an INSERT bump would refuse every capability of the organization at each
-- install. A re-grant that changes nothing moves nothing; one that changes
-- only its expiry moves it, the row having changed (as migration 50 does for a
-- team's exposure). A grant that merely expires moves nothing (no row
-- changes); the capability lifetime ceiling (authority.DefaultMaxTTL, fifteen
-- minutes) bounds that case.
--
-- An installation agent's own grant at its root (its standing grant) is not
-- covered here. Uninstall deletes it and then revokes the agent principal,
-- whose trigger takes the organization's and the principal's revisions, in the
-- order TestDeleteOrganizationWaitsForUninstallBeforeMembershipDeletion pins;
-- taking the revision at the grant's delete would invert it. Revoking that
-- grant through RevokeScope alone, with the installation left in place, still
-- moves nothing: the capability lifetime bounds it. That path is not how a
-- standing grant is removed and is left to a follow-up.
--
-- A grant to a person moves the person's revision as well as the
-- organization's; a team's moves the organization's.

CREATE FUNCTION public.authorization_revision_scope_grant_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND
       (NEW.org_id, NEW.subject_kind, NEW.subject_id, NEW.scope_path, NEW.role_id, NEW.expires_at)
       IS NOT DISTINCT FROM
       (OLD.org_id, OLD.subject_kind, OLD.subject_id, OLD.scope_path, OLD.role_id, OLD.expires_at) THEN
        RETURN NEW;
    END IF;
    IF OLD.subject_kind = 'principal' AND EXISTS (
        SELECT 1 FROM public.installations i
         WHERE i.org_id = OLD.org_id AND i.agent_principal_id = OLD.subject_id) THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;
    PERFORM public.bump_organization_authorization_revision(OLD.org_id);
    IF OLD.subject_kind = 'principal' THEN
        PERFORM public.bump_principal_authorization_revision(OLD.org_id, OLD.subject_id);
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER scope_grants_bump_authorization_revision BEFORE UPDATE OR DELETE ON public.scope_grants
    FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_scope_grant_mutation();

REVOKE ALL ON FUNCTION public.authorization_revision_scope_grant_mutation() FROM PUBLIC;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.authorization_revision_scope_grant_mutation() OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
