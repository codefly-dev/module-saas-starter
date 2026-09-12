#!/usr/bin/env python3
"""Stage explicit fresh/upgrade inputs for the existing schema-plan packager.

No database, credentials, image publication or operator calls. Identity and
access bindings come unchanged from the caller's reviewed v1 specification.
"""
import argparse,hashlib,json,re
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
BASELINE=ROOT/'module/services/store/baselines/managed-v1'
MIGRATIONS=ROOT/'module/services/store/migrations'


def stage(profile, template, output):
    spec=json.loads(template.read_text())
    plan=spec['plan']
    if plan['contract-version']!='codefly.dev/postgres-schema-plan/v1' or len(plan['lineages'])!=1:
        raise ValueError('exactly one v1 store lineage required')
    lineage=plan['lineages'][0]
    if (lineage['label'],lineage['ledger'],lineage['stage'])!=('store','schema_migrations','store'):
        raise ValueError('canonical store lineage, ledger and stage required')
    if output.exists():raise ValueError('output must be a new directory')
    files={}
    for p in MIGRATIONS.glob('*.sql'):
        match=re.fullmatch(r'(\d+)_.+\.(up|down)\.sql',p.name)
        if not match:raise ValueError('invalid migration filename')
        if profile=='upgrade' or int(match[1])>135:files[p.name]=p.read_bytes()
    if profile=='fresh':
        metadata=json.loads((BASELINE/'provenance.json').read_text())
        baseline=BASELINE/'135_managed_baseline.up.sql'
        if hashlib.sha256(baseline.read_bytes()).hexdigest()!=metadata['baseline_sha256']:
            raise ValueError('baseline differs from recorded source digest')
        for p in BASELINE.glob('*.sql'):files[p.name]=p.read_bytes()
    elif profile!='upgrade':raise ValueError('explicit fresh or upgrade profile required')
    if not any(name.startswith('136_') for name in files):raise ValueError('explicit policies upgrade required')
    lineage['files']=[{'name':name,'digest':'sha256:'+hashlib.sha256(data).hexdigest()} for name,data in sorted(files.items())]
    lineage['digest']='';plan['digest']=''
    spec['binding']['plan-sha256']='';spec['binding']['plan-digest']=''
    output.mkdir(parents=True)
    (output/'sql/store').mkdir(parents=True)
    for name,data in files.items():(output/'sql/store'/name).write_bytes(data)
    (output/'spec.json').write_text(json.dumps(spec,indent=2)+'\n')
    (output/'selection.json').write_text(json.dumps({'profile':profile,'template_sha256':hashlib.sha256(template.read_bytes()).hexdigest(),'files':lineage['files'],'database_work_performed':False},indent=2)+'\n')
    return len(files)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--profile',required=True,choices=['fresh','upgrade'])
    p.add_argument('--template-spec',required=True,type=Path)
    p.add_argument('--output',required=True,type=Path)
    a=p.parse_args();n=stage(a.profile,a.template_spec,a.output)
    print(json.dumps({'profile':a.profile,'files':n,'output':str(a.output),'database_work_performed':False}))

if __name__=='__main__':main()
