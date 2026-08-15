-- Built-in roles (org_id IS NULL) must have globally unique names. The base
-- schema's UNIQUE(org_id, name) does NOT enforce this: PostgreSQL treats NULLs
-- as distinct in a unique constraint, so two built-ins named 'admin' could
-- coexist. The catalog importer keys built-in roles by name (snapshot + diff),
-- so a concurrent or retried import could otherwise create a duplicate role and
-- corrupt that mapping. This partial unique index closes the gap and also guards
-- the hand-seeded admin/editor/viewer roles.

CREATE UNIQUE INDEX roles_builtin_name_unique ON roles (name) WHERE org_id IS NULL;
