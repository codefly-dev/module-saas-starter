-- Backfill principals from existing users + api_keys. Idempotent:
-- ON CONFLICT (id) DO NOTHING means re-running the migration on a
-- partially-migrated DB produces no duplicates and no errors. The
-- UNION-of-INSERTs shape isn't strictly atomic; running the whole
-- file inside a transaction (the migration runner's default) makes it so.
--
-- ID equality is the convention. Humans get principals.id =
-- users.uuid; services get principals.id = api_keys.id. Audit events
-- pre-dating the principals table reference these same IDs as
-- actor_id and resolve cleanly going forward.

-- ---------------------------------------------------------------------
-- Humans: one row per active user. Cross-org by design (org_id NULL).
-- The CHECK constraint principals_org_scope enforces this.
-- ---------------------------------------------------------------------
INSERT INTO principals (id, kind, display_name, org_id, agent_identifier, created_at)
SELECT
    u.uuid,
    'human',
    u.primary_email,
    NULL,
    NULL,
    u.created_at
FROM users u
WHERE u.status != 'deleted'
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------------
-- Services: one row per api_key. org_id matches the api_key's org.
-- Revoked api_keys produce revoked principals — callers checking
-- revoked_at on principals get the same answer they'd get from
-- api_keys directly.
-- ---------------------------------------------------------------------
INSERT INTO principals (id, kind, display_name, org_id, agent_identifier, created_at, revoked_at, revoked_reason)
SELECT
    ak.id,
    'service',
    ak.name,
    ak.organization_id,
    NULL,
    ak.created_at,
    ak.revoked_at,
    -- Existing api_keys table doesn't carry a reason; leave NULL.
    -- New revocations through the principals layer can populate it.
    NULL
FROM api_keys ak
ON CONFLICT (id) DO NOTHING;
