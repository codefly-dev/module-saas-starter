-- The users relation contains private account data (email, profile, status).
-- Request traffic may read its own row only. Cross-user control-plane and
-- worker operations use their named BYPASSRLS roles; tenant code that needs a
-- co-member's email gets one deliberately narrow, organization-scoped
-- operation instead of SELECT visibility over the complete users relation.

DROP POLICY IF EXISTS users_select ON users;

CREATE POLICY users_select ON users FOR SELECT
    USING (uuid::text = current_setting('app.current_user_id', true));

CREATE OR REPLACE FUNCTION public.organization_member_primary_email(p_user_id UUID)
RETURNS TEXT
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
    SELECT u.primary_email
    FROM public.users AS u
    JOIN public.organization_members AS member
      ON member.user_id = u.uuid
    WHERE u.uuid = p_user_id
      AND member.org_id::text = pg_catalog.current_setting('app.current_org_id', true)
      AND u.status <> 'deleted'
    LIMIT 1
$function$;

-- app_control_plane is a NOLOGIN, BYPASSRLS role with SELECT on both source
-- relations. Owning this SECURITY DEFINER function gives app_tenant exactly
-- EXECUTE on the checked operation without widening either table policy.
--
-- Transferring ownership requires the incoming owner to hold CREATE on the
-- schema. A superuser migrator (local dev) skips that check; a server-admin
-- migrator (Azure Flexible Server) does not. app_control_plane is
-- deliberately denied schema CREATE (migration 67), so grant it only for the
-- span of the transfer — the whole migration runs in one transaction.
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.organization_member_primary_email(UUID)
    OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
REVOKE ALL ON FUNCTION public.organization_member_primary_email(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.organization_member_primary_email(UUID) TO app_tenant;

-- A user may remove its own linked identities (used by the atomic GDPR
-- deletion operation). The existing user_identities_user policy still limits
-- DELETE to app.current_user_id; this is not cross-user authority.
GRANT DELETE ON TABLE public.user_identities TO app_tenant;
