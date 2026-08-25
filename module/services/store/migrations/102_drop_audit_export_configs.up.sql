-- Retire the S3/minio audit-export feature: drop its table.
--
-- The feature (per-org audit-log export sink) is removed wholesale in
-- this release. Migrations 19 (CREATE) and 23 (RLS) stay untouched as
-- immutable history — instead this forward migration removes the table.
--
-- This matters for EXISTING deployments: audit_export_configs holds
-- customer S3 credentials, including secret_access_key in cleartext (see
-- migration 19). Deleting the historical CREATE migration would leave
-- that table — and those secrets — stranded on every already-migrated
-- database, with no code path left to read, rotate, or delete them (the
-- Go store methods, admin UI, and RPCs are all removed in this release).
-- Dropping it here is what actually purges the secret material.
--
-- CASCADE removes the tenant RLS policy and index along with the table.
-- Fresh databases created 19→101 make the table and then drop it here;
-- already-migrated databases drop it on upgrade. Both converge to "no
-- audit_export_configs", matching the removed application code.

DROP TABLE IF EXISTS audit_export_configs CASCADE;
