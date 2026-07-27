-- Direct RBAC subjects are Principals, not only human users. Keep teams as the
-- indirect/group subject kind; agent and service Principals can now receive
-- explicit roles without being encoded as users.

ALTER TABLE public.role_assignments
    DROP CONSTRAINT role_assignments_subject_kind_check;

UPDATE public.role_assignments
SET subject_kind = 'principal'
WHERE subject_kind = 'user';

ALTER TABLE public.role_assignments
    ADD CONSTRAINT role_assignments_subject_kind_check
    CHECK (subject_kind IN ('principal', 'team'));

CREATE OR REPLACE FUNCTION public.authorization_revision_role_assignment_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.org_id IS NOT NULL THEN
        PERFORM public.bump_organization_authorization_revision(OLD.org_id);
        IF OLD.subject_kind = 'principal' THEN
            PERFORM public.bump_principal_authorization_revision(OLD.org_id, OLD.subject_id);
        END IF;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND NEW.org_id IS NOT NULL THEN
        PERFORM public.bump_organization_authorization_revision(NEW.org_id);
        IF NEW.subject_kind = 'principal' THEN
            PERFORM public.bump_principal_authorization_revision(NEW.org_id, NEW.subject_id);
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;
