#!/usr/bin/env python3
"""Export the store's schema as its one migration.

The ledger is a single baseline: the schema, seed rows, roles, grants, policies
and function owners of an empty database, exported from a disposable PostgreSQL
16 container that has the current migration set applied. Re-run it whenever the
schema changes; the ledger carries no upgrade path, so a database migrated by an
earlier baseline is recreated, never migrated forward.

Only a disposable network-isolated container is touched. Never connects through
an ambient DSN.
"""
import argparse, hashlib, json, re, subprocess, time, uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
MIGRATIONS = ROOT / 'module/services/store/migrations'
PROVENANCE = ROOT / 'module/services/store/baseline.provenance.json'
IMAGE = 'postgres@sha256:fe03a7605299a34ddf5e4f285dff78c3d7190a576b3c6b46f2fcff69f4bffd54'
ROLES = ['app_tenant', 'app_control_plane', 'app_billing_worker', 'app_webhook_worker', 'app_job_worker']
BASELINE = '1_baseline'


def run(args, **kwargs):
    p = subprocess.run(args, text=True, capture_output=True, timeout=180, **kwargs)
    if p.returncode:
        raise RuntimeError(p.stderr[-3000:])
    return p.stdout


def sql(container, body):
    return run(['docker', 'exec', '-i', container, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres'], input=body)


def ledger(ref):
    """The migrations to fold, in version order: (repo path, text) pairs and every file's hash.

    From the working tree by default; from a committed ref when the tree has
    already been folded and the source of record is the last commit that held
    the ledger."""
    prefix = 'module/services/store/migrations/'
    if ref:
        names = [line for line in run(['git', 'ls-tree', '-r', '--name-only', ref, '--', prefix], cwd=ROOT).splitlines() if line.endswith('.sql')]
        read = lambda path: run(['git', 'show', f'{ref}:{path}'], cwd=ROOT)
    else:
        names = [str(p.relative_to(ROOT)) for p in MIGRATIONS.glob('*.sql')]
        read = lambda path: (ROOT / path).read_text()
    names.sort(key=lambda path: (int(Path(path).name.split('_')[0]), path))
    texts = {path: read(path) for path in names}
    hashes = {path: hashlib.sha256(text.encode()).hexdigest() for path, text in texts.items()}
    ups = [(path, texts[path]) for path in names if path.endswith('.up.sql')]
    return ups, hashes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--from-ref', help='fold the ledger committed at this git ref instead of the working tree')
    args = parser.parse_args()
    name = 'store-baseline-' + uuid.uuid4().hex[:10]
    run(['docker', 'image', 'inspect', IMAGE])
    ups, hashes = ledger(args.from_ref)
    source = run(['git', 'rev-parse', args.from_ref or 'HEAD'], cwd=ROOT).strip()
    frontier = max(int(Path(path).name.split('_')[0]) for path, _ in ups)
    try:
        run(['docker', 'run', '-d', '--name', name, '--network', 'none', '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', IMAGE])
        for _ in range(160):
            try:
                if 'PostgreSQL init process complete' in run(['docker', 'logs', name]):
                    sql(name, 'SELECT 1')
                    break
            except RuntimeError:
                pass
            time.sleep(.25)
        else:
            raise RuntimeError('disposable database failed to start')
        for _, text in ups:
            sql(name, text)
        owners = json.loads(sql(name, """SELECT coalesce(json_agg(x ORDER BY signature),'[]') FROM (
SELECT p.oid::regprocedure::text AS signature, r.rolname AS owner
FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles r ON r.oid=p.proowner
WHERE n.nspname='public' AND r.rolname<>'postgres') x;"""))
        # Seed identifiers are deterministic per natural key, and seeded
        # timestamps become install time, so the export does not freeze the
        # build machine's clock or its random UUIDs into every installation.
        id_map = {}
        time_values = set()
        for table, key in [('roles', 'name'), ('plans', 'name'), ('email_templates', 'name'), ('data_retention_policies', 'resource_type')]:
            for row in json.loads(sql(name, f"SELECT json_agg(t) FROM public.{table} t")):
                id_map[row['id']] = str(uuid.uuid5(uuid.NAMESPACE_URL, 'codefly.dev/accounts/baseline/' + table + '/' + row[key]))
            columns = sql(name, f"SELECT attname FROM pg_attribute WHERE attrelid='public.{table}'::regclass AND atttypid IN ('timestamptz'::regtype,'timestamp'::regtype) AND attnum>0 AND NOT attisdropped").split()
            for col in columns:
                time_values.update(sql(name, f'SELECT DISTINCT {col}::text FROM public.{table} WHERE {col} IS NOT NULL').splitlines())
        time_values.update(sql(name, "SELECT DISTINCT updated_at::text FROM audit_event_types WHERE updated_at IS NOT NULL").splitlines())
        time_values.update(sql(name, "SELECT DISTINCT created_at::text FROM identity_providers WHERE created_at IS NOT NULL").splitlines())
        # Monthly partitions are provisioned relative to installation time by
        # the canonical helper, not frozen to the build machine's calendar.
        for table in sql(name, "SELECT c.relname FROM pg_class c JOIN pg_inherits i ON i.inhrelid=c.oid WHERE i.inhparent='public.audit_events'::regclass").splitlines():
            assert re.fullmatch('audit_events_[0-9]{4}_[0-9]{2}', table)
            sql(name, 'DROP TABLE public.' + table)
        dump = run(['docker', 'exec', name, 'pg_dump', '-U', 'postgres', '--no-owner', '--inserts', '--rows-per-insert=100', '--dbname=postgres'])
        for old, new in id_map.items():
            dump = dump.replace(old, new)
        for value in sorted(time_values, reverse=True):
            dump = dump.replace("'" + value + "'", 'CURRENT_TIMESTAMP')
        dump = re.sub(r'^SET (?:statement_timeout|lock_timeout|idle_in_transaction_session_timeout) = .*;\n', '', dump, flags=re.M)
        dump = re.sub(r'^COMMENT ON EXTENSION .*;\n', '', dump, flags=re.M)
        dump = re.sub(r'^\\(?:un)?restrict .*\n', '', dump, flags=re.M)
        dump = dump.replace('ALTER DEFAULT PRIVILEGES FOR ROLE postgres ', 'ALTER DEFAULT PRIVILEGES FOR ROLE CURRENT_USER ')
        # The endpoint-read policy names the role that owns subscription sync — the
        # migration owner, whoever runs this baseline — so the owner is resolved at
        # install time rather than frozen to the generator's superuser.
        owner_policy = re.compile(r'^CREATE POLICY webhook_subscriptions_migration_owner_read ON public\.webhook_subscriptions .*\n', re.M)
        assert owner_policy.search(dump)
        dump = owner_policy.sub('', dump)
        # The migration engine's own ledger is not part of the schema.
        dump = re.sub(r'^--\n-- Name: schema_migrations;.*?\n\n\n', '', dump, flags=re.S | re.M)
        dump = re.sub(r'^--\n-- Data for Name: schema_migrations;.*?\n\n\n', '', dump, flags=re.S | re.M)
        dump = re.sub(r'^--\n-- Name: schema_migrations schema_migrations_pkey;.*?\n\n\n', '', dump, flags=re.S | re.M)
        prefix = f'''-- The store's one migration: the schema of an empty database, exported by
-- module/services/store/tools/generate_baseline.py. Regenerate it; never edit it.
-- Folded {len(ups)} migrations (through {frontier}) at {source[:12]}; provenance:
-- module/services/store/baseline.provenance.json.
DO $guard$ BEGIN
 IF current_setting('server_version_num')::int < 160000 THEN
  RAISE EXCEPTION 'the store baseline requires PostgreSQL 16 or later';
 END IF;
 IF EXISTS (SELECT FROM pg_roles WHERE rolname IN ('app_tenant','app_control_plane',
 'app_billing_worker','app_webhook_worker','app_job_worker')) THEN
  RAISE EXCEPTION 'the store baseline requires absent application roles: this database was installed by an earlier ledger; recreate it';
 END IF;
 IF EXISTS (SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f','S') AND c.relname<>'schema_migrations'
 AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid='pg_class'::regclass
 AND d.objid=c.oid AND d.deptype='e')) THEN
  RAISE EXCEPTION 'the store baseline requires an empty application database: recreate it';
 END IF;
 IF EXISTS (SELECT FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname='public' AND NOT EXISTS (SELECT FROM pg_depend d
 WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')) THEN
  RAISE EXCEPTION 'the store baseline requires absent application functions: recreate the database';
 END IF;
END $guard$;
'''
        for role in ROLES:
            prefix += f'CREATE ROLE {role} NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;\n'
            prefix += f'GRANT {role} TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;\n'
        suffix = '\n-- Restore the canonical security-definer owners without schema creation authority at runtime.\n'
        for role in sorted({o['owner'] for o in owners}):
            assert role in ROLES, role
            suffix += f'GRANT CREATE ON SCHEMA public TO {role};\n'
            for o in owners:
                if o['owner'] == role:
                    suffix += f"ALTER FUNCTION public.{o['signature']} OWNER TO {role};\n"
            suffix += f'REVOKE CREATE ON SCHEMA public FROM {role};\n'
        suffix += '''REVOKE CREATE ON SCHEMA public FROM PUBLIC;
DO $database_acl$ BEGIN
 EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
END $database_acl$;
'''
        partitions = """
-- Audit partitions cover an install-time window, provisioned by the canonical helper.
DO $partitions$ DECLARE m int; BEGIN
 FOR m IN -2..3 LOOP
  PERFORM public.audit_events_ensure_partition((date_trunc('month', now()) + (m || ' month')::interval)::date);
 END LOOP;
END $partitions$;
"""
        marker = r'ALTER TABLE public.audit_events\n    ADD CONSTRAINT audit_events_impersonation_identity_complete[^;]+;'
        constraint = re.search(marker, dump).group(0)
        dump = re.sub(marker, '', dump)
        suffix += partitions + constraint + '\n'
        suffix += '''
-- Subscription sync keeps its schema-owner authority; its guarded lookup needs
-- SELECT visibility on the FORCE-RLS endpoint table without BYPASSRLS.
DO $owner_policy$ DECLARE owner_role name; BEGIN
 SELECT pg_get_userbyid(proowner) INTO STRICT owner_role FROM pg_proc
 WHERE oid='public.sync_webhook_event_subscriptions(uuid,uuid,text[])'::regprocedure;
 EXECUTE format('CREATE POLICY webhook_subscriptions_migration_owner_read ON public.webhook_subscriptions FOR SELECT TO %I USING (current_user = %L)', owner_role, owner_role);
END $owner_policy$;
'''
        suffix += 'GRANT SELECT, UPDATE, USAGE ON SEQUENCE public.job_state_transitions_sequence_seq TO CURRENT_USER;\n'
        suffix += 'SET row_security = on;\nSET check_function_bodies = on;\n'
        text = prefix + dump + suffix
        for old in MIGRATIONS.glob('*.sql'):
            old.unlink()
        MIGRATIONS.mkdir(exist_ok=True)
        (MIGRATIONS / f'{BASELINE}.up.sql').write_text(text)
        (MIGRATIONS / f'{BASELINE}.down.sql').write_text(
            "-- The ledger is one baseline and carries no downgrade: recreate the database instead.\n"
            "DO $$ BEGIN RAISE EXCEPTION 'the store baseline is forward-only; recreate the database'; END $$;\n")
        PROVENANCE.write_text(json.dumps({
            'baseline': f'module/services/store/migrations/{BASELINE}.up.sql',
            'baseline_sha256': hashlib.sha256(text.encode()).hexdigest(),
            'replaced_source': source,
            'replaced_frontier': frontier,
            'replaced_migration_count': len(ups),
            'replaced_sha256': hashes,
            'postgres_image': IMAGE,
            'postgres_version': sql(name, 'SHOW server_version').strip(),
        }, indent=2, sort_keys=True) + '\n')
        print(json.dumps({'baseline_bytes': len(text), 'function_owners': len(owners), 'folded_migrations': len(ups), 'frontier': frontier}))
    finally:
        subprocess.run(['docker', 'rm', '-f', '-v', name], capture_output=True)


if __name__ == '__main__':
    main()
