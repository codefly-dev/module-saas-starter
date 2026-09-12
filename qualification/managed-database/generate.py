#!/usr/bin/env python3
"""Generate a reviewed fresh baseline from a pinned, empty canonical PG16 schema.

Only disposable network-isolated Docker containers are touched. Never connects
through an ambient DSN. Historical migrations are read from the pinned Git tree.
"""
import hashlib,json,re,subprocess,time,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
OUT=ROOT/'module/services/store/baselines/managed-v1'
SOURCE='ebcd41a643ab73f97c4a17a070f815610bf9d7aa'
IMAGE='postgres@sha256:fe03a7605299a34ddf5e4f285dff78c3d7190a576b3c6b46f2fcff69f4bffd54'
ROLES=['app_tenant','app_control_plane','app_billing_worker','app_webhook_worker','app_job_worker']

def run(args,**kwargs):
    p=subprocess.run(args,text=True,capture_output=True,timeout=90,**kwargs)
    if p.returncode: raise RuntimeError(p.stderr[-3000:])
    return p.stdout

def sql(container,body):
    return run(['docker','exec','-i',container,'psql','-XqAt','-v','ON_ERROR_STOP=1','-U','postgres'],input=body)

def main():
    name='managed-baseline-'+uuid.uuid4().hex[:10]
    run(['docker','image','inspect',IMAGE])
    try:
        run(['docker','run','-d','--name',name,'--network','none','-e','POSTGRES_HOST_AUTH_METHOD=trust',IMAGE])
        for _ in range(160):
            try:
                if 'PostgreSQL init process complete' in run(['docker','logs',name]):
                    sql(name,'SELECT 1');break
            except RuntimeError: pass
            time.sleep(.25)
        else: raise RuntimeError('disposable database failed to start')
        paths=run(['git','ls-tree','-r','--name-only',SOURCE,'module/services/store/migrations'],cwd=ROOT).splitlines()
        paths=sorted((p for p in paths if p.endswith('.up.sql')),key=lambda p:int(Path(p).name.split('_')[0]))
        hashes={}
        for path in paths:
            data=run(['git','show',SOURCE+':'+path],cwd=ROOT)
            hashes[path]=hashlib.sha256(data.encode()).hexdigest()
            sql(name,data)
        # Fixed policy inventory derives from this reviewed snapshot only. It is
        # emitted as literal SQL, never inferred from live ACLs during install.
        inventory=json.loads(sql(name,"""SELECT json_agg(x ORDER BY role_name,table_name) FROM (
SELECT r.rolname AS role_name,c.relname AS table_name FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN pg_roles r
WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relrowsecurity
AND r.rolname IN ('app_control_plane','app_billing_worker','app_webhook_worker','app_job_worker')
AND (has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE')
OR has_any_column_privilege(r.oid,c.oid,'INSERT,UPDATE,SELECT')) ) x;"""))
        assert inventory
        policies='-- Explicit background row visibility. Existing SQL ACLs remain unchanged.\n-- Exact current_user prevents inherited role membership from widening a tenant role.\n'
        down='''-- A managed install needs these policies; never silently disable its workers.
DO $guard$ BEGIN
 IF EXISTS (SELECT FROM pg_roles WHERE rolname IN
 ('app_control_plane','app_billing_worker','app_webhook_worker','app_job_worker')
 AND NOT rolbypassrls) THEN
  RAISE EXCEPTION 'background policy rollback requires all legacy bypass roles; use a forward fix';
 END IF;
END $guard$;
'''
        for entry in inventory:
            role,table=entry['role_name'],entry['table_name']
            assert re.fullmatch('[a-z_]+',role) and re.fullmatch('[a-z_0-9]+',table)
            policy=role+'_explicit_rows'
            policies+=f'CREATE POLICY {policy} ON public.{table} FOR ALL TO {role}\n USING (current_user = \'{role}\') WITH CHECK (current_user = \'{role}\');\n'
            down+=f'DROP POLICY {policy} ON public.{table};\n'
        policies+="""
-- Historical default grants could include the migration engine's ledger.
-- Runtime roles must never edit the record of installed schema versions.
DO $ledger$ BEGIN
 IF to_regclass('public.schema_migrations') IS NOT NULL THEN
  REVOKE ALL ON public.schema_migrations FROM PUBLIC, app_tenant,
   app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;
 END IF;
END $ledger$;
"""
        # These guarded operations must execute as their existing DML role,
        # not assume the schema owner's formerly implicit superuser authority.
        operation_owners={'enqueue_job_message':'app_job_worker','replay_job_message':'app_job_worker',
                          'publish_domain_event':'app_control_plane'}
        operation_rows=json.loads(sql(name,"SELECT json_agg(x ORDER BY name) FROM (SELECT proname name,oid::regprocedure::text signature FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname IN ('enqueue_job_message','replay_job_message','publish_domain_event','sync_webhook_event_subscriptions')) x"))
        policies+='\n-- Preserve guarded function-only access without a privileged schema-owner session.\n'
        for role in ['app_control_plane','app_job_worker']:
            policies+=f'GRANT CREATE ON SCHEMA public TO {role};\n'
            for op in operation_rows:
                if operation_owners.get(op['name'])==role:
                    policies+=f"ALTER FUNCTION public.{op['signature']} OWNER TO {role};\n"
                    down+=f"ALTER FUNCTION public.{op['signature']} OWNER TO CURRENT_USER;\n"
            policies+=f'REVOKE CREATE ON SCHEMA public FROM {role};\n'
        policies+='''
-- Subscription sync retains its schema-owner authority to DELETE subscription
-- bindings. No runtime role receives that table privilege. Its guarded lookup
-- needs SELECT visibility on the FORCE-RLS endpoint table without BYPASSRLS.
DO $owner_policy$ DECLARE owner_role name; BEGIN
 SELECT pg_get_userbyid(proowner) INTO STRICT owner_role FROM pg_proc
 WHERE oid='public.sync_webhook_event_subscriptions(uuid,uuid,text[])'::regprocedure;
 EXECUTE format('CREATE POLICY webhook_subscriptions_migration_owner_read ON public.webhook_subscriptions FOR SELECT TO %I USING (current_user = %L)', owner_role, owner_role);
END $owner_policy$;
'''
        down+='DROP POLICY webhook_subscriptions_migration_owner_read ON public.webhook_subscriptions;\n'
        enqueue=run(['git','show',SOURCE+':module/services/store/migrations/75_email_job_convergence.up.sql'],cwd=ROOT)
        enqueue=re.search(r'CREATE OR REPLACE FUNCTION public.enqueue_job_message\([\s\S]*?\$function\$;',enqueue).group(0)
        assert 'IF NOT (' in enqueue and ') THEN\n            RAISE EXCEPTION \'job scope' in enqueue
        enqueue=enqueue.replace('IF NOT (','IF (',1).replace(") THEN\n            RAISE EXCEPTION 'job scope", ") IS NOT TRUE THEN\n            RAISE EXCEPTION 'job scope",1)
        marker='\n-- Preserve guarded function-only access without a privileged schema-owner session.\n'
        policies=policies.replace(marker,'\n-- Missing or empty request scope is denial, including SQL NULL predicates.\n'+enqueue+'\n'+marker)
        # Replace before transferring ownership; preserve the existing EXECUTE ACL.
        # Downgrade never restores the old nullable authorization guard.
        diagnostic=run(['git','show',SOURCE+':module/services/store/migrations/135_organization_administrator_diagnostic.up.sql'],cwd=ROOT)
        diagnostic=re.search(r'CREATE (?:OR REPLACE )?FUNCTION public.record_membership_integrity_findings\(\)[\s\S]*?\$function\$;',diagnostic).group(0)
        diagnostic=diagnostic.replace('CREATE FUNCTION', 'CREATE OR REPLACE FUNCTION', 1)
        old_guard="""IF NOT EXISTS (
        SELECT 1 FROM pg_roles
        WHERE rolname = current_user AND (rolbypassrls OR rolsuper)
    ) THEN"""
        assert old_guard in diagnostic
        updated=diagnostic.replace(old_guard,"IF current_user <> 'app_control_plane' THEN")
        policies+='\n-- The named control plane now spans rows through explicit policies.\n'+updated+'\n'
        down+=diagnostic+'\n'
        (ROOT/'module/services/store/migrations/136_explicit_background_rls.up.sql').write_text(policies)
        (ROOT/'module/services/store/migrations/136_explicit_background_rls.down.sql').write_text(down)
        owners=json.loads(sql(name,"""SELECT coalesce(json_agg(x ORDER BY signature),'[]') FROM (
SELECT p.oid::regprocedure::text AS signature, r.rolname AS owner
FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles r ON r.oid=p.proowner
WHERE n.nspname='public' AND r.rolname<>'postgres') x;"""))
        # Fresh catalog identifiers are deterministic per natural key. Replacing
        # the generated UUIDs in the export also updates their FK references.
        id_map={}
        time_values=set()
        for table,key in [('roles','name'),('plans','name'),('email_templates','name'),('data_retention_policies','resource_type')]:
            rows=json.loads(sql(name,f"SELECT json_agg(t) FROM public.{table} t"))
            for row in rows:
                id_map[row['id']]=str(uuid.uuid5(uuid.NAMESPACE_URL,'codefly.dev/accounts/managed-v1/'+table+'/'+row[key]))
            for col in sql(name,f"SELECT attname FROM pg_attribute WHERE attrelid='public.{table}'::regclass AND atttypid IN ('timestamptz'::regtype,'timestamp'::regtype) AND attnum>0 AND NOT attisdropped").splitlines():
                time_values.update(sql(name,f'SELECT DISTINCT {col}::text FROM public.{table} WHERE {col} IS NOT NULL').splitlines())
        # Other seeded timestamp columns retain install-time behavior too.
        time_values.update(sql(name,"SELECT DISTINCT updated_at::text FROM audit_event_types WHERE updated_at IS NOT NULL").splitlines())
        time_values.update(sql(name,"SELECT DISTINCT created_at::text FROM identity_providers WHERE created_at IS NOT NULL").splitlines())
        # Monthly partitions are provisioned relative to installation time by
        # the canonical helper, not frozen to the build machine's calendar.
        for table in sql(name,"SELECT c.relname FROM pg_class c JOIN pg_inherits i ON i.inhrelid=c.oid WHERE i.inhparent='public.audit_events'::regclass").splitlines():
            assert re.fullmatch('audit_events_[0-9]{4}_[0-9]{2}',table)
            sql(name,'DROP TABLE public.'+table)
        dump=run(['docker','exec',name,'pg_dump' ,'-U','postgres','--no-owner','--inserts','--rows-per-insert=100','--dbname=postgres'])
        for old,new in id_map.items(): dump=dump.replace(old,new)
        for value in sorted(time_values,reverse=True): dump=dump.replace("'"+value+"'",'CURRENT_TIMESTAMP')
        dump=re.sub(r'^SET (?:statement_timeout|lock_timeout|idle_in_transaction_session_timeout) = .*;\n','',dump,flags=re.M)
        dump=re.sub(r'^COMMENT ON EXTENSION .*;\n','',dump,flags=re.M)
        # Remove only pg_dump's ephemeral psql guards. The runner executes SQL,
        # and the committed digest supplies integrity. No deployed SQL is edited.
        dump=re.sub(r'^\\(?:un)?restrict .*\n','',dump,flags=re.M)
        dump=dump.replace('ALTER DEFAULT PRIVILEGES FOR ROLE postgres ', 'ALTER DEFAULT PRIVILEGES FOR ROLE CURRENT_USER ')
        # Database-specific grants are emitted independently below; pg_dump of
        # a database without --create omits database ACLs.
        prefix='''-- Versioned empty-database baseline; generated by qualification/managed-database/generate.py.
-- Never use on an existing installation. Historical migrations remain unchanged.
DO $guard$ BEGIN
 IF current_setting('server_version_num')::int < 160000 THEN
  RAISE EXCEPTION 'managed baseline requires PostgreSQL 16 or later';
 END IF;
 IF EXISTS (SELECT FROM pg_roles WHERE rolname IN ('app_tenant','app_control_plane',
 'app_billing_worker','app_webhook_worker','app_job_worker')) THEN
  RAISE EXCEPTION 'managed baseline requires absent application roles';
 END IF;
 IF EXISTS (SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f','S') AND c.relname<>'schema_migrations'
 AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid='pg_class'::regclass
 AND d.objid=c.oid AND d.deptype='e')) THEN
  RAISE EXCEPTION 'managed baseline requires an empty application database';
 END IF;
 IF EXISTS (SELECT FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname='public' AND NOT EXISTS (SELECT FROM pg_depend d
 WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')) THEN
  RAISE EXCEPTION 'managed baseline requires absent application functions';
 END IF;
END $guard$;
'''
        for role in ROLES:
            prefix+=f'CREATE ROLE {role} NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;\n'
            prefix+=f'GRANT {role} TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;\n'
        suffix='\n-- Restore the canonical security-definer owners without schema creation authority at runtime.\n'
        for role in sorted({o['owner'] for o in owners}):
            assert role in ROLES
            suffix+=f'GRANT CREATE ON SCHEMA public TO {role};\n'
            for o in owners:
                if o['owner']==role: suffix+=f"ALTER FUNCTION public.{o['signature']} OWNER TO {role};\n"
            suffix+=f'REVOKE CREATE ON SCHEMA public FROM {role};\n'
        suffix+='''REVOKE CREATE ON SCHEMA public FROM PUBLIC;
DO $database_acl$ BEGIN
 EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
END $database_acl$;
'''
        partitions="""
-- Preserve migration 97's install-time partition window.
DO $partitions$ DECLARE m int; BEGIN
 FOR m IN -2..3 LOOP
  PERFORM public.audit_events_ensure_partition((date_trunc('month', now()) + (m || ' month')::interval)::date);
 END LOOP;
END $partitions$;
"""
        marker=r'ALTER TABLE public.audit_events\n    ADD CONSTRAINT audit_events_impersonation_identity_complete[^;]+;'
        constraint=re.search(marker,dump).group(0)
        dump=re.sub(marker,'',dump)
        suffix+=partitions+constraint+'\n'
        suffix+='GRANT SELECT, UPDATE, USAGE ON SEQUENCE public.job_state_transitions_sequence_seq TO CURRENT_USER;\n'
        suffix+='SET row_security = on;\nSET check_function_bodies = on;\n'
        sqltext=prefix+dump+suffix
        (OUT/'135_managed_baseline.up.sql').write_text(sqltext)
        (OUT/'135_managed_baseline.down.sql').write_text("DO $$ BEGIN RAISE EXCEPTION 'managed baseline is forward-only; restore a reviewed backup or apply a forward fix'; END $$;\n")
        (OUT/'provenance.json').write_text(json.dumps({'canonical_source':SOURCE,'canonical_version':135,'migration_count':len(paths),'migration_sha256':hashes,'postgres_image':IMAGE,'postgres_version':sql(name,'SHOW server_version').strip(),'baseline_sha256':hashlib.sha256(sqltext.encode()).hexdigest(),'explicit_policy_inventory':inventory,'function_owners':owners},indent=2)+'\n')
        print(json.dumps({'baseline_bytes':len(sqltext),'policies':len(inventory),'function_owners':len(owners),'historical_migrations':len(paths)}))
    finally:
        subprocess.run(['docker','rm','-f','-v',name],capture_output=True)

if __name__=='__main__':main()
