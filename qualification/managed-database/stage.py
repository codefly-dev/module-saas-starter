#!/usr/bin/env python3
"""Stage the store ledger as explicit inputs for the existing schema-plan packager.

No database, credentials, image publication or operator calls. Identity and
access bindings come unchanged from the caller's reviewed v1 specification. The
ledger is one generated baseline plus any forward migration above it, and the
baseline is admitted only when it hashes to what its provenance recorded.
"""
import argparse,hashlib,json,re
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
MIGRATIONS=ROOT/'module/services/store/migrations'
PROVENANCE=ROOT/'module/services/store/baseline.provenance.json'


def ledger_files():
    provenance=json.loads(PROVENANCE.read_text())
    baseline=ROOT/provenance['baseline']
    if hashlib.sha256(baseline.read_bytes()).hexdigest()!=provenance['baseline_sha256']:
        raise ValueError('baseline differs from its recorded provenance digest')
    files={}
    for p in MIGRATIONS.glob('*.sql'):
        if not re.fullmatch(r'(\d+)_.+\.(up|down)\.sql',p.name):raise ValueError('invalid migration filename')
        files[p.name]=p.read_bytes()
    if baseline.name not in files:raise ValueError('the ledger must contain the recorded baseline')
    return files


def stage(template, output):
    spec=json.loads(template.read_text())
    plan=spec['plan']
    if plan['contract-version']!='codefly.dev/postgres-schema-plan/v1' or len(plan['lineages'])!=1:
        raise ValueError('exactly one v1 store lineage required')
    lineage=plan['lineages'][0]
    if (lineage['label'],lineage['ledger'],lineage['stage'])!=('store','schema_migrations','store'):
        raise ValueError('canonical store lineage, ledger and stage required')
    if output.exists():raise ValueError('output must be a new directory')
    files=ledger_files()
    lineage['files']=[{'name':name,'digest':'sha256:'+hashlib.sha256(data).hexdigest()} for name,data in sorted(files.items())]
    lineage['digest']='';plan['digest']=''
    spec['binding']['plan-sha256']='';spec['binding']['plan-digest']=''
    output.mkdir(parents=True)
    (output/'sql/store').mkdir(parents=True)
    for name,data in files.items():(output/'sql/store'/name).write_bytes(data)
    (output/'spec.json').write_text(json.dumps(spec,indent=2)+'\n')
    (output/'selection.json').write_text(json.dumps({'profile':'baseline','template_sha256':hashlib.sha256(template.read_bytes()).hexdigest(),'files':lineage['files']},indent=2)+'\n')
    return len(files)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--template-spec',required=True,type=Path)
    p.add_argument('--output',required=True,type=Path)
    a=p.parse_args();n=stage(a.template_spec,a.output)
    print(json.dumps({'profile':'baseline','files':n,'output':str(a.output),'database_work_performed':False}))

if __name__=='__main__':main()
