#!/usr/bin/env python3
"""Disposable CLI/browser pilot. Requires the local PostgreSQL fixture cluster."""
import json
import os
from pathlib import Path
import socket
import shutil
import subprocess
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

root = Path(__file__).resolve().parents[1]
work = root / '.circuit' / ('local-validation-' + str(int(time.time())))
work.mkdir(mode=0o700)
workspace = work / 'workspace'
workspace.mkdir()
(workspace / 'existing.txt').write_text('original fixture')
admin = 'local-validation-operator-public-fixture-only'
agent = 'local-validation-agent-public-fixture-only'
checks = []
def check(name, condition):
    if not condition:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)

class LostResponse(BaseHTTPRequestHandler):
    calls = 0
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', 0)))
        LostResponse.calls += 1
        self.connection.shutdown(socket.SHUT_RDWR)
        self.connection.close()
    def log_message(self, *args):
        pass
origin = ThreadingHTTPServer(('127.0.0.1', 0), LostResponse)
threading.Thread(target=origin.serve_forever, daemon=True).start()
config = work / 'gateway.yaml'
config.write_text(f'''name: local-validation
approval_ttl: 24h
admin_token_env: CIRCUIT_ADMIN_TOKEN
workspaces:
  - id: local
    path: {json.dumps(str(workspace))}
databases:
  - id: local
    driver: postgres
    dsn_env: CIRCUIT_TEST_POSTGRES_DSN
    max_timeout: 2s
    allow_tables: [circuit_cli_items]
custom_tools:
  - id: lost-response
    endpoint: http://127.0.0.1:{origin.server_port}
    operations: [record_fixture]
agents:
  - id: pilot
    token_env: CIRCUIT_AGENT_TOKEN
    workspaces: [local]
    databases: [local]
    custom_tools: [lost-response]
    actions: [read_file, write_file, exec_cmd, query_sql, exec_sql, record_fixture]
limits:
  - id: file-budget
    actions: [write_file]
    scope: agent_workspace
    window: 1h
    max_calls: 2
''')
subprocess.run([str(root / 'bin/circuit'), 'gateway', 'check', str(config)], check=True)
psql = os.environ.get('CIRCUIT_PSQL_BIN') or shutil.which('psql') or '/opt/homebrew/Cellar/postgresql@18/18.6/bin/psql'
subprocess.run([psql, os.environ['CIRCUIT_TEST_POSTGRES_DSN'], '-v', 'ON_ERROR_STOP=1', '-c',
                'CREATE TABLE circuit_cli_items(id integer PRIMARY KEY, value integer); INSERT INTO circuit_cli_items VALUES (1,10),(2,20);'], check=True, capture_output=True)
url = 'http://127.0.0.1:55445'
process = None
def api(path, body=None, operator=False, key=None):
    headers = {'Authorization': 'Bearer ' + (admin if operator else agent), 'Content-Type': 'application/json'}
    if key:
        headers['Idempotency-Key'] = key
    req = urllib.request.Request(url + path, headers=headers, data=None if body is None else json.dumps(body).encode())
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return json.load(response)
def start():
    global process
    env = {**os.environ, 'GITHUB_TOKEN': '', 'CIRCUIT_ADMIN_TOKEN': admin, 'CIRCUIT_AGENT_TOKEN': agent}
    process = subprocess.Popen([str(root / 'bin/circuit'), 'gateway', 'serve', '--config', str(config), '--data', str(work / 'gateway.db'), '--listen', '127.0.0.1:55445'], env=env)
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError('gateway startup failed')
        try:
            api('/admin/actions', operator=True)
            return
        except OSError:
            time.sleep(.1)
    raise RuntimeError('startup timeout')
def stop():
    if process and process.poll() is None:
        process.terminate()
        process.wait(timeout=15)
def submit(op, target, args, key):
    return api('/v1/actions', {'operation': op, **target, 'args': args}, key=key)
