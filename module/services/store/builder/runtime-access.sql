\set ON_ERROR_STOP on
\getenv owner_user POSTGRES_USER
\getenv read_only_password POSTGRES_READ_ONLY_PASSWORD
\getenv read_write_password POSTGRES_READ_WRITE_PASSWORD

BEGIN;
SELECT pg_advisory_xact_lock(hashtext('codefly-runtime-access:' || current_database()));

SELECT format('CREATE ROLE %I', 'codefly_users_7dfb4cf6_ro')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('CREATE ROLE %I', 'codefly_users_7dfb4cf6_rw')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'codefly_users_7dfb4cf6_rw')
\gexec

SELECT format(
    'ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD %L',
    'codefly_users_7dfb4cf6_ro', :'read_only_password')
\gexec
SELECT format(
    'ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD %L',
    'codefly_users_7dfb4cf6_rw', :'read_write_password')
\gexec
SELECT format('ALTER ROLE %I SET default_transaction_read_only = on', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('ALTER ROLE %I RESET default_transaction_read_only', 'codefly_users_7dfb4cf6_rw')
\gexec

SELECT format('REVOKE ALL PRIVILEGES ON DATABASE %I FROM %I', current_database(), 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON DATABASE %I FROM %I', current_database(), 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('REVOKE CREATE ON SCHEMA %I FROM PUBLIC', 'public')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON SCHEMA %I FROM %I', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON SCHEMA %I FROM %I', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('GRANT USAGE ON SCHEMA %I TO %I', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('GRANT USAGE ON SCHEMA %I TO %I', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA %I FROM %I', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA %I FROM %I', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA %I FROM %I', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA %I FROM %I', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO %I', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %I TO %I', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec

SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON TABLES FROM %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON TABLES FROM %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON SEQUENCES FROM %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON SEQUENCES FROM %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT ON TABLES TO %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_ro')
\gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %I', :'owner_user', 'public', 'codefly_users_7dfb4cf6_rw')
\gexec

-- The managed login owns no application capabilities implicitly. Reconcile
-- its SET ROLE allow-list from service configuration on every deployment.
SELECT format('REVOKE %I FROM %I', granted.rolname, 'codefly_users_7dfb4cf6_rw')
FROM pg_auth_members membership
JOIN pg_roles granted ON granted.oid = membership.roleid
JOIN pg_roles principal ON principal.oid = membership.member
WHERE principal.rolname = 'codefly_users_7dfb4cf6_rw'
\gexec
DO $codefly$
DECLARE
    target pg_roles%ROWTYPE;
BEGIN
    SELECT * INTO target FROM pg_roles WHERE rolname = 'app_tenant';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'configured runtime read-write role % does not exist; create it in a migration', 'app_tenant';
    END IF;
    IF target.rolcanlogin OR target.rolsuper OR target.rolcreatedb OR target.rolcreaterole THEN
        RAISE EXCEPTION 'configured runtime read-write role % must be NOLOGIN, NOSUPERUSER, NOCREATEDB, and NOCREATEROLE', 'app_tenant';
    END IF;
    EXECUTE format('GRANT %I TO %I', 'app_tenant', 'codefly_users_7dfb4cf6_rw');
END;
$codefly$;
DO $codefly$
DECLARE
    target pg_roles%ROWTYPE;
BEGIN
    SELECT * INTO target FROM pg_roles WHERE rolname = 'app_control_plane';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'configured runtime read-write role % does not exist; create it in a migration', 'app_control_plane';
    END IF;
    IF target.rolcanlogin OR target.rolsuper OR target.rolcreatedb OR target.rolcreaterole THEN
        RAISE EXCEPTION 'configured runtime read-write role % must be NOLOGIN, NOSUPERUSER, NOCREATEDB, and NOCREATEROLE', 'app_control_plane';
    END IF;
    EXECUTE format('GRANT %I TO %I', 'app_control_plane', 'codefly_users_7dfb4cf6_rw');
END;
$codefly$;
DO $codefly$
DECLARE
    target pg_roles%ROWTYPE;
BEGIN
    SELECT * INTO target FROM pg_roles WHERE rolname = 'app_billing_worker';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'configured runtime read-write role % does not exist; create it in a migration', 'app_billing_worker';
    END IF;
    IF target.rolcanlogin OR target.rolsuper OR target.rolcreatedb OR target.rolcreaterole THEN
        RAISE EXCEPTION 'configured runtime read-write role % must be NOLOGIN, NOSUPERUSER, NOCREATEDB, and NOCREATEROLE', 'app_billing_worker';
    END IF;
    EXECUTE format('GRANT %I TO %I', 'app_billing_worker', 'codefly_users_7dfb4cf6_rw');
END;
$codefly$;
DO $codefly$
DECLARE
    target pg_roles%ROWTYPE;
BEGIN
    SELECT * INTO target FROM pg_roles WHERE rolname = 'app_webhook_worker';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'configured runtime read-write role % does not exist; create it in a migration', 'app_webhook_worker';
    END IF;
    IF target.rolcanlogin OR target.rolsuper OR target.rolcreatedb OR target.rolcreaterole THEN
        RAISE EXCEPTION 'configured runtime read-write role % must be NOLOGIN, NOSUPERUSER, NOCREATEDB, and NOCREATEROLE', 'app_webhook_worker';
    END IF;
    EXECUTE format('GRANT %I TO %I', 'app_webhook_worker', 'codefly_users_7dfb4cf6_rw');
END;
$codefly$;
DO $codefly$
DECLARE
    target pg_roles%ROWTYPE;
BEGIN
    SELECT * INTO target FROM pg_roles WHERE rolname = 'app_job_worker';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'configured runtime read-write role % does not exist; create it in a migration', 'app_job_worker';
    END IF;
    IF target.rolcanlogin OR target.rolsuper OR target.rolcreatedb OR target.rolcreaterole THEN
        RAISE EXCEPTION 'configured runtime read-write role % must be NOLOGIN, NOSUPERUSER, NOCREATEDB, and NOCREATEROLE', 'app_job_worker';
    END IF;
    EXECUTE format('GRANT %I TO %I', 'app_job_worker', 'codefly_users_7dfb4cf6_rw');
END;
$codefly$;

COMMIT;
