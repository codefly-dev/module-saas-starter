-- Postgres role + privileges for the RLS code path.
--
-- Why a dedicated role: request traffic must always enter through a
-- non-superuser, non-BYPASSRLS role so policies cannot be skipped by
-- accident. Codefly exports a non-owner read-write principal and grants
-- it permission to SET ROLE app_tenant through the Postgres service's
-- runtime-read-write-roles setting. WithBypass returns to that managed
-- principal and opts into the explicitly audited app.bypass policy path.
--
-- Idempotent: CREATE ROLE under DO $$ ... IF NOT EXISTS, GRANTs are
-- repeat-safe.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_tenant') THEN
        CREATE ROLE app_tenant NOINHERIT;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE
    ON ALL TABLES IN SCHEMA public TO app_tenant;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_tenant;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO app_tenant;

-- The migration owner also gets membership for migrations and integration
-- tests. The Codefly Postgres agent separately reconciles membership for its
-- managed read-write principal after all migrations have run.
DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format('GRANT app_tenant TO %I', cur);
END $$;
