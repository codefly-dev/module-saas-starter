-- platform_admins and bootstrap_state are platform tables: they carry no tenant
-- column and no row-level security, so a write by app_tenant is a write for the
-- whole platform. Any statement reachable from a tenant transaction — an
-- injected expression, a confused handler — could mint a platform
-- administrator or re-arm the one-time bootstrap claim.
--
-- Nothing legitimate writes them as a tenant. Granting and revoking a platform
-- role, and the first-login bootstrap claim, all run as app_control_plane
-- (PlatformAdminService under WithControlPlane, the resolver under
-- WithAuthBootstrapTx). Reads stay: request paths resolve a caller's platform
-- role from platform_admins.
REVOKE INSERT, UPDATE, DELETE ON TABLE public.platform_admins FROM app_tenant;
REVOKE UPDATE ON TABLE public.bootstrap_state FROM app_tenant;
