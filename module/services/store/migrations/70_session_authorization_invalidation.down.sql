DROP TRIGGER IF EXISTS mfa_devices_invalidate_authorization_sessions ON public.mfa_devices;
DROP TRIGGER IF EXISTS platform_admins_invalidate_authorization_sessions ON public.platform_admins;
DROP TRIGGER IF EXISTS organization_members_invalidate_authorization_sessions ON public.organization_members;
DROP TRIGGER IF EXISTS users_invalidate_authorization_sessions ON public.users;

DROP FUNCTION IF EXISTS public.invalidate_authorization_sessions();
