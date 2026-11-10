-- Accounts judges at startup whether this database is at the schema its binary
-- needs, and refuses to serve when it is not (schema_frontier.go). The version
-- table belongs to the migration login and no runtime login could read it, so
-- the check had no way to ask. Read-only on one row: the application's control
-- plane learns which migration the store is at and whether one is running or
-- failed, and can change nothing.
SET LOCAL lock_timeout = '15s';
GRANT SELECT ON TABLE public.schema_migrations TO app_control_plane;
