DROP TABLE IF EXISTS bootstrap_state;

DROP INDEX IF EXISTS idx_organizations_provider_link;
ALTER TABLE organizations
    DROP COLUMN IF EXISTS provider_org_id,
    DROP COLUMN IF EXISTS provider;

ALTER TABLE sessions           ALTER COLUMN id   SET DEFAULT gen_random_uuid();
ALTER TABLE organizations      ALTER COLUMN id   SET DEFAULT gen_random_uuid();
ALTER TABLE user_identities    ALTER COLUMN uuid SET DEFAULT gen_random_uuid();
ALTER TABLE users              ALTER COLUMN uuid SET DEFAULT gen_random_uuid();
