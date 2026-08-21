-- Monotonic authorization revisions for cache safety and signed Work Contexts.
--
-- Revisions are database facts, not process counters. Every authorization
-- mutation advances the same sequence inside the mutation transaction. The
-- effective revision for one principal in one organization is:
--
--   greatest(organization revision, principal-within-organization revision)
--
-- An organization-wide bump is intentionally conservative for team/role
-- mutations. It may invalidate more cached decisions than strictly necessary,
-- but it cannot leave an authorization-bearing capability current after its
-- source facts changed.

CREATE SEQUENCE public.authorization_revision_sequence
    AS BIGINT
    MINVALUE 1
    START WITH 1
    NO CYCLE;

CREATE TABLE public.organization_authorization_revisions (
    org_id UUID PRIMARY KEY REFERENCES public.organizations(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL
        DEFAULT nextval('public.authorization_revision_sequence')
        CHECK (revision > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.principal_authorization_revisions (
    org_id UUID NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
    principal_id UUID NOT NULL,
    revision BIGINT NOT NULL
        DEFAULT nextval('public.authorization_revision_sequence')
        CHECK (revision > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, principal_id)
);

CREATE INDEX principal_authorization_revisions_principal_idx
    ON public.principal_authorization_revisions (principal_id, org_id);

INSERT INTO public.organization_authorization_revisions (org_id)
SELECT id
FROM public.organizations
ON CONFLICT (org_id) DO NOTHING;

INSERT INTO public.principal_authorization_revisions (org_id, principal_id)
SELECT org_id, user_id
FROM public.organization_members
ON CONFLICT (org_id, principal_id) DO NOTHING;

INSERT INTO public.principal_authorization_revisions (org_id, principal_id)
SELECT org_id, id
FROM public.principals
WHERE org_id IS NOT NULL
ON CONFLICT (org_id, principal_id) DO NOTHING;

ALTER TABLE public.organization_authorization_revisions
    ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.organization_authorization_revisions
    FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_authorization_revisions_tenant_read
    ON public.organization_authorization_revisions
    FOR SELECT
    USING (
        org_id::text = current_setting('app.current_org_id', true)
    );

ALTER TABLE public.principal_authorization_revisions
    ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.principal_authorization_revisions
    FORCE ROW LEVEL SECURITY;
CREATE POLICY principal_authorization_revisions_tenant_read
    ON public.principal_authorization_revisions
    FOR SELECT
    USING (
        org_id::text = current_setting('app.current_org_id', true)
    );

REVOKE ALL PRIVILEGES ON
    public.organization_authorization_revisions,
    public.principal_authorization_revisions
FROM PUBLIC, app_tenant;
GRANT SELECT ON
    public.organization_authorization_revisions,
    public.principal_authorization_revisions
TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON
    public.organization_authorization_revisions,
    public.principal_authorization_revisions
TO app_control_plane;

REVOKE ALL PRIVILEGES ON SEQUENCE
    public.authorization_revision_sequence
FROM PUBLIC, app_tenant;
GRANT USAGE, SELECT, UPDATE ON SEQUENCE
    public.authorization_revision_sequence
TO app_control_plane;

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

CREATE OR REPLACE FUNCTION public.bump_principal_and_organization_authorization(
    target_org UUID,
    target_principal UUID
)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    PERFORM public.bump_organization_authorization_revision(target_org);
    PERFORM public.bump_principal_authorization_revision(target_org, target_principal);
END
$function$;

CREATE OR REPLACE FUNCTION public.bump_role_authorization_revision(
    target_role UUID,
    direct_org UUID
)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    affected_org UUID;
BEGIN
    IF direct_org IS NOT NULL THEN
        PERFORM public.bump_organization_authorization_revision(direct_org);
    END IF;
    FOR affected_org IN
        SELECT DISTINCT assignment.org_id
        FROM public.role_assignments AS assignment
        WHERE assignment.role_id = target_role
          AND assignment.org_id IS NOT NULL
    LOOP
        PERFORM public.bump_organization_authorization_revision(affected_org);
    END LOOP;
END
$function$;

CREATE OR REPLACE FUNCTION public.authorization_revision_organization_insert()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    INSERT INTO public.organization_authorization_revisions (org_id)
    VALUES (NEW.id)
    ON CONFLICT (org_id) DO NOTHING;
    RETURN NEW;
END
$function$;

CREATE TRIGGER organizations_seed_authorization_revision
AFTER INSERT ON public.organizations
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_organization_insert();

CREATE OR REPLACE FUNCTION public.authorization_revision_membership_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM public.bump_principal_and_organization_authorization(OLD.org_id, OLD.user_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        IF TG_OP <> 'UPDATE'
           OR NEW.org_id IS DISTINCT FROM OLD.org_id
           OR NEW.user_id IS DISTINCT FROM OLD.user_id
           OR NEW.role IS DISTINCT FROM OLD.role THEN
            PERFORM public.bump_principal_and_organization_authorization(NEW.org_id, NEW.user_id);
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER organization_members_bump_authorization_revision
BEFORE INSERT OR UPDATE OF org_id, user_id, role OR DELETE
ON public.organization_members
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_membership_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_team_member_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    target_org UUID;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        SELECT org_id INTO target_org
        FROM public.teams
        WHERE id = OLD.team_id;
        PERFORM public.bump_principal_and_organization_authorization(target_org, OLD.user_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        SELECT org_id INTO target_org
        FROM public.teams
        WHERE id = NEW.team_id;
        IF TG_OP <> 'UPDATE'
           OR NEW.team_id IS DISTINCT FROM OLD.team_id
           OR NEW.user_id IS DISTINCT FROM OLD.user_id
           OR NEW.role IS DISTINCT FROM OLD.role THEN
            PERFORM public.bump_principal_and_organization_authorization(target_org, NEW.user_id);
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER team_members_bump_authorization_revision
BEFORE INSERT OR UPDATE OF team_id, user_id, role OR DELETE
ON public.team_members
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_team_member_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_team_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM public.bump_organization_authorization_revision(OLD.org_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        IF TG_OP <> 'UPDATE' OR NEW.org_id IS DISTINCT FROM OLD.org_id THEN
            PERFORM public.bump_organization_authorization_revision(NEW.org_id);
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER teams_bump_authorization_revision
BEFORE INSERT OR UPDATE OF org_id, parent_team_id OR DELETE
ON public.teams
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_team_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_role_assignment_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.org_id IS NOT NULL THEN
        PERFORM public.bump_organization_authorization_revision(OLD.org_id);
        IF OLD.subject_kind = 'user' THEN
            PERFORM public.bump_principal_authorization_revision(OLD.org_id, OLD.subject_id);
        END IF;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND NEW.org_id IS NOT NULL THEN
        PERFORM public.bump_organization_authorization_revision(NEW.org_id);
        IF NEW.subject_kind = 'user' THEN
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

CREATE TRIGGER role_assignments_bump_authorization_revision
BEFORE INSERT OR UPDATE OR DELETE ON public.role_assignments
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_role_assignment_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_role_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM public.bump_role_authorization_revision(OLD.id, OLD.org_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM public.bump_role_authorization_revision(NEW.id, NEW.org_id);
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER roles_bump_authorization_revision
BEFORE INSERT OR UPDATE OR DELETE ON public.roles
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_role_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_role_permission_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    target_org UUID;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        SELECT org_id INTO target_org FROM public.roles WHERE id = OLD.role_id;
        PERFORM public.bump_role_authorization_revision(OLD.role_id, target_org);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        SELECT org_id INTO target_org FROM public.roles WHERE id = NEW.role_id;
        PERFORM public.bump_role_authorization_revision(NEW.role_id, target_org);
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER role_permissions_bump_authorization_revision
BEFORE INSERT OR UPDATE OR DELETE ON public.role_permissions
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_role_permission_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_principal_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    affected_org UUID;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        IF OLD.org_id IS NOT NULL THEN
            PERFORM public.bump_principal_and_organization_authorization(OLD.org_id, OLD.id);
        ELSE
            FOR affected_org IN
                SELECT org_id FROM public.organization_members WHERE user_id = OLD.id
            LOOP
                PERFORM public.bump_principal_and_organization_authorization(affected_org, OLD.id);
            END LOOP;
        END IF;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        IF NEW.org_id IS NOT NULL THEN
            PERFORM public.bump_principal_and_organization_authorization(NEW.org_id, NEW.id);
        ELSE
            FOR affected_org IN
                SELECT org_id FROM public.organization_members WHERE user_id = NEW.id
            LOOP
                PERFORM public.bump_principal_and_organization_authorization(affected_org, NEW.id);
            END LOOP;
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER principals_bump_authorization_revision
BEFORE INSERT OR UPDATE OF org_id, revoked_at, revoked_reason OR DELETE
ON public.principals
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_principal_mutation();

CREATE OR REPLACE FUNCTION public.sync_human_principal()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO public.principals (
            id,
            kind,
            display_name,
            org_id,
            agent_identifier,
            created_at
        )
        VALUES (
            NEW.uuid,
            'human',
            NEW.primary_email,
            NULL,
            NULL,
            NEW.created_at
        )
        ON CONFLICT (id) DO NOTHING;
        RETURN NEW;
    END IF;

    UPDATE public.principals
       SET display_name = NEW.primary_email,
           revoked_at = CASE
               WHEN NEW.status IN ('inactive', 'suspended', 'deleted')
                   THEN COALESCE(revoked_at, CURRENT_TIMESTAMP)
               ELSE revoked_at
           END,
           revoked_reason = CASE
               WHEN NEW.status IN ('inactive', 'suspended', 'deleted')
                   THEN COALESCE(revoked_reason, 'user_status_' || NEW.status::text)
               ELSE revoked_reason
           END
     WHERE id = NEW.uuid
       AND kind = 'human';
    RETURN NEW;
END
$function$;

INSERT INTO public.principals (
    id,
    kind,
    display_name,
    org_id,
    agent_identifier,
    created_at
)
SELECT
    uuid,
    'human',
    primary_email,
    NULL,
    NULL,
    created_at
FROM public.users
WHERE status <> 'deleted'
ON CONFLICT (id) DO NOTHING;

CREATE TRIGGER users_sync_human_principal
AFTER INSERT OR UPDATE OF primary_email, status ON public.users
FOR EACH ROW EXECUTE FUNCTION public.sync_human_principal();

CREATE OR REPLACE FUNCTION public.authorization_revision_user_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    affected_org UUID;
BEGIN
    FOR affected_org IN
        SELECT org_id
        FROM public.organization_members
        WHERE user_id = COALESCE(NEW.uuid, OLD.uuid)
    LOOP
        PERFORM public.bump_principal_and_organization_authorization(
            affected_org,
            COALESCE(NEW.uuid, OLD.uuid)
        );
    END LOOP;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;

CREATE TRIGGER users_bump_authorization_revision
BEFORE UPDATE OF status OR DELETE ON public.users
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_user_mutation();

CREATE OR REPLACE FUNCTION public.authorization_revision_platform_admin_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    affected_org UUID;
    affected_user UUID;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        affected_user := OLD.user_id;
        FOR affected_org IN
            SELECT org_id FROM public.organization_members WHERE user_id = affected_user
        LOOP
            PERFORM public.bump_principal_and_organization_authorization(affected_org, affected_user);
        END LOOP;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        affected_user := NEW.user_id;
        FOR affected_org IN
            SELECT org_id FROM public.organization_members WHERE user_id = affected_user
        LOOP
            PERFORM public.bump_principal_and_organization_authorization(affected_org, affected_user);
        END LOOP;
        RETURN NEW;
    END IF;
    RETURN OLD;
END
$function$;

CREATE TRIGGER platform_admins_bump_authorization_revision
BEFORE INSERT OR UPDATE OR DELETE ON public.platform_admins
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_platform_admin_mutation();

-- Ownership transfer requires the incoming owner to hold CREATE on the
-- schema. app_control_plane is deliberately denied it (migration 67); a
-- superuser migrator only masks the check. Grant it for the transaction span
-- so a server-admin migrator (Azure Flexible Server) can transfer ownership.
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.bump_organization_authorization_revision(UUID)
    OWNER TO app_control_plane;
ALTER FUNCTION public.bump_principal_authorization_revision(UUID, UUID)
    OWNER TO app_control_plane;
ALTER FUNCTION public.bump_principal_and_organization_authorization(UUID, UUID)
    OWNER TO app_control_plane;
ALTER FUNCTION public.bump_role_authorization_revision(UUID, UUID)
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_organization_insert()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_membership_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_team_member_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_team_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_role_assignment_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_role_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_role_permission_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_principal_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.sync_human_principal()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_user_mutation()
    OWNER TO app_control_plane;
ALTER FUNCTION public.authorization_revision_platform_admin_mutation()
    OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;

REVOKE ALL ON FUNCTION
    public.bump_organization_authorization_revision(UUID),
    public.bump_principal_authorization_revision(UUID, UUID),
    public.bump_principal_and_organization_authorization(UUID, UUID),
    public.bump_role_authorization_revision(UUID, UUID),
    public.authorization_revision_organization_insert(),
    public.authorization_revision_membership_mutation(),
    public.authorization_revision_team_member_mutation(),
    public.authorization_revision_team_mutation(),
    public.authorization_revision_role_assignment_mutation(),
    public.authorization_revision_role_mutation(),
    public.authorization_revision_role_permission_mutation(),
    public.authorization_revision_principal_mutation(),
    public.sync_human_principal(),
    public.authorization_revision_user_mutation(),
    public.authorization_revision_platform_admin_mutation()
FROM PUBLIC, app_tenant;
