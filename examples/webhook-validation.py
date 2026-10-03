#!/usr/bin/env python3
"""Live App webhook recovery with a deliberately lost merge response."""
import base64
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import time
import urllib.error
import urllib.request

root = Path(__file__).resolve().parents[1]
work = root / '.circuit' / ('webhook-validation-' + str(int(time.time())))
work.mkdir(mode=0o700)
repo = 'hgayan7/circuit-gateway-pilot-20261003'
run = work.name
admin, agent = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
env = {**os.environ, 'CIRCUIT_ADMIN_TOKEN': admin, 'CIRCUIT_AGENT_TOKEN': agent,
       'CIRCUIT_PILOT_WEBHOOK_SECRET': secrets.token_urlsafe(32)}
config = work / 'gateway.yaml'
config.write_text(f'''name: webhook-pilot
approval_ttl: 24h
webhook:
  secret_env: CIRCUIT_PILOT_WEBHOOK_SECRET
agents:
  - id: pilot
    token_env: CIRCUIT_AGENT_TOKEN
    repositories: [{repo}]
    actions: [create_branch, put_file, create_pr, get_pr, merge_pr]
rules:
  - id: file-review
    actions: [put_file]
    action: REQUIRE_APPROVAL
''')
subprocess.run(['go', 'build', '-o', str(root / 'bin/pilot-support'), './examples/pilot-support'], cwd=root, check=True)
process = subprocess.Popen([str(root / 'bin/pilot-support'), '--mode', 'serve', '--config', str(config), '--data', str(work / 'gateway.db')], env=env)
url = 'http://127.0.0.1:55441'
checks = []
hook_id = None
def github(path, method='GET', body=None):
    command = ['gh', 'api', path, '--method', method]
    if body is not None:
        command += ['--input', '-']
    completed = subprocess.run(command, input=None if body is None else json.dumps(body),
                               capture_output=True, text=True, check=True)
    return json.loads(completed.stdout) if completed.stdout.strip() else None
def check(name, condition):
    if not condition:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)
def api(path, body=None, operator=False, key=None):
    headers = {'Authorization': 'Bearer ' + (admin if operator else agent), 'Content-Type': 'application/json'}
    if key:
        headers['Idempotency-Key'] = key
    request = urllib.request.Request(url + path, headers=headers, data=None if body is None else json.dumps(body).encode())
    try:
        response = urllib.request.urlopen(request, timeout=40)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return json.load(response)
def submit(op, args, key):
    return api('/v1/actions', {'operation': op, 'repository': repo, 'args': args}, key=key)
def approve(a):
    check('Exact action pending', a['state'] == 'pending')
    return api('/admin/actions/' + a['id'] + '/decision', {'digest': a['digest'], 'decision': 'approve'}, operator=True)
def success(a, name):
    check(name, a['state'] == 'succeeded')
    return a['outcome']['body']
try:
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError('pilot gateway exited')
        try:
            api('/admin/actions', operator=True)
            break
        except OSError:
            time.sleep(.1)
    hook = github('repos/' + repo + '/hooks', 'POST', {'name': 'web', 'active': True,
                  'events': ['pull_request'], 'config': {'url': os.environ['CIRCUIT_PILOT_WEBHOOK_URL'],
                  'content_type': 'json', 'secret': env['CIRCUIT_PILOT_WEBHOOK_SECRET'], 'insecure_ssl': '0'}})
    hook_id = hook['id']
    try:
        response = urllib.request.urlopen(os.environ['CIRCUIT_PILOT_WEBHOOK_URL'].replace('/webhooks/github', '/admin/actions'))
        public_status = response.status
    except urllib.error.HTTPError as error:
        public_status = error.code
    check('Public tunnel does not expose operator API', public_status == 404)
    # Operator obtains only the base SHA. The App gateway performs all writes.
    sha = subprocess.run(['gh', 'api', 'repos/' + repo + '/git/ref/heads/main', '--jq', '.object.sha'], capture_output=True, text=True, check=True).stdout.strip()
    branch = 'circuit/' + run
    success(submit('create_branch', {'branch': branch, 'sha': sha}, 'branch'), 'Live branch created')
    success(approve(submit('put_file', {'branch': branch, 'path': run + '.txt', 'content': base64.b64encode(b'Synthetic webhook recovery fixture\n').decode(), 'message': 'Webhook recovery fixture'}, 'file')), 'Approved live file written')
    pr = success(submit('create_pr', {'head': branch, 'base': 'main', 'title': run, 'body': 'Synthetic Circuit webhook recovery test only.'}, 'pr'), 'Live PR created')
    print('PR', pr['html_url'], flush=True)
    head = success(submit('get_pr', {'number': pr['number']}, 'get'), 'Live PR read')['head']['sha']
    values = {'number': pr['number'], 'sha': head}
    merge = approve(submit('merge_pr', values, 'merge'))
    check('Lost merge response requires reconciliation', merge['state'] == 'uncertain')
    retry = submit('merge_pr', values, 'merge')
    check('Uncertain retry returns original action', retry['id'] == merge['id'])
    for _ in range(120):
        observed = api('/v1/actions/' + merge['id'])
        if observed['state'] == 'succeeded':
            break
        time.sleep(1)
    check('Real signed webhook reconciles exact approved head', observed['state'] == 'succeeded' and observed['outcome']['body']['reconciled_by'] == 'webhook')
    events = api('/admin/actions/' + merge['id'] + '/events', operator=True)
    check('Webhook attribution persisted', any(e.get('actor') == 'webhook' for e in events))
    deliveries = github('repos/' + repo + '/hooks/' + str(hook_id) + '/deliveries')
    check('GitHub confirms successful delivery', any(d['event'] == 'pull_request' and d['status_code'] == 200 for d in deliveries))
    deliveries = [{'event': d['event'], 'status_code': d['status_code'], 'delivered_at': d['delivered_at']} for d in deliveries]
    (work / 'report.json').write_text(json.dumps({'passed': True, 'checks': checks, 'pull_request': pr['html_url'], 'deliveries': deliveries}, indent=2))
    print('REPORT', work / 'report.json', flush=True)
finally:
    if hook_id:
        github('repos/' + repo + '/hooks/' + str(hook_id), 'DELETE')
    if process.poll() is None:
        process.send_signal(signal.SIGINT)
        process.wait(timeout=15)
