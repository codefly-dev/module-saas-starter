-- Administrative continuity counts administrators, and an administrator who
-- cannot authenticate administers nothing: findIdentity admits only
-- status = 'active' and returns ErrAccountInactive otherwise
-- (module/services/accounts/code/pkg/auth/pg/resolver.go). DeleteUser is a soft
-- delete that leaves the organization_members row standing, so counting every
-- administrative row lets the last usable administrator be removed while the
-- invariant reports the organization healthy.
--
-- Request traffic cannot read a co-member's users row (migration 69), so a
-- direct join in tenant code returns zero rows. That failure mode is the
-- dangerous one: zero administrators reads as "this organization never had one"
-- and the rule exempts it, silently disabling the invariant instead of
-- tightening it. Same remedy as migration 69 -- one SECURITY DEFINER function
-- owned by app_control_plane, scoped to the caller's own organization, exposing
-- only membership ids that caller can already enumerate.

CREATE OR REPLACE FUNCTION public.organization_eligible_administrators(p_org_id UUID)
RETURNS SETOF UUID
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
    SELECT member.user_id
    FROM public.organization_members AS member
    JOIN public.users AS u
      ON u.uuid = member.user_id
    WHERE member.org_id = p_org_id
      AND member.org_id::text = pg_catalog.current_setting('app.current_org_id', true)
      AND member.role IN ('owner', 'admin')
      AND u.status = 'active'
$function$;

-- Ownership transfer needs CREATE on the schema, which migration 67 denies
-- app_control_plane; grant it only for the span of the transfer, as the whole
-- migration runs in one transaction.
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.organization_eligible_administrators(UUID)
    OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
REVOKE ALL ON FUNCTION public.organization_eligible_administrators(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.organization_eligible_administrators(UUID) TO app_tenant;
