-- Postgres role + privileges for the RLS code path.
--
-- Why a dedicated role: postgres SUPERUSERS AND ROLES WITH BYPASSRLS
-- BYPASS RLS UNCONDITIONALLY, even with FORCE ROW LEVEL SECURITY.
-- Codefly's Postgres plugin connects the api as a superuser, which
-- silently defeats every policy. Solution: WithOrgTx switches to
-- this non-superuser non-BYPASSRLS role for the duration of the
-- request transaction; SET LOCAL restores the original role on
-- commit/rollback. WithBypass leaves the role as the default
-- (superuser) so workers naturally bypass RLS.
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

-- Allow the connection's user (typically the codefly-managed
-- superuser) to assume app_tenant via SET ROLE. Superusers can SET
-- ROLE without an explicit grant in most cases, but being explicit
-- documents the contract for non-superuser-deployed setups.
DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format('GRANT app_tenant TO %I', cur);
END $$;
