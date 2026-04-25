-- Per-org self-serve SSO state. Empty connection_id means SSO is
-- not configured for this org; status mirrors the WorkOS Connections
-- lifecycle ("linked" once portal session minted, "active" once IdP
-- setup complete, "disabled" if admin paused).
--
-- We don't store the customer's IdP secrets — those live in WorkOS.
-- We just stash the connection_id (used to authorize SSO logins via
-- /authorize?connection=) and the WorkOS organization_id (used to
-- mint subsequent admin-portal links for the same customer).

ALTER TABLE organizations
    ADD COLUMN IF NOT EXISTS sso_provider        TEXT,
    ADD COLUMN IF NOT EXISTS sso_connection_id   TEXT,
    ADD COLUMN IF NOT EXISTS sso_organization_id TEXT,
    ADD COLUMN IF NOT EXISTS sso_status          TEXT,
    ADD COLUMN IF NOT EXISTS sso_configured_at   TIMESTAMPTZ;

-- Index for the auth-time lookup: when a user with an email in an
-- org's domain attempts to sign in, the auth flow checks for an
-- active connection_id. Indexed for that hot path.
CREATE INDEX IF NOT EXISTS idx_organizations_sso_connection
    ON organizations (sso_connection_id)
    WHERE sso_connection_id IS NOT NULL;
