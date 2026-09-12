-- Deactivating an identity is an administrative-continuity decision about every
-- organization that identity administers, not about one of them. DeleteUser is
-- a soft delete and SuspendUser a status change; neither touches
-- organization_members, so the administrative membership rows survive while
-- findIdentity stops admitting the identity entirely
-- (module/services/accounts/code/pkg/auth/pg/resolver.go). Migration 133 already
-- says an eligible administrator is an administrative membership held by an
-- active identity, which is exactly what a deactivation takes away.
--
-- Deciding that needs three facts per organization the identity administers: that
-- it administers it, how many eligible administrators the organization has, and
-- whether anybody else is still in it. The last one is what separates an
-- organization left unadministrable from one left empty: RegisterUser gives every
-- identity a personal organization it solely owns, so a rule that only counted
-- administrators would refuse every deletion on this platform.
--
-- A user-scoped request transaction can read none of it: organization_members is
-- scoped to app.current_org_id (migrations 29/68), which a deactivation does not
-- set, and users is readable only for the caller's own row (migration 69). The
-- failure is silent in both cases -- zero rows, no error -- and zero administered
-- organizations reads as "this identity administers nothing", which is precisely
-- the answer that lets the deactivation through. Same remedy as migrations 69 and
-- 133: one SECURITY DEFINER operation owned by app_control_plane, scoped to the
-- caller's own identity, exposing only organizations that identity is already a
-- member of and counts over them.
--
-- The administrator count deliberately repeats 133's eligibility predicate rather
-- than calling organization_eligible_administrators: that function scopes itself
-- to app.current_org_id, which a deactivation has no single value for.

CREATE OR REPLACE FUNCTION public.identity_administered_organizations(p_user_id UUID)
RETURNS TABLE (
    administered_org_id    UUID,
    eligible_administrators INTEGER,
    other_active_members    INTEGER
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
    SELECT held.org_id,
           (
               SELECT count(*)::integer
               FROM public.organization_members AS peer
               JOIN public.users AS peer_holder
                 ON peer_holder.uuid = peer.user_id
               WHERE peer.org_id = held.org_id
                 AND peer.role IN ('owner', 'admin')
                 AND peer_holder.status = 'active'
           ),
           (
               SELECT count(*)::integer
               FROM public.organization_members AS peer
               JOIN public.users AS peer_holder
                 ON peer_holder.uuid = peer.user_id
               WHERE peer.org_id = held.org_id
                 AND peer.user_id <> p_user_id
                 AND peer_holder.status = 'active'
           )
    FROM public.organization_members AS held
    JOIN public.users AS holder
      ON holder.uuid = held.user_id
    WHERE held.user_id = p_user_id
      AND p_user_id::text = pg_catalog.current_setting('app.current_user_id', true)
      AND held.role IN ('owner', 'admin')
      AND holder.status = 'active'
    ORDER BY held.org_id
$function$;

-- Narrow the audience before handing the function over, not after. A migration
-- principal that is not a superuser -- which the managed profile's is, by
-- construction -- stops being the owner the moment ownership moves, and a
-- non-owner REVOKE or GRANT touches only the grants that role itself made:
-- PostgreSQL warns and changes nothing rather than refusing. Done in the other
-- order this silently leaves the default EXECUTE to PUBLIC in place on exactly
-- the deployment that has no superuser to fall back on, which for a
-- SECURITY DEFINER function is the whole audience it exists to narrow.
-- ALTER FUNCTION ... OWNER TO rewrites the owner's own ACL entry and preserves
-- every other grant, so the audience set here survives the transfer.
REVOKE ALL ON FUNCTION public.identity_administered_organizations(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.identity_administered_organizations(UUID) TO app_tenant;

-- Ownership transfer needs CREATE on the schema, which migration 67 denies
-- app_control_plane; grant it only for the span of the transfer, as the whole
-- migration runs in one transaction.
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.identity_administered_organizations(UUID)
    OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
