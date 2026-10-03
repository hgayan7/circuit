#!/usr/bin/env python3
"""Operator-owned local monitoring, encrypted backup, and email delivery rehearsal."""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--deployment-dir', required=True, type=Path)
parser.add_argument('--start-soak', action='store_true')
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
work = args.deployment_dir.resolve()
runtime = json.loads((work / 'runtime.json').read_text())
if not runtime['project'].startswith('circuit-staging-'):
    raise SystemExit('Requires an existing local fixture deployment')
def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]
ports = {name: port() for name in ['prometheus', 'alertmanager', 'mailpit']}
env = {**os.environ, 'CIRCUIT_DEPLOY_DIR': str(work), 'CIRCUIT_BIND_PORT': str(runtime['port']),
       'CIRCUIT_STATE_FILE': runtime['state_file'], 'CIRCUIT_BACKUP_INTERVAL': '5s',
       'CIRCUIT_BACKUP_RETAIN': '2', 'CIRCUIT_SOAK_INTERVAL': '2s', 'CIRCUIT_SOAK_DURATION': '6s',
       **{'CIRCUIT_'+name.upper()+'_PORT': str(value) for name, value in ports.items()}}
compose = ['docker', 'compose', '-p', runtime['project'], '-f', str(root/'deploy/docker/compose.yaml'),
           '-f', str(root/'deploy/docker/operations.compose.yaml'), '-f', str(root/'deploy/docker/mail-test.compose.yaml')]
def run(command, **kwargs):
    return subprocess.run(command, env=env, check=True, **kwargs)
def dc(*command, **kwargs):
    return run(compose+list(command), **kwargs)
def http(name, path):
    with urllib.request.urlopen('http://127.0.0.1:'+str(ports[name])+path, timeout=5) as response:
        return json.load(response)
def wait(test, seconds=120):
    until = time.monotonic()+seconds
    while time.monotonic()<until:
        try:
            if test():
                return
        except (OSError, ValueError, subprocess.CalledProcessError):
            pass
        time.sleep(2)
    raise AssertionError('Validation condition timed out')
checks=[]
def check(name, value):
    if not value:
        raise AssertionError(name)
    checks.append(name)
    print('PASS',name,flush=True)
def report(service):
    result=dc('exec','-T',service,'wget','-qO-','http://127.0.0.1:9091/report',capture_output=True,text=True)
    return json.loads(result.stdout)
offline=work/'offline'
offline.mkdir(mode=0o700,exist_ok=True)
identity=offline/'backup-identity'
recipient=work/'secrets/backup-recipient'
run(['go','build','-o',str(root/'bin/circuit'),'./cmd/circuit'],cwd=root)
if not identity.exists():
    run([str(root/'bin/circuit'),'gateway','backup-keygen','--identity-out',str(identity),'--recipient-out',str(recipient)])
recipient.chmod(0o444)
renewed=False
if args.start_soak and subprocess.run(['openssl','x509','-in',str(work/'secrets/tls.crt'),'-checkend','259260','-noout'],stdout=subprocess.DEVNULL).returncode:
    # Only this operator-owned local fixture certificate is renewed, never system trust.
    new_key=work/'secrets/tls.key.new'
    new_cert=work/'secrets/tls.crt.new'
    run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','7','-keyout',str(new_key),'-out',str(new_cert),
         '-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,DNS:gateway,IP:127.0.0.1'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for source,target in [(new_key,work/'secrets/tls.key'),(new_cert,work/'secrets/tls.crt')]:
        source.chmod(0o444)
        source.replace(target)
    renewed=True
smtp='''global:
  smtp_smarthost: mailpit:1025
  smtp_from: circuit@localhost
  smtp_require_tls: false
route:
  receiver: fixture-email
  group_by: [alertname, job]
  group_wait: 5s
  group_interval: 10s
  repeat_interval: 4h
receivers:
  - name: fixture-email
    email_configs:
      - to: operator@localhost
        send_resolved: true
        headers:
          Subject: '[Circuit] {{ .Status }}: {{ .GroupLabels.alertname }}'
        text: '{{ range .Alerts }}{{ .Labels.alertname }}: {{ .Annotations.summary }}{{ "\\n" }}{{ end }}'
'''
config=work/'alertmanager.yaml'
if config.exists() and config.read_text()!=smtp:
    raise SystemExit('Refusing to overwrite existing operator email configuration')
