-- Accounts-owned empty-baseline catalog observation, policy version 1. No application rows,
-- DDL, grants, role changes or repair. All substitutions are bound by the renderer.
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL search_path = pg_catalog;
SET LOCAL lock_timeout = '3s';
SET LOCAL row_security = off;
-- Fail before any ledger access on another target, impersonation or pre-PG16.
SELECT 1 / CASE WHEN current_database() = __DATABASE__
 AND current_user = __MIGRATOR__ AND session_user = __MIGRATOR__
 AND current_setting('server_version_num')::integer >= __MIN_SERVER_VERSION__
 THEN 1 ELSE 0 END AS target_guard
\gset
SELECT to_regclass(__LEDGER_REGCLASS__) IS NOT NULL AS ledger_present,
 EXISTS (
  SELECT FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = __SCHEMA__ AND c.relname = __LEDGER_NAME__
   AND c.relkind = 'r' AND NOT c.relispartition AND NOT c.relrowsecurity
   AND NOT EXISTS (SELECT FROM pg_inherits i WHERE i.inhparent = c.oid OR i.inhrelid = c.oid)
   AND has_table_privilege(current_user, c.oid, 'SELECT')
   AND (SELECT count(*) FROM pg_attribute a WHERE a.attrelid = c.oid
        AND a.attnum > 0 AND NOT a.attisdropped) = 2
   AND EXISTS (SELECT FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attnum > 0
               AND NOT a.attisdropped AND a.attname = __LEDGER_VERSION_COLUMN__ AND a.atttypid = __LEDGER_VERSION_TYPE__::regtype)
   AND EXISTS (SELECT FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attnum > 0
               AND NOT a.attisdropped AND a.attname = __LEDGER_DIRTY_COLUMN__ AND a.atttypid = __LEDGER_DIRTY_TYPE__::regtype)
 ) AS ledger_queryable
\gset
\if :ledger_queryable
SELECT COALESCE(json_agg(t), '[]'::json)::text AS ledger_rows
 FROM (SELECT __LEDGER_VERSION_IDENTIFIER__ AS version, __LEDGER_DIRTY_IDENTIFIER__ AS dirty FROM __LEDGER_QUALIFIED__ LIMIT 2) t
