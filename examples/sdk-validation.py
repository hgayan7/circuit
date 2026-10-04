#!/usr/bin/env python3
"""All three generated clients against the actual TLS gateway and a disposable REST origin."""
import argparse
import http.server
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.request

root = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument('--python', default='python3', help='Python interpreter with sdk/python installed')
a = p.parse_args()
calls = []
stop = threading.Event()
class Origin(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        assert self.headers.get('Authorization') == 'Bearer '+provider_token
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
        self.wfile.write(b'{"ok":true}')
    def do_POST(self):
        assert self.headers.get('Authorization') == 'Bearer '+provider_token
        args = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        calls.append((self.path,args['sku']))
        if self.path == '/lose':
            self.close_connection=True
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            return
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
        self.wfile.write(b'{"ok":true}')
    def log_message(self,*args):pass

with tempfile.TemporaryDirectory(prefix='circuit-sdks-') as temp:
    work = Path(temp)
    binary, operator = work/'circuit', work/'operator'
    subprocess.run(['go','build','-o',str(binary),'./cmd/circuit'],cwd=root,check=True)
    provider_token = secrets.token_urlsafe(32)
    token = work/'provider-token'
    token.write_text(provider_token);token.chmod(0o600)
    origin = http.server.ThreadingHTTPServer(('127.0.0.1',0),Origin)
    threading.Thread(target=origin.serve_forever,daemon=True).start()
    schema = {'type':'object','required':['sku'],'additionalProperties':False,'properties':{'sku':{'type':'string'}}}
    manifest = work/'manifest.json'
    manifest.write_text(json.dumps({'custom_tools':[{'id':'inventory','protocol':'rest-routes-v1',
        'endpoint':f'http://127.0.0.1:{origin.server_port}','token_file':str(token),
        'operations':['lookup','reserve','lose'],'read_only_operations':['lookup'],
        'routes':[{'operation':op,'method':method,'path':path,'input_schema':schema,**({'query_params':['sku']} if method=='GET' else {})}
                  for op,method,path in [('lookup','GET','/lookup'),('reserve','POST','/reserve'),('lose','POST','/lose')]]}]}))
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
    subprocess.run([str(binary),'setup','--non-interactive','--integration','middleware','--preset','review-writes',
        '--upstream-manifest',str(manifest),'--out',str(operator),'--port',str(port)],check=True,stdout=subprocess.DEVNULL)
    # Three languages each claim two writes. Change only this disposable fixture's quota.
    fixture_config = subprocess.run(['go','run','./examples/sdk-fixture','--config',str(operator/'gateway.yaml')],
                                    cwd=root,check=True,capture_output=True,text=True).stdout
    (operator/'gateway.yaml').write_text(fixture_config)
    log = (work/'gateway.log').open('w')
    process = subprocess.Popen([str(binary),'start','--dir',str(operator)],stdout=log,stderr=log)
    try:
        url = f'https://127.0.0.1:{port}'
        ca = operator/'secrets/tls.crt'
        context = ssl.create_default_context(cafile=str(ca))
        reviewer = (operator/'secrets/reviewer-token').read_text().strip()
        def request(path, body=None):
            req = urllib.request.Request(url+path,data=json.dumps(body).encode() if body is not None else None,
                    headers={'Authorization':'Bearer '+reviewer,'Content-Type':'application/json'})
            with urllib.request.urlopen(req,context=context,timeout=10) as response:return json.load(response)
        for _ in range(100):
            try:request('/readyz');break
            except Exception:
                if process.poll() is not None:raise RuntimeError((work/'gateway.log').read_text())
                time.sleep(.1)
        else:raise AssertionError('gateway never ready')
        def approve_fixture_only():
            while not stop.wait(.05):
                try:
                    for action in request('/admin/actions'):
                        if action['state']=='pending':request('/admin/actions/'+action['id']+'/decision',{'digest':action['digest'],'decision':'approve'})
                except Exception:
                    if not stop.is_set():raise
        reviewer_thread = threading.Thread(target=approve_fixture_only,daemon=True)
        reviewer_thread.start()
        env = dict(os.environ,CIRCUIT_GATEWAY_URL=url,CIRCUIT_TOKEN_FILE=str(operator/'secrets/agent-token'),CIRCUIT_CA_CERT=str(ca))
        for command,cwd in [([a.python,'validation.py'],root/'sdk/python'),(['node','validation.cjs'],root/'sdk/typescript'),(['go','run','./cmd/validation'],root/'sdk/go')]:
            subprocess.run(command,cwd=cwd,env=env,check=True,timeout=120)
        for language in ('python','typescript','go'):
            assert calls.count(('/reserve',language)) == 1, calls
            assert calls.count(('/lose',language)) == 1, calls
        print('PASS Three generated SDKs: exactly one approved write and one uncertain dispatch per language; no replay',flush=True)
    finally:
        stop.set()
        process.terminate()
        try:process.wait(timeout=15)
        except subprocess.TimeoutExpired:process.kill();process.wait()
        origin.shutdown();log.close()
