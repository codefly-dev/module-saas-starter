-- Restore the pre-61 runtime-role defaults. This is intentionally a complete
-- rollback of the migration, including the unsafe implicit grants.

DO $$
BEGIN
    EXECUTE format(
        'GRANT TEMPORARY ON DATABASE %I TO PUBLIC',
        current_database()
    );
    EXECUTE format(
        'GRANT CONNECT ON DATABASE %I TO app_tenant, app_billing_worker, app_webhook_worker',
        current_database()
    );
END $$;

GRANT CREATE ON SCHEMA public TO PUBLIC;
GRANT TRUNCATE ON ALL TABLES IN SCHEMA public TO app_tenant;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO app_tenant;
