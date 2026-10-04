#!/usr/bin/env python3
"""Disposable BYOK REST middleware rehearsal through the actual setup/start CLI."""
import http.server
import json
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

root = Path(__file__).resolve().parents[1]
checks = []
def check(name, value):
    if not value:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)

with tempfile.TemporaryDirectory(prefix='circuit-middleware-validation-') as tmp:
    work = Path(tmp).resolve()
    binary = work/'circuit'
    subprocess.run(['go','build','-o',str(binary),'./cmd/circuit'],cwd=root,check=True)
    upstream_token = secrets.token_urlsafe(32)
    upstream_file = work/'upstream-token'
    upstream_file.write_text(upstream_token)
    upstream_file.chmod(0o600)
    calls = []
    class Upstream(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            if self.headers.get('Authorization') != 'Bearer '+upstream_token or self.path != '/reserve':
                self.send_error(403)
                return
            data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            calls.append((self.headers.get('Idempotency-Key'),data))
            self.send_response(200)
            self.send_header('Content-Type','application/json')
            self.end_headers()
            self.wfile.write(b'{"ok":true}')
        def log_message(self,*args):
            pass
    upstream = http.server.ThreadingHTTPServer(('127.0.0.1',0),Upstream)
    threading.Thread(target=upstream.serve_forever,daemon=True).start()
    gateway = None
    try:
        schema = {'type':'object','additionalProperties':False,'required':['sku'],
                  'properties':{'sku':{'type':'string'}}}
        manifest = {'custom_tools':[{'id':'inventory','protocol':'rest-routes-v1',
                    'endpoint':f'http://127.0.0.1:{upstream.server_port}',
                    'token_file':str(upstream_file),'operations':['reserve_item'],
                    'routes':[{'operation':'reserve_item','method':'POST','path':'/reserve',
                               'input_schema':schema}]}]}
        manifest_file = work/'manifest.yaml'
        manifest_file.write_text(json.dumps(manifest))
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0))
            port = sock.getsockname()[1]
        operator = work/'operator'
        subprocess.run([str(binary),'setup','--non-interactive','--integration','middleware',
                        '--preset','review-writes','--upstream-manifest',str(manifest_file),
                        '--out',str(operator),'--port',str(port)],check=True,
                       stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        setup = json.loads((operator/'setup.json').read_text())
        connection = setup['connection']
        agent = Path(connection['token_file']).read_text().strip()
        reviewer = (operator/'secrets/reviewer-token').read_text().strip()
        tls = ssl.create_default_context(cafile=connection['ca_cert'])
        def request(path,method,token=None,key=None,body=None):
            headers = {'Content-Type':'application/json'}
            if token:
                headers['Authorization'] = 'Bearer '+token
            if key:
                headers['Idempotency-Key'] = key
            data = None if body is None else json.dumps(body).encode()
            req = urllib.request.Request(connection['url']+path,data=data,method=method,headers=headers)
            try:
                response = urllib.request.urlopen(req,context=tls,timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                return response.code,json.load(response)
        with (work/'gateway.log').open('w') as log:
            gateway = subprocess.Popen([str(binary),'start','--dir',str(operator)],stdout=log,stderr=log)
            until = time.monotonic()+30
            while True:
                try:
                    if request('/readyz','GET')[0]==200:
                        break
                except (OSError,urllib.error.URLError):
                    if time.monotonic()>until:
                        raise AssertionError('Middleware gateway startup timed out')
                    time.sleep(.1)
            check('Guided manifest import starts a real verified-TLS middleware gateway',True)
            subprocess.run([str(binary),'doctor','--dir',str(operator)],check=True,timeout=30,
                           stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            check('Doctor verifies agent-scoped forwarding tool discovery',True)
            action = {'operation':'reserve_item','custom_tool':'inventory','args':{'sku':'fixture'}}
            status,pending = request('/v1/actions','POST',agent,'fixture-write',action)
            check('REST write waits for exact approval without upstream execution',
                  status==202 and pending['state']=='pending' and not calls)
            path = '/admin/actions/'+pending['id']+'/decision'
            decision = {'digest':pending['digest'],'decision':'approve'}
            check('Agent cannot approve its own write',request(path,'POST',agent,body=decision)[0]==403)
            status,approved = request(path,'POST',reviewer,body=decision)
            check('Reviewer approval executes exactly the configured route and JSON body',
                  status==200 and approved['state']=='succeeded' and calls==[(pending['id'],{'sku':'fixture'})])
            status,retry = request('/v1/actions','POST',agent,'fixture-write',action)
            check('Retry returns the durable action without a second upstream call',
                  status==200 and retry['id']==pending['id'] and len(calls)==1)
            action['custom_tool'] = 'unscoped'
            status,denied = request('/v1/actions','POST',agent,'fixture-denied',action)
            check('Unregistered target is denied before forwarding',
                  status==403 and denied['state']=='denied' and len(calls)==1)
            export = (operator/'mcp.json').read_text()
            check('Agent export contains no upstream/operator credentials',
                  upstream_token not in export and reviewer not in export and 'upstream-token' not in export)
        gateway.terminate()
        gateway.wait(timeout=10)
        check('Logs contain no upstream or Circuit credentials',
              all(token not in (work/'gateway.log').read_text() for token in (upstream_token,agent,reviewer)))
    finally:
        if gateway is not None and gateway.poll() is None:
            gateway.terminate()
            try:
                gateway.wait(timeout=10)
            except subprocess.TimeoutExpired:
                gateway.kill()
                gateway.wait(timeout=5)
        upstream.shutdown()
        upstream.server_close()
print(json.dumps({'checks':checks,'count':len(checks)}))
