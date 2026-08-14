ALTER TABLE organizations
    DROP COLUMN IF EXISTS sso_provision_mode,
    DROP COLUMN IF EXISTS sso_default_role,
    DROP COLUMN IF EXISTS sso_allowed_email_domains;
