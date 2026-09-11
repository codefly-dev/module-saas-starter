#!/usr/bin/env python3
"""Disposable real PostgreSQL/Vault acceptance; no managed-resource operations."""
import argparse, base64, hashlib, json, os, pathlib, subprocess, tempfile, time, urllib.request, uuid
ROOT=pathlib.Path(__file__).resolve().parents[2]
VAULT='sha256:0b60cd7b620685d1b772f43a37b2cdcd2afe1376c3119972c43c32092e5b118d'
REDIS='sha256:9d317178eceac8454a2284a9e6df2466b93c745529947f0cd42a0fa9609d7005'
PG='sha256:fe03a7605299a34ddf5e4f285dff78c3d7190a576b3c6b46f2fcff69f4bffd54'

def cmd(*args, **kw):
    return subprocess.check_output(args,text=True,stderr=subprocess.PIPE,**kw).strip()
def vault(url,path,data=None,token=None,method=None):
    req=urllib.request.Request(url+'/v1/'+path,data=json.dumps(data).encode() if data is not None else None,method=method)
    if token:req.add_header('X-Vault-Token',token)
    with urllib.request.urlopen(req,timeout=4) as r:return json.load(r) if r.status!=204 else {}
def ready(fn):
    for _ in range(100):
        try:return fn()
        except Exception:time.sleep(.1)
    raise RuntimeError('local dependency not ready')

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-dirty',action='store_true')
    parser.add_argument('--tenant-mount',action='store_true',help='focused normal-host tenant TLS proof with real Redis; skips broker suite')
    options=parser.parse_args()
    source=cmd('git','rev-parse','HEAD',cwd=ROOT)
    dirty=bool(cmd('git','status','--porcelain',cwd=ROOT))
    if dirty and not options.allow_dirty:raise RuntimeError('clean source required; use --allow-dirty only during development')
    for image in ((PG,VAULT,REDIS) if options.tenant_mount else (PG,VAULT)):
        if cmd('docker','image','inspect',image,'--format','{{.Id}}')!=image:raise RuntimeError('exact cached local image required')
    names=[]
    with tempfile.TemporaryDirectory(prefix='execution-custody-') as temp:
        d=pathlib.Path(temp)
        os.chmod(d,0o755)
        (d/'vault-data').mkdir(mode=0o777);os.chmod(d/'vault-data',0o777)
        (d/'vault.hcl').write_text('storage "file" { path = "/vault/file" }\nlistener "tcp" {\n address = "0.0.0.0:8200"\n tls_disable = true\n}\ndisable_mlock = true\napi_addr = "http://127.0.0.1:8200"\n')
        try:
            for suffix,image,extra in [('pg',PG,['-e','POSTGRES_HOST_AUTH_METHOD=trust']),('vault',VAULT,['-v',str(d/'vault-data')+':/vault/file','-v',str(d/'vault.hcl')+':/vault/config/local.hcl:ro'])]:
                name='custody-'+uuid.uuid4().hex[:10]+'-'+suffix;names.append(name)
                port='5432' if suffix=='pg' else '8200'
                args=['docker','run','-d','--name',name,'-p','127.0.0.1::'+port,*extra,image]
                if suffix=='vault':args+=['server']
                cmd(*args)
            pgport=cmd('docker','port',names[0],'5432').rsplit(':',1)[1]
            vport=cmd('docker','port',names[1],'8200').rsplit(':',1)[1]
            dsn=f'postgresql://postgres@127.0.0.1:{pgport}/postgres?sslmode=disable'
            vurl='http://127.0.0.1:'+vport
            def sql(text):
                p=subprocess.run(['psql',dsn,'-v','ON_ERROR_STOP=1','-q'],input=text,text=True,capture_output=True)
                if p.returncode:raise RuntimeError(p.stderr[-2000:])
            ready(lambda:sql('SELECT 1'))
            initialized=ready(lambda:vault(vurl,'sys/init',{'secret_shares':1,'secret_threshold':1},method='PUT'))
            root=initialized['root_token'];unseal=initialized['keys'][0]
            vault(vurl,'sys/unseal',{'key':unseal},method='PUT')
            vault(vurl,'sys/mounts/transit',{'type':'transit'},root)
            vault(vurl,'transit/keys/api-keys',{},root)
            vault(vurl,'sys/policies/acl/custody',{'policy':'path "transit/encrypt/api-keys" { capabilities = ["update"] }\npath "transit/decrypt/api-keys" { capabilities = ["update"] }'},root,method='PUT')
            credential=vault(vurl,'auth/token/create',{'policies':['custody'],'no_default_policy':True,'ttl':'1h'},root)['auth']['client_token']
            probe=b'non-secret-custody-restart-probe'
            encrypted=vault(vurl,'transit/encrypt/api-keys',{'plaintext':base64.b64encode(probe).decode()},credential)['data']['ciphertext']
            # A real Vault process restart, using persisted transit keys and ACLs.
            cmd('docker','restart',names[1])
            vport=cmd('docker','port',names[1],'8200').rsplit(':',1)[1]
            vurl='http://127.0.0.1:'+vport
            ready(lambda:vault(vurl,'sys/unseal',{'key':unseal},method='PUT'))
            decrypted=vault(vurl,'transit/decrypt/api-keys',{'ciphertext':encrypted},credential)['data']['plaintext']
            assert base64.b64decode(decrypted)==probe, 'transit restart changed original ciphertext'
            migrations=sorted((ROOT/'module/services/store/migrations').glob('*.up.sql'),key=lambda p:int(p.name.split('_')[0]))
            for migration in migrations:
                try:sql(migration.read_text())
                except Exception as e:raise RuntimeError(migration.name+': '+str(e)) from e
            sql('''CREATE ROLE custody_reader LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
CREATE ROLE custody_writer LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
GRANT USAGE ON SCHEMA public TO custody_reader,custody_writer;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO custody_reader;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO custody_writer;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO custody_writer;
REVOKE ALL ON execution_custody FROM custody_reader,custody_writer;
GRANT app_tenant,app_control_plane TO custody_writer;
''')
            binary=d/'accounts-custody'
            redis_url=''
            if options.tenant_mount:
                name='custody-'+uuid.uuid4().hex[:10]+'-redis';names.append(name)
                cmd('docker','run','-d','--name',name,'-p','127.0.0.1::6379',REDIS)
                redis_url='redis://127.0.0.1:'+cmd('docker','port',name,'6379').rsplit(':',1)[1]
                ready(lambda:cmd('docker','exec',name,'redis-cli','ping'))
            else:
                subprocess.run(['go','build','-trimpath','-o',str(binary),'./cmd/custody-qualification'],cwd=ROOT/'module/services/accounts/code',check=True)
            env={**os.environ,'CUSTODY_TEST_REDIS':redis_url,'CUSTODY_TEST_BINARY':str(binary),'CUSTODY_TEST_ADMIN':dsn,'CUSTODY_TEST_READER':dsn.replace('postgres@','custody_reader@'),'CUSTODY_TEST_WRITER':dsn.replace('postgres@','custody_writer@'),'CUSTODY_TEST_VAULT':vurl,'CUSTODY_TEST_VAULT_TOKEN':credential,'CUSTODY_TEST_VAULT_ROOT':root,'GOCACHE':os.environ.get('GOCACHE','/private/tmp/robin-accounts-go-cache')}
            print('Real local dependencies initialized; running '+('normal tenant mount' if options.tenant_mount else 'authenticated broker')+' acceptance.',flush=True)
            if options.tenant_mount:
                subprocess.run(['go','test','-c','-race','-trimpath','-o',str(binary),'.'],cwd=ROOT/'module/services/accounts/code',env=env,check=True)
                subprocess.run([str(binary),'-test.run=^TestExecutionTenantReal$','-test.v','-test.timeout=2m'],cwd=ROOT/'module/services/accounts/code',env=env,check=True)
            else:
                subprocess.run(['go','test','-race','-count=1','./pkg/adapters','-run','TestExecutionCustodyReal','-v'],cwd=ROOT/'module/services/accounts/code',env=env,check=True)
            sql((ROOT/'module/services/store/migrations/131_execution_custody.down.sql').read_text())
            sql((ROOT/'module/services/store/migrations/131_execution_custody.up.sql').read_text())
            print(json.dumps({'source':source,'dirty':dirty,'migration_sha256':{p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in migrations},'local_only':True,'binary_kind':'normal-host-test' if options.tenant_mount else 'local-custody-process','qualification':'normal-tenant-mount' if options.tenant_mount else 'custody-broker','redis_image':REDIS if options.tenant_mount else None,'subprocess_binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest() if binary.exists() else None,'postgres_image':cmd('docker','image','inspect',PG,'--format','{{.Id}}'),'vault_image':VAULT,'vault_restart':True,'migrations':len(migrations),'source_sha256':{str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in [ROOT/'module/services/accounts/code/execution_custody.go',ROOT/'module/services/accounts/code/execution_tenant_test.go',ROOT/'module/services/accounts/code/pkg/adapters/execution_custody_grpc.go',ROOT/'module/deployment/topology.bindings.codefly.yaml',ROOT/'qualification/execution-custody/run.py'] if p.exists()},'paid_calls':0}))
        finally:
            for name in reversed(names):subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
if __name__=='__main__':main()
