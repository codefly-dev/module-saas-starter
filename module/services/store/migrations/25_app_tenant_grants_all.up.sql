-- Comprehensive GRANTs for app_tenant.
--
-- Migration 24 created the role and granted on existing tables, but
-- on test/dogfood DBs that already had migration 24 applied with
-- earlier (incomplete) content during iteration, some tables ended
-- up un-granted. This migration is the belt-and-suspenders pass:
-- regrant on every table + sequence in public, idempotent.
--
-- Future-table coverage (ALTER DEFAULT PRIVILEGES) was already set
-- by migration 24; this rev only fixes existing tables.

GRANT USAGE ON SCHEMA public TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE
    ON ALL TABLES IN SCHEMA public TO app_tenant;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_tenant;

-- Re-affirm default privileges in case migration 24 ran before the
-- role had been created in some race.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO app_tenant;
