#!/usr/bin/env python3
"""Opt-in live test against dedicated Circuit fixture repositories.

Requires existing gh authentication. Credentials stay in process memory and
the gateway environment; reports contain action IDs and outcomes only.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--repo', required=True)
parser.add_argument('--protected-repo', required=True)
parser.add_argument('--run-id', required=True)
parser.add_argument('--github-user', help='Existing gh account to use (defaults to active account)')
parser.add_argument('--app-id', type=int, help='Use GitHub App authentication in the gateway')
parser.add_argument('--private-key-file', help='GitHub App PEM path, gateway only')
parser.add_argument('--isolated', action='store_true', help='Run gateway and agent in separate Docker containers with agent egress blocked')
args = parser.parse_args()
if bool(args.app_id) != bool(args.private_key_file):
    parser.error('--app-id and --private-key-file must be supplied together')
if not re.fullmatch(r'[A-Za-z0-9_-]{1,80}', args.run_id):
    parser.error('run-id must be 1–80 letters, digits, underscores, or hyphens')
for repo in (args.repo, args.protected_repo):
    if '/circuit-gateway-' not in repo or 'pilot-' not in repo:
        parser.error('Only dedicated circuit-gateway-*-pilot-* fixture repositories are accepted')
root = Path(__file__).resolve().parents[1]
work = root / '.circuit' / ('pilot-' + args.run_id)
work.mkdir(parents=True, exist_ok=False, mode=0o700)
report = {'run_id': args.run_id, 'repositories': [args.repo, args.protected_repo],
          'checks': [], 'pull_requests': [], 'credential': 'Existing gh OAuth token; not a least-privilege credential test'}
if args.app_id:
    report['credential'] = 'Repository-scoped short-lived GitHub App installation tokens'
report['isolated_agent'] = args.isolated

def check(name, condition, **detail):
    if not condition:
        raise AssertionError(name + ': ' + json.dumps(detail))
    report['checks'].append({'name': name, **detail})
    print('PASS', name, json.dumps(detail), flush=True)

def github(endpoint, method='GET', payload=None):
    command = ['gh', 'api', endpoint, '--method', method]
    if payload is not None:
        command += ['--input', '-']
    environment = os.environ.copy()
    environment['GH_TOKEN'] = github_token
    completed = subprocess.run(command, env=environment, input=None if payload is None else json.dumps(payload),
                               text=True, capture_output=True, check=True)
    return json.loads(completed.stdout)

token_command = ['gh', 'auth', 'token', '--hostname', 'github.com']
if args.github_user:
    token_command += ['--user', args.github_user]
github_token = subprocess.run(token_command,
                              text=True, capture_output=True, check=True).stdout.strip()
admin_token, agent_token = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
config = work / 'gateway.yaml'
subprocess.run([str(root / 'bin/circuit'), 'gateway', 'init', '--repo', args.repo, '--out', str(config)],
               check=True, capture_output=True)
source = config.read_text().replace('repositories: [' + json.dumps(args.repo) + ']',
                                   'repositories: ' + json.dumps([args.repo, args.protected_repo]))
source = source.replace('  - id: total-write-hour', '  - id: issue-pilot-cap\n    actions: [create_issue]\n'
                        '    scope: agent_repository\n    window: 1h\n    max_calls: 1\n  - id: total-write-hour')
if args.app_id:
    pem_path = '/app/key.pem' if args.isolated else str(Path(args.private_key_file).resolve())
    source += '\ngithub_app:\n  app_id: ' + str(args.app_id) + '\n  private_key_file: ' + json.dumps(pem_path) + '\n'
config.write_text(source)
with socket.socket() as listener:
    listener.bind(('127.0.0.1', 0))
    port = listener.getsockname()[1]
url = 'http://127.0.0.1:' + str(port)
process = None
network_name = 'circuit-pilot-' + args.run_id
container_name = 'circuit-gateway-' + args.run_id
docker_env = {'CIRCUIT_ADMIN_TOKEN': admin_token, 'CIRCUIT_AGENT_TOKEN': agent_token}
if args.isolated:
    if not args.app_id:
        parser.error('--isolated requires GitHub App credentials')
    for target, package in [('circuit-linux', './cmd/circuit'), ('pilot-agent-linux', './examples/pilot-agent')]:
        subprocess.run(['go', 'build', '-o', str(root / 'bin' / target), package], cwd=root,
                       env={**os.environ, 'GOOS': 'linux', 'GOARCH': 'arm64', 'CGO_ENABLED': '0'}, check=True)
    subprocess.run(['docker', 'pull', 'alpine:3.22'], check=True, capture_output=True)
    subprocess.run(['docker', 'network', 'create', '--internal', network_name], check=True, capture_output=True)

def start():
    global process
    environment = os.environ.copy()
    environment.update(GITHUB_TOKEN='' if args.app_id else github_token, CIRCUIT_ADMIN_TOKEN=admin_token,
                       CIRCUIT_AGENT_TOKEN=agent_token)
    if args.isolated:
        subprocess.run(['docker', 'run', '-d', '--name', container_name, '--network', 'bridge',
                        '-p', '127.0.0.1:' + str(port) + ':8080',
                        '-e', 'CIRCUIT_ADMIN_TOKEN', '-e', 'CIRCUIT_AGENT_TOKEN',
                        '-v', str(root / 'bin/circuit-linux') + ':/app/circuit:ro',
                        '-v', str(Path(args.private_key_file).resolve()) + ':/app/key.pem:ro',
                        '-v', str(work) + ':/state',
                        'alpine:3.22', '/app/circuit', 'gateway', 'serve', '--config', '/state/gateway.yaml',
                        '--data', '/state/gateway.db', '--listen', '0.0.0.0:8080'],
                       env={**os.environ, **docker_env}, check=True, capture_output=True)
        subprocess.run(['docker', 'network', 'connect', network_name, container_name], check=True, capture_output=True)
    else:
        process = subprocess.Popen([str(root / 'bin/circuit'), 'gateway', 'serve', '--config', str(config),
                                '--data', str(work / 'gateway.db'), '--listen', '127.0.0.1:' + str(port)],
                               env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(100):
        if process and process.poll() is not None:
            raise RuntimeError('Gateway exited during startup')
        try:
            api('/admin/actions', admin=True)
            return
        except (OSError, urllib.error.URLError):
            time.sleep(0.1)
    raise RuntimeError('Gateway startup timed out')

def stop():
    if args.isolated:
        subprocess.run(['docker', 'rm', '-f', container_name], capture_output=True)
    if process and process.poll() is None:
        process.terminate()
        process.wait(timeout=40)

def api(path, body=None, key=None, admin=False):
    if args.isolated and path == '/v1/actions' and not admin:
        completed = subprocess.run(['docker', 'run', '--rm', '-i', '--network', network_name,
                                    '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                                    '-e', 'CIRCUIT_AGENT_TOKEN', '-v', str(root / 'bin/pilot-agent-linux') + ':/agent:ro',
                                    'alpine:3.22', '/agent', '--url', 'http://' + container_name + ':8080',
                                    '--key', args.run_id + ':' + key], input=json.dumps(body), text=True,
                                   env={**os.environ, 'CIRCUIT_AGENT_TOKEN': agent_token}, capture_output=True, check=True)
        return json.loads(completed.stdout)
    headers = {'Authorization': 'Bearer ' + (admin_token if admin else agent_token),
               'Content-Type': 'application/json'}
    if key:
        headers['Idempotency-Key'] = args.run_id + ':' + key
    request = urllib.request.Request(url + path, headers=headers,
                                     data=None if body is None else json.dumps(body).encode())
    try:
        response = urllib.request.urlopen(request, timeout=40)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return json.load(response)

def submit(repo, operation, values, key):
    return api('/v1/actions', {'repository': repo, 'operation': operation, 'args': values}, key=key)

def approve(action):
    check('Action waits for exact approval', action['state'] == 'pending', action_id=action.get('id'))
    return api('/admin/actions/' + action['id'] + '/decision',
               {'digest': action['digest'], 'decision': 'approve'}, admin=True)

def successful(action, name):
    check(name, action['state'] == 'succeeded', action_id=action.get('id'), state=action.get('state'),
          http_status=action.get('outcome', {}).get('http_status'))
    return action['outcome']['body']

try:
    start()
    if args.isolated:
        isolated = subprocess.run(['docker', 'run', '--rm', '--network', network_name, '--read-only',
                                    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                                    '-v', str(root / 'bin/pilot-agent-linux') + ':/agent:ro',
                                    'alpine:3.22', '/agent', '--probe'], text=True, capture_output=True, check=True)
        check('Agent has no provider credentials or upstream egress', json.loads(isolated.stdout)['isolated'])
    for index, repo in enumerate((args.repo, args.protected_repo)):
        prefix = str(index)
        base = github('repos/' + repo + '/git/ref/heads/main')['object']['sha']
        branch = 'circuit/' + args.run_id
        branch_values = {'branch': branch, 'sha': base}
        created = submit(repo, 'create_branch', branch_values, prefix + '-branch')
        successful(created, 'Real branch created')
        retry = submit(repo, 'create_branch', branch_values, prefix + '-branch')
        check('Same-key retry returns original action', retry['id'] == created['id'], action_id=retry['id'])
        denied = submit(repo, 'put_file', {'branch': 'main', 'path': 'denied.txt',
                         'content': 'dGVzdA==', 'message': 'Must not execute'}, prefix + '-deny-main')
        check('Default-branch write denied', denied['state'] == 'denied')
        denied = submit(repo, 'put_file', {'branch': branch, 'path': '.github/workflows/denied.yml',
                         'content': 'dGVzdA==', 'message': 'Must not execute'}, prefix + '-deny-workflow')
        check('Workflow write denied', denied['state'] == 'denied')
        values = {'branch': branch, 'path': 'circuit-pilot-' + args.run_id + '.txt',
                  'content': base64.b64encode(b'Synthetic Circuit pilot fixture.\n').decode(),
                  'message': 'Add synthetic Circuit pilot fixture'}
        file_result = successful(approve(submit(repo, 'put_file', values, prefix + '-file')),
                                 'Approved file written on GitHub')
        read = successful(submit(repo, 'read_file', {'path': values['path'], 'ref': branch}, prefix + '-read'),
                          'Real file read')
        check('Read content matches approved bytes', base64.b64decode(read['content']) ==
              base64.b64decode(values['content']))
        pr = successful(submit(repo, 'create_pr', {'head': branch, 'base': 'main',
                        'title': 'Circuit live pilot ' + args.run_id,
                        'body': 'Synthetic test fixtures only. Validates exact-action gateway controls.'},
                               prefix + '-pr'), 'Real pull request created')
        report['pull_requests'].append(pr['html_url'])
        print('PR', pr['html_url'], flush=True)
        head = successful(submit(repo, 'get_pr', {'number': pr['number']}, prefix + '-get'),
                          'Real pull request read')['head']['sha']
        if index == 1:
            values.update(sha=file_result['content']['sha'],
                          content=base64.b64encode(b'Updated synthetic fixture for stale SHA test.\n').decode())
            successful(approve(submit(repo, 'put_file', values, prefix + '-update')), 'Updated PR head')
            stale = approve(submit(repo, 'merge_pr', {'number': pr['number'], 'sha': head}, prefix + '-stale'))
            check('Stale commit cannot merge', stale['state'] == 'failed', reason=stale['reason'])
            head = github('repos/' + repo + '/pulls/' + str(pr['number']))['head']['sha']
            blocked = approve(submit(repo, 'merge_pr', {'number': pr['number'], 'sha': head}, prefix + '-blocked'))
            check('GitHub branch protection rejects approved merge', blocked['state'] == 'failed' and
                  blocked.get('outcome', {}).get('http_status') in (403, 405, 409, 422),
                  http_status=blocked.get('outcome', {}).get('http_status'))
            check('Blocked PR remains unmerged', not github('repos/' + repo + '/pulls/' + str(pr['number']))['merged'])
            github('repos/' + repo + '/statuses/' + head, 'POST',
                   {'state': 'success', 'context': 'circuit-pilot/verified',
                    'description': 'Synthetic pilot: gateway checks passed'})
            time.sleep(2)
        successful(approve(submit(repo, 'merge_pr', {'number': pr['number'], 'sha': head}, prefix + '-merge')),
                   'Approved exact commit merged on GitHub')
        check('GitHub confirms merged PR', github('repos/' + repo + '/pulls/' + str(pr['number']))['merged'])
        issue_values = {'title': 'Circuit pilot fixture ' + args.run_id}
        issue = submit(repo, 'create_issue', issue_values, prefix + '-issue')
        issue_body = successful(issue, 'Real issue created')
        successful(submit(repo, 'update_issue', {'number': issue_body['number'], 'state': 'closed'},
                          prefix + '-close-issue'), 'Test issue closed')
        cap = submit(repo, 'create_issue', {'title': 'Must be blocked by allowance'}, prefix + '-cap')
        check('Real action allowance exhausted', cap['state'] == 'denied')
    denied = submit('hgayan7/circuit', 'create_issue', {'title': 'Must never reach upstream'}, 'deny-repo')
    check('Out-of-scope repository denied', denied['state'] == 'denied')
    history = api('/admin/actions/' + issue['id'] + '/events', admin=True)
    check('Execution history retained', len(history) >= 3)
    stop()
    start()
    retry = submit(args.protected_repo, 'create_issue', issue_values, '1-issue')
    check('Restart preserves idempotency', retry['id'] == issue['id'])
    cap = submit(args.protected_repo, 'create_issue', {'title': 'Still must be blocked'}, 'restart-cap')
    check('Restart preserves consumed allowance', cap['state'] == 'denied')
    report['passed'] = True
except Exception as error:
    report['passed'] = False
    report['error'] = str(error)
    raise
finally:
    stop()
    if args.isolated:
        subprocess.run(['docker', 'network', 'rm', network_name], capture_output=True)
    (work / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print('REPORT', str(work / 'report.json'), flush=True)
