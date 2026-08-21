-- Authorization-bearing refresh sessions must not survive changes to the
-- database facts from which their claims were derived. Database triggers are
-- the canonical invalidation boundary: every writer, including future workers
-- and administrative SQL paths, gets the same behavior in the same transaction
-- as the authorization mutation.

CREATE OR REPLACE FUNCTION public.invalidate_authorization_sessions()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_TABLE_NAME = 'users' THEN
        IF NEW.status IS DISTINCT FROM OLD.status THEN
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = CASE
                       WHEN NEW.status = 'active' THEN 'user_status_changed'
                       ELSE 'user_not_active'
                   END
             WHERE user_id = NEW.uuid
               AND revoked_at IS NULL;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_TABLE_NAME = 'organization_members' THEN
        IF TG_OP = 'INSERT' THEN
            -- An org-less session must not silently acquire newly granted
            -- tenant authority on its next refresh.
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = 'organization_membership_added'
             WHERE user_id = NEW.user_id
               AND org_id IS NULL
               AND revoked_at IS NULL;
            RETURN NEW;
        END IF;

        IF TG_OP = 'DELETE' THEN
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = 'organization_membership_revoked'
             WHERE user_id = OLD.user_id
               AND (org_id = OLD.org_id OR org_id IS NULL)
               AND revoked_at IS NULL;
            RETURN OLD;
        END IF;

        IF NEW.user_id IS DISTINCT FROM OLD.user_id
           OR NEW.org_id IS DISTINCT FROM OLD.org_id
           OR NEW.role IS DISTINCT FROM OLD.role THEN
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = 'organization_membership_changed'
             WHERE user_id = OLD.user_id
               AND (org_id = OLD.org_id OR org_id IS NULL)
               AND revoked_at IS NULL;

            IF NEW.user_id IS DISTINCT FROM OLD.user_id
               OR NEW.org_id IS DISTINCT FROM OLD.org_id THEN
                UPDATE public.sessions
                   SET revoked_at = CURRENT_TIMESTAMP,
                       revoked_reason = 'organization_membership_changed'
                 WHERE user_id = NEW.user_id
                   AND (org_id = NEW.org_id OR org_id IS NULL)
                   AND revoked_at IS NULL;
            END IF;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_TABLE_NAME = 'platform_admins' THEN
        IF TG_OP = 'INSERT' THEN
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = 'platform_role_changed'
             WHERE user_id = NEW.user_id
               AND revoked_at IS NULL;
            RETURN NEW;
        END IF;

        IF TG_OP = 'DELETE' THEN
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = 'platform_role_changed'
             WHERE user_id = OLD.user_id
               AND revoked_at IS NULL;
            RETURN OLD;
        END IF;

        IF NEW.user_id IS DISTINCT FROM OLD.user_id
           OR NEW.platform_role IS DISTINCT FROM OLD.platform_role THEN
            IF TG_OP = 'UPDATE' AND NEW.user_id IS DISTINCT FROM OLD.user_id THEN
                UPDATE public.sessions
                   SET revoked_at = CURRENT_TIMESTAMP,
                       revoked_reason = 'platform_role_changed'
                 WHERE user_id = OLD.user_id
                   AND revoked_at IS NULL;
            END IF;
            UPDATE public.sessions
               SET revoked_at = CURRENT_TIMESTAMP,
                   revoked_reason = 'platform_role_changed'
             WHERE user_id = NEW.user_id
               AND revoked_at IS NULL;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_TABLE_NAME = 'mfa_devices' THEN
        IF TG_OP = 'INSERT' THEN
            IF NEW.verified_at IS NOT NULL THEN
                UPDATE public.sessions
                   SET revoked_at = CURRENT_TIMESTAMP,
                       revoked_reason = 'mfa_enrollment_changed'
                 WHERE user_id = NEW.user_id
                   AND revoked_at IS NULL;
            END IF;
            RETURN NEW;
        END IF;

        IF TG_OP = 'DELETE' THEN
            IF OLD.verified_at IS NOT NULL THEN
                UPDATE public.sessions
                   SET revoked_at = CURRENT_TIMESTAMP,
                       revoked_reason = 'mfa_enrollment_changed'
                 WHERE user_id = OLD.user_id
                   AND revoked_at IS NULL;
            END IF;
            RETURN OLD;
        END IF;

        IF NEW.user_id IS DISTINCT FROM OLD.user_id
           OR (NEW.verified_at IS NULL) IS DISTINCT FROM (OLD.verified_at IS NULL) THEN
            IF OLD.verified_at IS NOT NULL THEN
                UPDATE public.sessions
                   SET revoked_at = CURRENT_TIMESTAMP,
                       revoked_reason = 'mfa_enrollment_changed'
                 WHERE user_id = OLD.user_id
                   AND revoked_at IS NULL;
            END IF;
            IF NEW.verified_at IS NOT NULL THEN
                UPDATE public.sessions
                   SET revoked_at = CURRENT_TIMESTAMP,
                       revoked_reason = 'mfa_enrollment_changed'
                 WHERE user_id = NEW.user_id
                   AND revoked_at IS NULL;
            END IF;
        END IF;
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'unsupported authorization invalidation source: %', TG_TABLE_NAME;
END
$function$;

-- Ownership transfer requires the incoming owner to hold CREATE on the
-- schema. app_control_plane is deliberately denied it (migration 67); a
-- superuser migrator only masks the check. Grant it for the transaction span
-- so a server-admin migrator (Azure Flexible Server) can transfer ownership.
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.invalidate_authorization_sessions()
    OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
REVOKE ALL ON FUNCTION public.invalidate_authorization_sessions() FROM PUBLIC;

CREATE TRIGGER users_invalidate_authorization_sessions
AFTER UPDATE OF status ON public.users
FOR EACH ROW EXECUTE FUNCTION public.invalidate_authorization_sessions();

CREATE TRIGGER organization_members_invalidate_authorization_sessions
AFTER INSERT OR UPDATE OF user_id, org_id, role OR DELETE ON public.organization_members
FOR EACH ROW EXECUTE FUNCTION public.invalidate_authorization_sessions();

CREATE TRIGGER platform_admins_invalidate_authorization_sessions
AFTER INSERT OR UPDATE OF user_id, platform_role OR DELETE ON public.platform_admins
FOR EACH ROW EXECUTE FUNCTION public.invalidate_authorization_sessions();

CREATE TRIGGER mfa_devices_invalidate_authorization_sessions
AFTER INSERT OR UPDATE OF user_id, verified_at OR DELETE ON public.mfa_devices
FOR EACH ROW EXECUTE FUNCTION public.invalidate_authorization_sessions();