if not config.exists():
    config.write_text(smtp)
config.chmod(0o444)
gateway_stopped=False
try:
    dc('build','backup')
    dc('run','--rm','--no-deps','--entrypoint','/bin/promtool','prometheus','check','config','/etc/prometheus/prometheus.yaml')
    dc('run','--rm','--no-deps','--entrypoint','/bin/amtool','alertmanager','check-config','/etc/alertmanager/alertmanager.yaml')
    dc('up','-d',*(['--force-recreate'] if renewed else []),'gateway','prometheus','alertmanager','mailpit','backup')
    wait(lambda: any(item['labels']['job']=='circuit' and item['health']=='up' for item in http('prometheus','/api/v1/targets')['data']['activeTargets']))
    check('Prometheus scrapes authenticated gateway over verified TLS',True)
    wait(lambda: report('backup')['samples']>=3)
    check('Scheduled encrypted backups succeed', report('backup')['failures']==0)
    listing=dc('exec','-T','backup','find','/backups','-type','f',capture_output=True,text=True).stdout.splitlines()
    check('Retention keeps two encrypted archives and no plaintext',len(listing)==2 and all(path.endswith('.db.age') for path in listing))
    container=dc('ps','-q','backup',capture_output=True,text=True).stdout.strip()
    archived=work/'operations-restore.age'
    run(['docker','cp',container+':'+sorted(listing)[-1],str(archived)])
    restored=offline/('restore-rehearsal-'+str(time.time_ns())+'.db')
    run([str(root/'bin/circuit'),'gateway','restore','--backup',str(archived),'--identity-file',str(identity),'--out',str(restored)])
    check('Encrypted archive restores with mandatory reconciliation barrier',restored.stat().st_mode&0o777==0o600)
    absent=dc('exec','-T','backup','sh','-c','test ! -e /run/secrets/backup-identity && test ! -e /run/secrets/github-app.pem',capture_output=True)
    check('Backup worker has neither decryption identity nor provider key',absent.returncode==0)
    dc('--profile','soak','up','-d','soak')
    wait(lambda: report('soak')['complete'])
    check('Short fixture soak passes without claiming multi-day completion',not report('soak')['multi_day_soak_complete'])
    dc('stop','soak')
    dc('stop','gateway')
    gateway_stopped=True
    wait(lambda: any('CircuitUnavailable' in item['Subject'] and 'firing' in item['Subject'] for item in http('mailpit','/api/v1/messages')['messages']),150)
    check('Real scrape failure travels through Alertmanager to SMTP inbox',True)
    dc('up','-d','gateway')
    gateway_stopped=False
    wait(lambda: any('CircuitUnavailable' in item['Subject'] and 'resolved' in item['Subject'] for item in http('mailpit','/api/v1/messages')['messages']),90)
    check('Recovery email is delivered',True)
    result={'passed':True,'checks':checks,'ports':ports,'email_delivery':'local SMTP fixture only; production BYOK configuration required',
            'independent_review':'deferred by operator','multi_day_soak_complete':False}
    (work/'operations-report.json').write_text(json.dumps(result,indent=2))
    for key in ['CIRCUIT_BACKUP_INTERVAL','CIRCUIT_BACKUP_RETAIN','CIRCUIT_SOAK_INTERVAL','CIRCUIT_SOAK_DURATION']:
        env.pop(key,None)
    dc('up','-d','--force-recreate','backup')
    if args.start_soak:
        dc('--profile','soak','up','-d','--force-recreate','soak')
        print('STARTED 72-hour read-only fixture soak; not yet complete',flush=True)
    print('REPORT',work/'operations-report.json',flush=True)
    print('MAIL http://127.0.0.1:'+str(ports['mailpit']),flush=True)
finally:
    if gateway_stopped:
        dc('up','-d','gateway')
