CREATE OR REPLACE FUNCTION public.bump_organization_authorization_revision(target_org UUID)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF target_org IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO public.organization_authorization_revisions (org_id, revision, updated_at)
    VALUES (
        target_org,
        nextval('public.authorization_revision_sequence'),
        CURRENT_TIMESTAMP
    )
    ON CONFLICT (org_id) DO UPDATE
       SET revision = EXCLUDED.revision,
           updated_at = EXCLUDED.updated_at;
END
$function$;

CREATE OR REPLACE FUNCTION public.bump_principal_authorization_revision(
    target_org UUID,
    target_principal UUID
)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF target_org IS NULL OR target_principal IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO public.principal_authorization_revisions (
        org_id,
        principal_id,
        revision,
        updated_at
    )
    VALUES (
        target_org,
        target_principal,
        nextval('public.authorization_revision_sequence'),
        CURRENT_TIMESTAMP
    )
    ON CONFLICT (org_id, principal_id) DO UPDATE
       SET revision = EXCLUDED.revision,
           updated_at = EXCLUDED.updated_at;
END
$function$;
