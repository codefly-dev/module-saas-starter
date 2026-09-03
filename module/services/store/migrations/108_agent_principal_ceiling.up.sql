-- Per-org non-human (agent) Principal delegated-execution ceiling + reversible
-- disable (issue #440).
--
-- allowed_audiences / allowed_scopes are the ceiling an org admin declares when
-- registering an agent principal: the Work Context audiences the actor may be
-- minted for and the resource kinds it may request. Empty = unrestricted. Both
-- are meaningful only for kind='agent'. They follow the schema-wide TEXT[]
-- convention (NOT NULL DEFAULT '{}', e.g. org_identity_providers.allowed_email_domains):
-- an absent ceiling is the empty array, never NULL.
--
-- disabled_at is a REVERSIBLE suspension, distinct from the terminal revoked_at:
-- while set, the actor resolves as inactive so no new Work Context mints with it
-- and outstanding ones go stale. The principals authorization-revision trigger
-- is widened below to fire on disabled_at so a disable takes immediate effect on
-- signed contexts, exactly as a revoke does.

ALTER TABLE principals
    ADD COLUMN allowed_audiences TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN allowed_scopes    TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN disabled_at       TIMESTAMP WITH TIME ZONE,
    ADD COLUMN disabled_reason   TEXT;

-- The ceiling is an agent-only concept; every other kind keeps the empty default.
ALTER TABLE principals ADD CONSTRAINT principals_ceiling_agent_only CHECK (
    kind = 'agent' OR (cardinality(allowed_audiences) = 0 AND cardinality(allowed_scopes) = 0)
);

-- disabled_reason is only meaningful alongside disabled_at, and the reversible
-- suspension itself only applies to delegated actors.
ALTER TABLE principals ADD CONSTRAINT principals_disabled_consistency CHECK (
    (disabled_at IS NULL AND disabled_reason IS NULL) OR
    (disabled_at IS NOT NULL AND kind = 'agent')
);

-- Widen the revision-bump trigger to fire on disable/enable so a suspended
-- actor's outstanding Work Contexts go stale immediately. Migration 78 defined
-- the function; only the firing column list changes here.
DROP TRIGGER IF EXISTS principals_bump_authorization_revision ON public.principals;
CREATE TRIGGER principals_bump_authorization_revision
BEFORE INSERT OR UPDATE OF org_id, revoked_at, revoked_reason, disabled_at, disabled_reason OR DELETE
ON public.principals
FOR EACH ROW EXECUTE FUNCTION public.authorization_revision_principal_mutation();
