DROP INDEX IF EXISTS idx_organizations_sso_connection;

ALTER TABLE organizations
    DROP COLUMN IF EXISTS sso_configured_at,
    DROP COLUMN IF EXISTS sso_status,
    DROP COLUMN IF EXISTS sso_organization_id,
    DROP COLUMN IF EXISTS sso_connection_id,
    DROP COLUMN IF EXISTS sso_provider;
