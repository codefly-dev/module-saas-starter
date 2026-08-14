DROP TRIGGER IF EXISTS role_assignments_invalidate_scoped_role_sessions ON public.role_assignments;

DROP FUNCTION IF EXISTS public.invalidate_scoped_role_sessions();
