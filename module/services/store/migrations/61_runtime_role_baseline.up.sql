-- Fail-closed baseline for every application runtime role.
--
-- Earlier migrations granted app_tenant full CRUD and TRUNCATE through default
-- privileges. That made a new table application-writable unless its creating
-- migration remembered to revoke access, and TRUNCATE bypasses row-level
-- security entirely. From this migration forward, every new relation must
-- receive deliberate grants in the migration that creates it.

ALTER ROLE app_tenant
    NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
ALTER ROLE app_billing_worker
    NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
ALTER ROLE app_webhook_worker
    NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;

-- TRUNCATE is table-scoped and never evaluates RLS. Test cleanup already uses
-- the migration principal through the audited bypass boundary, so the tenant
-- role has no legitimate reason to retain it.
REVOKE TRUNCATE ON ALL TABLES IN SCHEMA public FROM app_tenant;

-- Stop implicit authority growth. Existing table grants remain in place until
-- the explicit per-relation grant inventory is completed; new tables are
-- inaccessible until their own migration opts a runtime role in.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES FROM app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE USAGE, SELECT ON SEQUENCES FROM app_tenant;

-- Runtime roles are assumed by an already-authenticated Codefly-managed
-- connection. They do not create schema objects or connect independently.
REVOKE CREATE ON SCHEMA public
    FROM PUBLIC, app_tenant, app_billing_worker, app_webhook_worker;

DO $$
BEGIN
    EXECUTE format(
        'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC, app_tenant, app_billing_worker, app_webhook_worker',
        current_database()
    );
    EXECUTE format(
        'REVOKE CONNECT ON DATABASE %I FROM app_tenant, app_billing_worker, app_webhook_worker',
        current_database()
    );
END $$;