try:
    start()
    read = submit('query_sql', {'database': 'local'}, {'query': 'SELECT * FROM circuit_cli_items'}, 'query')
    check('CLI executes real PostgreSQL query', read['state'] == 'succeeded')
    bound = submit('exec_sql', {'database': 'local'}, {'query': 'UPDATE circuit_cli_items SET value=99 WHERE id>0', 'max_affected_rows': 1}, 'rollback')
    check('REST row limit fails and rolls back', bound['state'] == 'failed' and 'rolled back' in bound['reason'])
    read = submit('query_sql', {'database': 'local'}, {'query': 'SELECT * FROM circuit_cli_items'}, 'query-after')
    check('Rollback preserved real rows', all(row[1] != 99 for row in read['outcome']['body']['rows']))
    implicit = submit('write_file', {'workspace': 'local'}, {'path': 'existing.txt', 'content': 'wrong'}, 'implicit-overwrite')
    check('Implicit overwrite fails', implicit['state'] == 'failed')
    overwrite = submit('write_file', {'workspace': 'local'}, {'path': 'existing.txt', 'content': 'browser-approved fixture', 'overwrite': True}, 'browser-overwrite')
    check('Explicit overwrite waits for review', overwrite['state'] == 'pending')
    blocked = api('/admin/actions/' + overwrite['id'] + '/decision', {'digest': overwrite['digest'], 'decision': 'approve'})
    check('Agent cannot approve', 'error' in blocked)
    uncertain = submit('record_fixture', {'custom_tool': 'lost-response'}, {'payload': {'fixture': True}}, 'lost-response')
    check('Lost provider response is uncertain', uncertain['state'] == 'uncertain')
    retry = submit('record_fixture', {'custom_tool': 'lost-response'}, {'payload': {'fixture': True}}, 'lost-response')
    check('Uncertain retry never replays', retry['id'] == uncertain['id'] and LostResponse.calls == 1)
    stop()
    start()
    check('Restart preserves uncertain action', api('/v1/actions/' + uncertain['id'])['state'] == 'uncertain')
    print('BROWSER', url, 'OPERATOR', admin, flush=True)
    print('Approve write_file and confirm the uncertain action completed: the local fixture recorded exactly one request.', flush=True)
    for _ in range(3600):
        actions = api('/admin/actions', operator=True)
        states = {a['id']: a['state'] for a in actions}
        if states.get(overwrite['id']) == 'succeeded' and states.get(uncertain['id']) == 'succeeded':
            break
        time.sleep(1)
    else:
        raise RuntimeError('browser validation timed out')
    check('Browser approval executed exact file payload', (workspace / 'existing.txt').read_text() == 'browser-approved fixture')
    check('Browser reconciliation did not replay provider', LostResponse.calls == 1)
    events = api('/admin/actions/' + overwrite['id'] + '/events', operator=True)
    check('Operator decision persisted in history', any(e.get('actor') == 'operator' for e in events))
    stop()
    start()
    retry = submit('write_file', {'workspace': 'local'}, {'path': 'existing.txt', 'content': 'browser-approved fixture', 'overwrite': True}, 'browser-overwrite')
    check('Restart preserves approved action and idempotency', retry['id'] == overwrite['id'] and retry['state'] == 'succeeded')
    cap = submit('write_file', {'workspace': 'local'}, {'path': 'another.txt', 'content': 'denied'}, 'cap')
    check('Restart preserves consumed budget', cap['state'] == 'denied')
    (work / 'report.json').write_text(json.dumps({'passed': True, 'checks': checks}, indent=2))
    print('REPORT', work / 'report.json', flush=True)
finally:
    stop()
    origin.shutdown()
    subprocess.run([psql, os.environ['CIRCUIT_TEST_POSTGRES_DSN'], '-c', 'DROP TABLE IF EXISTS circuit_cli_items'], capture_output=True)
