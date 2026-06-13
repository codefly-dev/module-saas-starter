-- principals: the unified identity table for everything saas-starter
-- authorizes — humans, services, and agents.
--
-- Why one table for all three kinds:
--   - Same RBAC machinery (role_assignments, role_permissions) covers
--     all of them, with no special-casing in CheckPermission/Decide.
--   - Audit events reference principals uniformly, so "what did X do
--     last week" works the same whether X is a user or a bot.
--   - Delegation grants (M7+) can lend authority between any two
--     principals — a human approving an agent's escalation request
--     uses the same token primitive as a user invoking that agent.
--
-- ID strategy:
--   - For humans, principals.id == users.uuid (1:1 by convention).
--   - For services derived from api_keys, principals.id == api_keys.id.
--   - For new agents, principals.id is a fresh UUID.
-- We do NOT enforce these as foreign keys because users + api_keys
-- have their own lifecycles; tying them at the FK level would force
-- cascading-delete semantics we don't want. The migration that
-- backfills enforces ID equality where applicable.
--
-- Org scoping:
--   - Humans: org_id IS NULL — humans are cross-org global identities
--     (matching the existing users + organization_members model).
--   - Services / agents: org_id IS NOT NULL — every service principal
--     belongs to exactly one org. This matches api_keys.organization_id
--     and gives org admins clean control over their automation.
CREATE TABLE IF NOT EXISTS "principals" (
    id               UUID PRIMARY KEY,
    kind             TEXT NOT NULL,
    display_name     TEXT NOT NULL,
    -- org_id NULL for humans, NOT NULL for services + agents.
    -- Enforced via CHECK below.
    org_id           UUID REFERENCES organizations(id) ON DELETE CASCADE,
    -- agent_identifier is "publisher/name:version" for kind=agent;
    -- NULL otherwise. Lets the CLI correlate a principal back to a
    -- specific plugin manifest at install time (which is when the
    -- agent's permissions are reviewed and approved).
    agent_identifier TEXT,
    created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at       TIMESTAMP WITH TIME ZONE,
    revoked_reason   TEXT,

    CONSTRAINT principals_kind_valid CHECK (kind IN ('human', 'service', 'agent')),

    -- Humans are org-less (cross-org identities); services + agents
    -- are always org-scoped. Stops malformed rows at insert time.
    CONSTRAINT principals_org_scope CHECK (
        (kind = 'human' AND org_id IS NULL) OR
        (kind IN ('service', 'agent') AND org_id IS NOT NULL)
    ),

    -- agent_identifier required for agents; absent otherwise.
    CONSTRAINT principals_agent_identifier_consistency CHECK (
        (kind = 'agent' AND agent_identifier IS NOT NULL AND agent_identifier <> '') OR
        (kind <> 'agent' AND agent_identifier IS NULL)
    ),

    -- revoked_reason only meaningful when revoked_at is set.
    CONSTRAINT principals_revoked_reason_consistency CHECK (
        (revoked_at IS NULL AND revoked_reason IS NULL) OR
        (revoked_at IS NOT NULL)
    )
);

-- Lookup by kind within an org (e.g. "list all agents in this org").
CREATE INDEX IF NOT EXISTS principals_org_kind_idx
    ON principals (org_id, kind)
    WHERE revoked_at IS NULL;

-- Two agents in the same org cannot share the same canonical
-- identifier (publisher/name:version). Across orgs we DO allow the
-- same agent identifier — different installations of the same
-- published agent are independent principals.
CREATE UNIQUE INDEX IF NOT EXISTS principals_agent_identifier_org_idx
    ON principals (org_id, agent_identifier)
    WHERE kind = 'agent' AND revoked_at IS NULL;

-- Lookup by display name within an org. Not unique (multiple humans
-- could share a display name); just a hint for list/search RPCs.
CREATE INDEX IF NOT EXISTS principals_display_name_idx
    ON principals (org_id, lower(display_name))
    WHERE revoked_at IS NULL;

COMMENT ON TABLE principals IS
    'Unified identity table: humans, services, and agents. The auth ' ||
    'layer (role_assignments, CheckPermission, Decide) treats them ' ||
    'uniformly. ID matches users.uuid or api_keys.id where applicable.';
