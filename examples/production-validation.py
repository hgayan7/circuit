#!/usr/bin/env python3
"""Bounded local Docker staging validation. Only reads the authorized GitHub fixture."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--app-id', required=True, type=int)
parser.add_argument('--private-key-file', required=True)
parser.add_argument('--repo', required=True)
parser.add_argument('--pr', default=3, type=int)
parser.add_argument('--duration-seconds', default=60, type=int)
parser.add_argument('--keep-running', action='store_true')
args = parser.parse_args()
if not args.repo.startswith('hgayan7/circuit-gateway-') or 'pilot-' not in args.repo:
    raise SystemExit('Use only the authorized hgayan7 fixture repositories')
if args.duration_seconds < 1:
    raise SystemExit('Duration must be positive')
root = Path(__file__).resolve().parents[1]
work = root / '.circuit' / ('production-validation-' + str(int(time.time())))
work.mkdir(mode=0o700)
secret_dir = work / 'secrets'
secret_dir.mkdir(mode=0o700)
tokens = {name: secrets.token_urlsafe(32) for name in ['admin', 'reviewer', 'observer', 'agent']}
for name, token in tokens.items():
    path = secret_dir / (name + '-token')
    path.write_text(token)
    path.chmod(0o444)
shutil.copyfile(args.private_key_file, secret_dir / 'github-app.pem')
(secret_dir / 'github-app.pem').chmod(0o444)
certificate_days = max(3, args.duration_seconds // 86400 + 2)
subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', str(certificate_days),
                '-keyout', str(secret_dir / 'tls.key'), '-out', str(secret_dir / 'tls.crt'),
                '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,DNS:gateway,IP:127.0.0.1'],
               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
(secret_dir / 'tls.key').chmod(0o444)
config = (root / 'deploy/docker/gateway.example.yaml').read_text()
config = config.replace('app_id: 123456', 'app_id: ' + str(args.app_id))
config = config.replace('owner/fixture-repository', args.repo)
config = config.replace('id: owner\n', 'id: hgayan7\n')
(work / 'gateway.yaml').write_text(config)
with socket.socket() as available:
    available.bind(('127.0.0.1', 0))
    port = available.getsockname()[1]
project = 'circuit-staging-' + str(int(time.time()))
env = {**os.environ, 'CIRCUIT_DEPLOY_DIR': str(work), 'CIRCUIT_BIND_PORT': str(port)}
compose = ['docker', 'compose', '-p', project, '-f', str(root / 'deploy/docker/compose.yaml')]
base = 'https://127.0.0.1:' + str(port)
context = ssl.create_default_context(cafile=str(secret_dir / 'tls.crt'))
checks = []
def run(command, **kwargs):
    return subprocess.run(command, check=True, env=env, **kwargs)
def dc(*command, **kwargs):
    return run(compose + list(command), **kwargs)
def check(name, value):
    if not value:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)
def api(path, token=None, body=None, key=None, raw=False):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if key:
        headers['Idempotency-Key'] = key
    request = urllib.request.Request(base + path, headers=headers, data=None if body is None else json.dumps(body).encode())
    try:
        response = urllib.request.urlopen(request, context=context, timeout=35)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = response.read()
        return response.status, data if raw else json.loads(data)
def wait_ready():
    for _ in range(120):
        try:
            if api('/readyz')[0] == 200:
                return
        except OSError:
            pass
        time.sleep(.25)
    raise RuntimeError('Gateway readiness timed out')
def agent(request, key):
    command = ['run', '--rm', '-T', '--no-deps', 'agent', '--url', 'https://gateway:8443',
               '--ca-cert', '/run/secrets/tls.crt', '--token-file', '/run/secrets/agent-token', '--key', key]
    result = dc(*command, input=json.dumps(request), capture_output=True, text=True)
    return json.loads(result.stdout)
success = False
try:
    dc('build', 'gateway', 'agent')
    run(['go', 'build', '-o', str(root / 'bin/circuit'), './cmd/circuit'], cwd=root)
    dc('up', '-d', 'gateway')
    wait_ready()
    check('TLS readiness and liveness', api('/healthz')[0] == 200)
    container = dc('ps', '-q', 'gateway', capture_output=True, text=True).stdout.strip()
    inspected = json.loads(run(['docker', 'inspect', container], capture_output=True, text=True).stdout)[0]
    check('Nonroot readonly capability-free gateway', inspected['Config']['User'] == '10001:10001' and inspected['HostConfig']['ReadonlyRootfs'] and inspected['HostConfig']['CapDrop'] == ['ALL'])
    check('Gateway listener published only on loopback', all(binding['HostIp'] == '127.0.0.1' for bindings in inspected['HostConfig']['PortBindings'].values() for binding in bindings))
    probe = dc('run', '--rm', '-T', '--no-deps', 'agent', '--probe', capture_output=True, text=True)
    check('Agent cannot read gateway secrets or reach upstream', json.loads(probe.stdout)['isolated'])
    check('Agent cannot access operator endpoints', api('/admin/actions', tokens['agent'])[0] == 403)
    check('Observer cannot download sensitive backup', api('/admin/backup', tokens['observer'])[0] == 403)
    check('Named identity available', api('/admin/me', tokens['admin'])[1] == {'id': 'hgayan7', 'role': 'admin'})
    request = {'operation': 'get_pr', 'repository': args.repo, 'args': {'number': args.pr}}
    read = agent(request, 'initial-read')
    check('Isolated agent performs real GitHub App read over TLS', read['state'] == 'succeeded')
    head = read['outcome']['body']['head']['sha']
    merge = agent({'operation': 'merge_pr', 'repository': args.repo, 'args': {'number': args.pr, 'sha': head}}, 'review-only')
    check('Consequential proposal pauses without provider write', merge['state'] == 'pending')
    decision = {'digest': merge['digest'], 'decision': 'reject'}
    check('Observer cannot decide', api('/admin/actions/' + merge['id'] + '/decision', tokens['observer'], decision)[0] == 403)
    rejected = api('/admin/actions/' + merge['id'] + '/decision', tokens['reviewer'], decision)[1]
    check('Reviewer rejection is attributed', rejected['state'] == 'rejected' and rejected['approved_by'] == 'operator:reviewer')
    events = api('/admin/actions/' + merge['id'] + '/events', tokens['observer'])[1]
    check('Audit records named reviewer', any(event['actor'] == 'operator:reviewer' for event in events))
    check('Reviewer cannot reconcile outcomes', api('/admin/actions/' + merge['id'] + '/reconcile', tokens['reviewer'], {'digest':merge['digest'],'state':'failed','note':'fixture'})[0] == 403)
    check('Metrics expose state counts without secrets', b'circuit_actions{state="rejected"} 1' in api('/admin/metrics', tokens['observer'], raw=True)[1])
    run(['docker', 'kill', '--signal', 'KILL', container], capture_output=True)
    run(['docker', 'start', container], capture_output=True)
    wait_ready()
    check('Container crash preserves idempotency', agent(request, 'initial-read')['id'] == read['id'])
    old_token = tokens['agent']
    tokens['agent'] = secrets.token_urlsafe(32)
    temporary = secret_dir / 'agent-token-new'
    temporary.write_text(tokens['agent'])
    temporary.chmod(0o444)
    temporary.replace(secret_dir / 'agent-token')
    dc('up', '-d', '--force-recreate', 'gateway')
    wait_ready()
    check('Old credential revoked after controlled restart', api('/v1/actions', old_token)[0] == 401)
    check('Rotated credential works', api('/v1/actions', tokens['agent'])[0] == 200)
    backup = work / 'verified-backup.db'
    run([str(root / 'bin/circuit'), 'gateway', 'backup', '--url', base, '--ca-cert', str(secret_dir / 'tls.crt'),
         '--token-file', str(secret_dir / 'admin-token'), '--out', str(backup)])
    check('Live backup is private', backup.stat().st_mode & 0o777 == 0o600)
    dc('exec', '-T', 'gateway', 'circuit', 'gateway', 'backup', '--url', 'https://127.0.0.1:8443',
       '--ca-cert', '/run/secrets/tls.crt', '--token-file', '/run/secrets/admin-token', '--out', '/var/lib/circuit/backup.db')
    dc('stop', 'gateway')
    dc('run', '--rm', '-T', '--no-deps', 'gateway', 'gateway', 'restore', '--backup', '/var/lib/circuit/backup.db', '--out', '/var/lib/circuit/restored.db')
    env['CIRCUIT_STATE_FILE'] = '/var/lib/circuit/restored.db'
    dc('up', '-d', '--force-recreate', 'gateway')
    for _ in range(120):
        try:
            if api('/healthz')[0] == 200:
                break
        except OSError:
            pass
        time.sleep(.25)
    check('Restored snapshot pauses dispatch until provider reconciliation', api('/readyz')[0] == 503 and api('/v1/actions', tokens['agent'], request, 'initial-read')[0] == 503)
    dc('stop', 'gateway')
    dc('run', '--rm', '-T', '--no-deps', 'gateway', 'gateway', 'acknowledge-restore', '--data', '/var/lib/circuit/restored.db',
       '--note', 'Isolated read-only fixture: no provider writes occurred after the verified backup; action history reviewed.')
    dc('up', '-d', 'gateway')
    wait_ready()
    check('Restored gateway preserves action idempotency', agent(request, 'initial-read')['id'] == read['id'])
    restored = api('/admin/actions', tokens['admin'])[1]
    check('Restored gateway retains named approval history', any(a['id'] == merge['id'] and a['approved_by'] == 'operator:reviewer' for a in restored))
    started = time.monotonic()
    samples = 0
    provider_reads = 0
    while time.monotonic() - started < args.duration_seconds:
        if api('/readyz')[0] != 200 or api('/admin/metrics', tokens['observer'], raw=True)[0] != 200:
            raise AssertionError('soak health sample failed')
        samples += 1
        if samples % 20 == 1:
            if agent(request, 'soak-read-' + str(samples))['state'] != 'succeeded':
                raise AssertionError('isolated GitHub read failed during soak')
            provider_reads += 1
        time.sleep(min(3, args.duration_seconds))
    check('Bounded staging health soak passes', samples > 0)
    logs = dc('logs', '--no-color', 'gateway', capture_output=True, text=True).stdout
    check('Structured request logs omit credentials', 'http_request' in logs and all(token not in logs for token in tokens.values()) and old_token not in logs)
    report = {'passed': True, 'checks': checks, 'soak_seconds': args.duration_seconds, 'soak_samples': samples,
              'scope': 'Local Docker; real GitHub reads only; no approval caused a provider write',
              'gateway_url': base, 'compose_project': project, 'state_file': env['CIRCUIT_STATE_FILE'],
              'soak_provider_reads': provider_reads, 'multi_day_soak_complete': args.duration_seconds >= 72 * 60 * 60}
    (work / 'report.json').write_text(json.dumps(report, indent=2))
    (work / 'runtime.json').write_text(json.dumps({'project':project,'port':port,'state_file':env['CIRCUIT_STATE_FILE']},indent=2))
    success = True
    print('REPORT', work / 'report.json', flush=True)
    if args.keep_running:
        print('RUNNING', base, 'private credentials:', secret_dir, flush=True)
finally:
    if not success:
        dc('logs', '--no-color', '--tail', '30', 'gateway')
    if not (success and args.keep_running):
        dc('down', '--volumes', '--remove-orphans')