\gset
\else
\set ledger_rows 'null'
\endif
WITH scoped_roles AS (
 SELECT * FROM pg_roles WHERE rolname IN (__ROLE_NAMES__)
), relations AS (
 SELECT c.relname::text AS name FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = __SCHEMA__ AND c.relkind IN ('r','p','v','m','f','S')
  AND c.relname <> __LEDGER_NAME__
  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
                  AND d.objid = c.oid AND d.deptype = 'e')
), functions AS (
 SELECT p.proname::text || ':' || p.oid::text AS name FROM pg_proc p
 JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = __SCHEMA__
  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass
                  AND d.objid = p.oid AND d.deptype = 'e')
), types AS (
 SELECT t.typname::text AS name FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
 WHERE n.nspname = __SCHEMA__ AND t.typname IN (__TYPE_NAMES__)
), edges AS (
 SELECT granted.rolname::text AS role, member.rolname::text AS member,
        grantor.rolname::text AS grantor,
        m.inherit_option AS inherit, m.set_option AS set, m.admin_option AS admin
 FROM pg_auth_members m JOIN pg_roles granted ON granted.oid = m.roleid
 JOIN pg_roles member ON member.oid = m.member JOIN pg_roles grantor ON grantor.oid = m.grantor
 WHERE m.roleid IN (SELECT oid FROM scoped_roles) OR m.member IN (SELECT oid FROM scoped_roles)
)
SELECT json_build_object(
 'contract', __OBSERVATION_CONTRACT__,
 'database', current_database(), 'migration_role', current_user, 'session_user', session_user,
 -- Instance is a declared proxy binding, not an independent server attestation.
 'instance', __INSTANCE__, 'handoff_sha256', __HANDOFF__,
 'observed_at', clock_timestamp(), 'server_version_num', current_setting('server_version_num')::integer,
 'server_address', inet_server_addr()::text, 'server_port', inet_server_port(),
 'transaction_read_only', current_setting('transaction_read_only') = 'on',
 'transaction_isolation', current_setting('transaction_isolation'),
 'migrator', json_build_object(
  'login', r.rolcanlogin, 'superuser', r.rolsuper, 'bypass_rls', r.rolbypassrls,
  'create_role', r.rolcreaterole, 'create_db', r.rolcreatedb,
  'replication', r.rolreplication, 'inherit', r.rolinherit,
  'database_owner', pg_get_userbyid(db.datdba),
  'schema_owner', (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = __SCHEMA__),
  'database_owner_usable', pg_has_role(current_user, db.datdba, 'USAGE'),
  'schema_owner_usable', COALESCE((SELECT pg_has_role(current_user, nspowner, 'USAGE')
                                 FROM pg_namespace WHERE nspname = __SCHEMA__), false),
  'schema_usage', has_schema_privilege(current_user, __SCHEMA__, 'USAGE'),
  'schema_create', has_schema_privilege(current_user, __SCHEMA__, 'CREATE'),
  'database_create', has_database_privilege(current_user, current_database(), 'CREATE'),
  'elevated_membership', EXISTS (SELECT FROM pg_roles x
    WHERE (x.rolsuper OR x.rolbypassrls OR x.rolreplication) AND pg_has_role(current_user, x.oid, 'MEMBER'))
 ),
 'objects', json_build_object(
  'relations', json_build_object('count', (SELECT count(*) FROM relations),
    'names', (SELECT COALESCE(json_agg(name), '[]'::json) FROM (SELECT name FROM relations ORDER BY name LIMIT 16) t)),
  'functions', json_build_object('count', (SELECT count(*) FROM functions),
    'names', (SELECT COALESCE(json_agg(name), '[]'::json) FROM (SELECT name FROM functions ORDER BY name LIMIT 16) t)),
  'types', json_build_object('count', (SELECT count(*) FROM types),
    'names', (SELECT COALESCE(json_agg(name), '[]'::json) FROM (SELECT name FROM types ORDER BY name LIMIT 16) t))
 ),
 'ledger', json_build_object('present', :'ledger_present'::boolean,
                            'queryable', :'ledger_queryable'::boolean, 'rows', :'ledger_rows'::json),
 'roles', (SELECT COALESCE(json_agg(json_build_object(
   'name', a.rolname, 'login', a.rolcanlogin, 'superuser', a.rolsuper,
   'bypass_rls', a.rolbypassrls, 'create_role', a.rolcreaterole, 'create_db', a.rolcreatedb,
   'replication', a.rolreplication, 'inherit', a.rolinherit,
   'can_admin', pg_has_role(current_user, a.oid, 'MEMBER WITH ADMIN OPTION'),
   'owner_membership', pg_has_role(a.oid, db.datdba, 'MEMBER') OR pg_has_role(a.oid, r.oid, 'MEMBER'),
   'elevated_membership', EXISTS (SELECT FROM pg_roles x
     WHERE (x.rolsuper OR x.rolbypassrls OR x.rolreplication OR x.rolcreaterole OR x.rolcreatedb)
      AND pg_has_role(a.oid, x.oid, 'MEMBER'))
  ) ORDER BY a.rolname), '[]'::json) FROM scoped_roles a),
 'memberships', json_build_object('count', (SELECT count(*) FROM edges),
   'edges', (SELECT COALESCE(json_agg(t), '[]'::json) FROM
             (SELECT * FROM edges ORDER BY role, member, grantor LIMIT 32) t)),
 'extensions', (SELECT json_agg(json_build_object(
   'name', wanted.name, 'installed_version', installed.extversion, 'installed_schema', ns.nspname,
   'default_version', available.default_version, 'available_version', version.version,
   'superuser', version.superuser, 'trusted', version.trusted, 'required_schema', version.schema,
   'requires', version.requires,
   'requirements_installed', NOT EXISTS (SELECT FROM unnest(version.requires) dep(name)
                                         WHERE NOT EXISTS (SELECT FROM pg_extension e WHERE e.extname = dep.name))
  ) ORDER BY wanted.name)
  FROM (VALUES __EXTENSION_NAMES__) wanted(name)
  LEFT JOIN pg_extension installed ON installed.extname = wanted.name
  LEFT JOIN pg_namespace ns ON ns.oid = installed.extnamespace
  LEFT JOIN pg_available_extensions available ON available.name = wanted.name
  LEFT JOIN pg_available_extension_versions version ON version.name = wanted.name
                                                     AND version.version = available.default_version)
)
FROM pg_roles r CROSS JOIN pg_database db
WHERE r.rolname = current_user AND db.datname = current_database();
ROLLBACK;
