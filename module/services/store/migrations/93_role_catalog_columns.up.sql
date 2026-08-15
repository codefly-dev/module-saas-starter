-- Two facts the built-in role catalog importer needs that the migration-4 seed
-- schema didn't carry:
--
--   * scope: the default role_assignments.scope recorded when a catalog role is
--     later assigned. NULL preserves the pre-catalog "global within org"
--     default. Assignment-time derivation lands with strict scope semantics.
--   * catalog_managed: provenance. The importer diff-applies ONLY rows it owns,
--     so the hand-seeded admin/editor/viewer built-ins (catalog_managed = false)
--     are never rewritten or removed unless a catalog explicitly names them.
--
-- Both columns are additive and nullable/defaulted, so existing readers and the
-- roles RLS policies (migrations 32, 65) are unaffected.

ALTER TABLE roles ADD COLUMN scope TEXT;
ALTER TABLE roles ADD COLUMN catalog_managed BOOLEAN NOT NULL DEFAULT false;
