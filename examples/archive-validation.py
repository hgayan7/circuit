#!/usr/bin/env python3
"""Disposable authenticated S3 fixture; never uses production keys or volumes."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser()
parser.add_argument('--image', default='circuit-archive:local')
parser.add_argument('--report', type=Path)
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
image = 'rclone/rclone:1.75.1@sha256:45401ad7410db1d67ffdb58e19059ad20b0d8e0285a60e38bbec55cc1019c7a5'
prefix = 'circuit-archive-fixture-'+str(time.time_ns())
checks = []
def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)
def check(name, value):
    if not value:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)
with tempfile.TemporaryDirectory(prefix='circuit-archive-fixture-') as tmp:
    work = Path(tmp)
    work.chmod(0o777)  # Disposable fixture-only files, accessible to container UID 10001.
    config = work/'storage.conf'
    config.write_text('[backup]\ntype=s3\nprovider=Other\nendpoint=http://s3:8080\naccess_key_id=fixture-access\nsecret_access_key=fixture-secret\nforce_path_style=true\n')
    config.chmod(0o444)
    env = {**os.environ, 'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': 'arm64' if os.uname().machine == 'arm64' else 'amd64'}
    run(['go', 'test', '-c', '-o', str(work/'archive.test'), './pkg/archive'], cwd=root, env=env)
    created = False
    try:
        run(['docker','network','create','--internal',prefix], stdout=subprocess.DEVNULL)
        created = True
        run(['docker','run','-d','--name',prefix,'--network',prefix,'--network-alias','s3',
             '--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
             '--tmpfs','/data:rw,noexec,nosuid,size=16m','--tmpfs','/tmp:rw,size=4m',
             image,'serve','s3','/data','--addr',':8080','--auth-key','fixture-access,fixture-secret'], stdout=subprocess.DEVNULL)
        time.sleep(2)
        base = ['docker','run','--rm','--network',prefix,'--read-only','--cap-drop','ALL',
                '--security-opt','no-new-privileges','--tmpfs','/tmp:rw,size=16m',
                '-v',str(work)+':/fixture']
        run(base+['-e','CIRCUIT_ARCHIVE_S3_FIXTURE=1','--entrypoint','/fixture/archive.test',args.image,
                  '-test.run','^TestRcloneS3Recovery$','-test.v'])
        check('Authenticated S3 upload, full readback, and recovery after disposable local state loss', True)
        check('Restored state retains reconciliation barrier; corrupt digest and oversized readback fail', True)
        worker = base+[args.image,'--once','--config','/fixture/storage.conf','--remote','backup:fixture/circuit',
                       '--source','/fixture/source','--state','/fixture/report.json']
        run(worker)
        def status_report():
            return json.loads(run(base+['--entrypoint','cat',args.image,'/fixture/report.json'],capture_output=True,text=True).stdout)
        status = status_report()
        check('Restricted archive worker publishes verified receipt',status['last_success_unix']>0 and len(status['receipts'])==1)
        bad = work/'bad.conf'
        bad.write_text(config.read_text().replace('fixture-secret','invalid-secret'))
        bad.chmod(0o444)
        negative = worker.copy()
        negative[negative.index('/fixture/storage.conf')] = '/fixture/bad.conf'
        result = subprocess.run(negative, capture_output=True, text=True)
        status = status_report()
        check('Invalid storage credentials fail closed and reset stale success evidence',result.returncode!=0 and status['last_success_unix']==0)
        check('Provider credential details are absent from errors and receipts','invalid-secret' not in result.stderr and 'fixture-secret' not in json.dumps(status))
        if args.report:
            args.report.write_text(json.dumps({'passed':True,'checks':checks,'backend':'authenticated local S3-compatible fixture',
                'host_loss_scope':'disposable local state removal; not an actual remote host loss',
                'production_storage_delivery':'operator BYOK configuration and separate-host recovery rehearsal required'},indent=2)+'\n')
    finally:
        if created:
            subprocess.run(['docker','run','--rm','--network','none','-v',str(work)+':/fixture',
                            '--entrypoint','sh',args.image,'-c',
                            'rm -rf /fixture/source /fixture/report.json /fixture/retrieved.age /fixture/restored.db /fixture/original.db'],
                           stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        subprocess.run(['docker','rm','-f',prefix],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if created:
            subprocess.run(['docker','network','rm',prefix],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
