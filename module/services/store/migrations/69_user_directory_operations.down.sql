DROP FUNCTION IF EXISTS public.organization_member_primary_email(UUID);

REVOKE DELETE ON TABLE public.user_identities FROM app_tenant;

DROP POLICY IF EXISTS users_select ON users;
CREATE POLICY users_select ON users FOR SELECT USING (true);
