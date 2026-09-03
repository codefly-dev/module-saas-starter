DROP TRIGGER IF EXISTS principals_bump_authorization_revision ON public.principals;
CREATE TRIGGER principals_bump_authorization_revision
BEFORE INSERT OR UPDATE OF org_id, revoked_at, revoked_reason OR DELETE
ON public.principals
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_principal_mutation();

ALTER TABLE principals DROP CONSTRAINT IF EXISTS principals_disabled_consistency;
ALTER TABLE principals DROP CONSTRAINT IF EXISTS principals_ceiling_agent_only;
ALTER TABLE principals
    DROP COLUMN IF EXISTS disabled_reason,
    DROP COLUMN IF EXISTS disabled_at,
    DROP COLUMN IF EXISTS allowed_scopes,
    DROP COLUMN IF EXISTS allowed_audiences;
