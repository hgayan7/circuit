#!/usr/bin/env python3
"""Rehearse previous-release upgrade/rollback with one owner and unchanged fixture configuration."""
import argparse
import http.server
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

p = argparse.ArgumentParser()
p.add_argument('--previous', required=True, type=Path)
p.add_argument('--candidate', required=True, type=Path)
a = p.parse_args()
calls = []
class Origin(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        calls.append(body['sku'])
        if body['sku'] == 'lost':
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"ok":true}')
    def log_message(self,*args): pass

with tempfile.TemporaryDirectory(prefix='circuit-compat-') as temp:
    work = Path(temp)
    origin = http.server.ThreadingHTTPServer(('127.0.0.1',0),Origin)
    threading.Thread(target=origin.serve_forever,daemon=True).start()
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0))
        port = sock.getsockname()[1]
    admin, agent = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    env = dict(os.environ, CIRCUIT_COMPAT_ADMIN=admin, CIRCUIT_COMPAT_AGENT=agent)
    cfg = {'name':'compatibility-fixture','admin_token_env':'CIRCUIT_COMPAT_ADMIN','approval_ttl':'1h',
        'custom_tools':[{'id':'inventory','endpoint':f'http://127.0.0.1:{origin.server_port}',
                         'method':'POST','operations':['reserve'],'require_approval':True}],
        'agents':[{'id':'fixture','token_env':'CIRCUIT_COMPAT_AGENT','custom_tools':['inventory'],'actions':['reserve']}],
        'limits':[{'id':'writes','actions':['reserve'],'scope':'custom_tool','window':'1h','max_calls':4}]}
    config, state = work/'gateway.json', work/'state.db'
    config.write_text(json.dumps(cfg))
    log = (work/'gateway.log').open('w')
    process = None
    base = f'http://127.0.0.1:{port}'
    def api(path, token, body=None, key=None):
        headers = {'Authorization':'Bearer '+token,'Content-Type':'application/json'}
        if key: headers['Idempotency-Key'] = key
        req = urllib.request.Request(base+path,headers=headers,data=None if body is None else json.dumps(body).encode())
        try:
            with urllib.request.urlopen(req,timeout=10) as response: return json.load(response)
        except urllib.error.HTTPError as error:
            if error.code in (403,410): return json.load(error)
            raise
    def start(binary):
        global process
        process = subprocess.Popen([str(binary.resolve()),'gateway','serve','--config',str(config),
            '--data',str(state),'--listen',f'127.0.0.1:{port}'],env=env,stdout=log,stderr=log)
        for _ in range(100):
            if process.poll() is not None: raise RuntimeError((work/'gateway.log').read_text())
            try:
                if api('/readyz',admin)['status']=='ready': return
            except OSError: pass
            time.sleep(.1)
        raise AssertionError('not ready')
    def stop():
        if process and process.poll() is None:
            process.terminate()
            try: process.wait(timeout=15)
            except subprocess.TimeoutExpired: process.kill(); process.wait(); raise
    def submit(sku,operation='reserve'):
        return api('/v1/actions',agent,{'operation':operation,'custom_tool':'inventory','args':{'sku':sku}},'compat-'+sku)
    def approve(action):
        return api('/admin/actions/'+action['id']+'/decision',admin,{'digest':action['digest'],'decision':'approve'})
    try:
        start(a.previous)
        success = approve(submit('success'))
        pending = submit('pending')
        denied = submit('denied','outside_scope')
        uncertain = approve(submit('lost'))
        assert [success['state'],pending['state'],denied['state'],uncertain['state']] == ['succeeded','pending','denied','uncertain']
        snapshot = api('/admin/actions',admin)
        stop()
        # Each binary acquires the same store only after the previous process exits.
        for label,binary in [('upgrade',a.candidate),('rollback',a.previous),('upgrade again',a.candidate)]:
            start(binary)
            history = api('/admin/actions',admin)
            assert {x['id']:x for x in history} == {x['id']:x for x in snapshot},label+' changed history'
            assert submit('success')['id'] == success['id']
            assert submit('lost')['id'] == uncertain['id']
            assert submit('pending')['id'] == pending['id']
            assert calls == ['success','lost'],calls
            print('PASS',label+': readiness, history, pending/denied/uncertain states, idempotency and no replay',flush=True)
            stop()
        start(a.candidate)
        assert submit('last-budget-slot')['state']=='pending'
        assert submit('over-budget')['state']=='denied'
        assert calls == ['success','lost'],calls
        stop()
        print('PASS Persisted budget reservations survive upgrade/rollback and deny excess work',flush=True)
        print('PASS rc.2 <-> local candidate: unchanged legacy fixture configuration and current state only',flush=True)
    finally:
        stop()
        origin.shutdown()
        log.close()
