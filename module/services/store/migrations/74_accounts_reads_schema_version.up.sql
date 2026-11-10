-- Accounts judges at startup whether this database is at the schema its binary
-- needs, and refuses to serve when it is not (schema_frontier.go). The version
-- table belongs to the migration login and no runtime login could read it, so
-- the check had no way to ask. Read-only on one row: the application's control
-- plane learns which migration the store is at and whether one is running or
-- failed, and can change nothing.
SET LOCAL lock_timeout = '15s';
-- The ledger table belongs to the migration engine. A replay that applies these
-- files without the engine (the qualification stages) has none, and accounts
-- reads it only where the engine ran, so the grant waits for the table.
DO $grant$ BEGIN
 IF to_regclass('public.schema_migrations') IS NOT NULL THEN
  GRANT SELECT ON TABLE public.schema_migrations TO app_control_plane;
 END IF;
END $grant$;
