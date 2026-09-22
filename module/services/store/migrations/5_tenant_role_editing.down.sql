DROP POLICY IF EXISTS role_permissions_delete ON public.role_permissions;
REVOKE DELETE ON TABLE public.role_permissions FROM app_tenant;
REVOKE UPDATE (description) ON TABLE public.roles FROM app_tenant;
