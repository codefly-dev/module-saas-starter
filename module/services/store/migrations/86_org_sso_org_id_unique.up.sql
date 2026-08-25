-- One WorkOS organization maps to exactly one of ours. The identity resolver
-- reverse-maps claims.ProviderOrgID to a tenant through sso_organization_id
-- (see selectOrg): without a uniqueness guarantee two organizations could carry
-- the same id, and that lookup — a LEFT JOIN read of a single row — would
-- resolve an arbitrary tenant or, when the user is a member of only one of the
-- duplicates, non-deterministically deny access. A partial UNIQUE index makes
-- the mapping single-valued at the source; NULL (SSO not configured) is left
-- unconstrained. The same index also serves the auth-time reverse lookup on the
-- login/signup hot path, mirroring idx_organizations_sso_connection.
CREATE UNIQUE INDEX IF NOT EXISTS idx_organizations_sso_organization_id
    ON organizations (sso_organization_id)
    WHERE sso_organization_id IS NOT NULL;
