-- Phase 1 of production-ready: drop server-side uuid defaults (business layer
-- now generates uuid v7 explicitly), add orgs→provider link, add bootstrap_state
-- singleton for first-super-admin tracking.

-- 1. Drop DEFAULT gen_random_uuid() on every id column. The business layer
--    now calls business.NewID() (uuid v7) and passes it explicitly.
ALTER TABLE users              ALTER COLUMN uuid DROP DEFAULT;
ALTER TABLE user_identities    ALTER COLUMN uuid DROP DEFAULT;
ALTER TABLE organizations      ALTER COLUMN id   DROP DEFAULT;
ALTER TABLE sessions           ALTER COLUMN id   DROP DEFAULT;

-- Snapshot org/role state on the sessions row so refresh rotation can
-- reconstitute an Identity without re-querying membership tables. Roles
-- inside an access token are valid for AccessTokenTTL (15 min) and
-- re-resolved on next /auth/refresh — acceptable staleness window.
ALTER TABLE sessions
    ADD COLUMN org_id        UUID,
    ADD COLUMN org_role      TEXT NOT NULL DEFAULT '',
    ADD COLUMN platform_role TEXT NOT NULL DEFAULT '';

-- 2. Link organizations to an external identity provider org (WorkOS et al).
--    Nullable — orgs created outside an SSO context don't need one.
ALTER TABLE organizations
    ADD COLUMN provider        TEXT,
    ADD COLUMN provider_org_id TEXT;

CREATE INDEX idx_organizations_provider_link
    ON organizations(provider, provider_org_id)
    WHERE provider_org_id IS NOT NULL;

-- 3. Bootstrap singleton: tracks whether the first super_admin has been
--    provisioned via BOOTSTRAP_ADMIN_EMAIL. The single row is inserted at
--    migration time so the resolver can always UPDATE it transactionally.
CREATE TABLE bootstrap_state (
    id              SMALLINT PRIMARY KEY CHECK (id = 1),
    bootstrapped_at TIMESTAMP WITH TIME ZONE
);

INSERT INTO bootstrap_state (id) VALUES (1) ON CONFLICT DO NOTHING;

-- 4. Seed "dev" as a known identity provider so the local dev validator
--    can insert user_identities rows without a FK violation.
INSERT INTO identity_providers (provider_id, name) VALUES ('dev', 'Dev')
ON CONFLICT (provider_id) DO NOTHING;
