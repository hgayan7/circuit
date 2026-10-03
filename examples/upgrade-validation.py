#!/usr/bin/env python3
"""Rehearse a single-instance image upgrade and rollback on current fixture state."""
import argparse
import json
import os
from pathlib import Path
import ssl
import subprocess
import time
import urllib.request

parser=argparse.ArgumentParser()
parser.add_argument('--deployment-dir',required=True,type=Path)
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
work=args.deployment_dir.resolve()
runtime=json.loads((work/'runtime.json').read_text())
operations=json.loads((work/'operations-report.json').read_text())
if not runtime['project'].startswith('circuit-staging-'):
    raise SystemExit('Requires local fixture deployment')
env={**os.environ,'CIRCUIT_DEPLOY_DIR':str(work),'CIRCUIT_BIND_PORT':str(runtime['port']),'CIRCUIT_STATE_FILE':runtime['state_file'],
     **{'CIRCUIT_'+key.upper()+'_PORT':str(value) for key,value in operations['ports'].items()}}
compose=['docker','compose','-p',runtime['project'],'-f',str(root/'deploy/docker/compose.yaml'),'-f',str(root/'deploy/docker/operations.compose.yaml'),'-f',str(root/'deploy/docker/mail-test.compose.yaml')]
def run(command,**kwargs):
    return subprocess.run(command,check=True,env=env,**kwargs)
def dc(*command,**kwargs):
    return run(compose+list(command),**kwargs)
context=ssl.create_default_context(cafile=str(work/'secrets/tls.crt'))
token=(work/'secrets/admin-token').read_text().strip()
agent=(work/'secrets/agent-token').read_text().strip()
base='https://127.0.0.1:'+str(runtime['port'])
def api(path,credential='',body=None,key=''):
    headers={'Content-Type':'application/json'}
    if credential:
        headers['Authorization']='Bearer '+credential
    if key:
        headers['Idempotency-Key']=key
    req=urllib.request.Request(base+path,headers=headers,data=None if body is None else json.dumps(body).encode())
    with urllib.request.urlopen(req,context=context,timeout=35) as response:
        return json.load(response)
def ready():
    for _ in range(120):
        try:
            if api('/readyz')['status']=='ready':
                return
        except OSError:
            pass
        time.sleep(.5)
    raise AssertionError('Gateway failed to become ready')
dc('stop','backup','soak')
old=run(['docker','inspect',runtime['project']+'-gateway-1','--format','{{.Image}}'],capture_output=True,text=True).stdout.strip()
before=api('/admin/actions',token)
original=api('/v1/actions',agent,{'operation':'get_pr','repository':'hgayan7/circuit-gateway-pilot-20261003','args':{'number':3}},'initial-read')
if original['state']!='succeeded':
    raise AssertionError('Original fixture read must be successful')
run(['docker','build','--target','gateway','-f',str(root/'deploy/docker/Dockerfile'),'-t','circuit-gateway:local',str(root)])
new=run(['docker','image','inspect','circuit-gateway:local','--format','{{.Id}}'],capture_output=True,text=True).stdout.strip()
if old==new:
    raise SystemExit('Upgrade rehearsal requires a different previous image')
checks=[]
try:
    for name,image in [('upgrade',new),('rollback',old),('return to upgraded image',new)]:
        run(['docker','tag',image,'circuit-gateway:local'])
        dc('up','-d','--no-build','--force-recreate','gateway')
        ready()
        after=api('/admin/actions',token)
        if {item['id']:item for item in before}!={item['id']:item for item in after}:
            raise AssertionError(name+' changed durable history')
        again=api('/v1/actions',agent,{'operation':'get_pr','repository':'hgayan7/circuit-gateway-pilot-20261003','args':{'number':3}},'initial-read')
        if again['id']!=original['id']:
            raise AssertionError(name+' lost idempotency')
        checks.append(name+' preserves readiness, history, and idempotency on current state')
        print('PASS',checks[-1],flush=True)
    result={'passed':True,'checks':checks,'previous_image':old,'updated_image':new,'scope':'single-instance GitHub fixture; current state only; no old backup substituted'}
    (work/'upgrade-report.json').write_text(json.dumps(result,indent=2))
finally:
    run(['docker','tag',new,'circuit-gateway:local'])
    dc('up','-d','--no-build','gateway')
    ready()
    dc('up','-d','backup')
    dc('--profile','soak','up','-d','--force-recreate','soak')
    print('STARTED fresh 72-hour read-only fixture soak after image rehearsal',flush=True)
