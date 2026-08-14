-- Scoped role grants ride in the access token's `sr` claim (resolved from
-- role_assignments where scope IS NOT NULL). Like every other authorization
-- fact a token projects, a change to those grants must not survive on a live
-- session: the session is revoked so the next refresh re-resolves the claim.
--
-- Migration 70 already invalidates sessions on organization_members /
-- platform_admins / mfa_devices / users changes, and migration 78 bumps
-- revision counters on role_assignments — but nothing revoked sessions when a
-- scoped assignment changed. This closes that gap.
--
-- Only principal-subject, non-NULL-scope, org-bound assignments matter: those
-- are exactly the rows the `sr` claim reads. principals.id equals users.uuid
-- for humans (migration 78), so subject_id maps directly to sessions.user_id;
-- non-human principals simply match no session rows.

CREATE OR REPLACE FUNCTION public.invalidate_scoped_role_sessions()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE')
       AND OLD.subject_kind = 'principal'
       AND OLD.scope IS NOT NULL
       AND OLD.org_id IS NOT NULL THEN
        UPDATE public.sessions
           SET revoked_at = CURRENT_TIMESTAMP,
               revoked_reason = 'scoped_role_changed'
         WHERE user_id = OLD.subject_id
           AND (org_id = OLD.org_id OR org_id IS NULL)
           AND revoked_at IS NULL;
    END IF;

    IF TG_OP IN ('INSERT', 'UPDATE')
       AND NEW.subject_kind = 'principal'
       AND NEW.scope IS NOT NULL
       AND NEW.org_id IS NOT NULL THEN
        UPDATE public.sessions
           SET revoked_at = CURRENT_TIMESTAMP,
               revoked_reason = 'scoped_role_changed'
         WHERE user_id = NEW.subject_id
           AND (org_id = NEW.org_id OR org_id IS NULL)
           AND revoked_at IS NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;

ALTER FUNCTION public.invalidate_scoped_role_sessions()
    OWNER TO app_control_plane;
REVOKE ALL ON FUNCTION public.invalidate_scoped_role_sessions() FROM PUBLIC;

CREATE TRIGGER role_assignments_invalidate_scoped_role_sessions
AFTER INSERT OR UPDATE OR DELETE ON public.role_assignments
FOR EACH ROW EXECUTE FUNCTION public.invalidate_scoped_role_sessions();
