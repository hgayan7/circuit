#!/usr/bin/env python3
"""Exercise generated setup, HTTPS gateway, and stdio MCP without provider keys."""
import json
import os
import queue
from pathlib import Path
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
with tempfile.TemporaryDirectory(prefix='circuit-onboarding-validation-') as tmp:
    work = Path(tmp)
    binary = work/'circuit'
    subprocess.run(['go','build','-o',str(binary),'./cmd/circuit'],cwd=root,check=True)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0))
        port = sock.getsockname()[1]
    setup = work/'operator'
    subprocess.run([str(binary),'setup','--non-interactive','--integration','workspace',
                    '--workspace',str(root/'docs'),'--out',str(setup),'--port',str(port)],check=True)
    manifest = json.loads((setup/'setup.json').read_text())
    connection = manifest['connection']
    ca = ssl.create_default_context(cafile=connection['ca_cert'])
    token = Path(connection['token_file']).read_text().strip()
    config = json.loads((setup/'mcp.json').read_text())['mcpServers']['circuit']
    check('Client export contains agent-only references, not provider/operator credentials',
          'admin-token' not in json.dumps(config) and token not in json.dumps(config))
    with (work/'gateway.log').open('w') as gateway_log, (work/'connector.log').open('w') as connector_log:
        gateway = subprocess.Popen([str(binary),'start','--dir',str(setup)],stdout=gateway_log,stderr=gateway_log)
        connector = None
        try:
            until = time.monotonic()+30
            while True:
                try:
                    with urllib.request.urlopen(connection['url']+'/readyz',context=ca,timeout=2) as response:
                        if response.status == 200:
                            break
                except (OSError,urllib.error.URLError):
                    if time.monotonic()>until:
                        raise AssertionError('Gateway startup timed out')
                    time.sleep(.1)
            subprocess.run([str(binary),'doctor','--dir',str(setup)],check=True,timeout=30)
            check('Generated local TLS gateway starts and doctor verifies scoped discovery',True)
            connector = subprocess.Popen([config['command']]+config['args'],stdin=subprocess.PIPE,
                                         stdout=subprocess.PIPE,stderr=connector_log,text=True)
            lines = queue.Queue()
            def read_lines():
                for line in connector.stdout:
                    lines.put(line)
                lines.put('')
            threading.Thread(target=read_lines,daemon=True).start()
            def rpc(number,method,params):
                connector.stdin.write(json.dumps({'jsonrpc':'2.0','id':number,'method':method,'params':params})+'\n')
                connector.stdin.flush()
                while True:
                    line = lines.get(timeout=15)
                    if not line:
                        raise AssertionError('MCP connector closed unexpectedly')
                    result = json.loads(line)
                    if result.get('id')==number:
                        return result
            initialized = rpc(1,'initialize',{'protocolVersion':'2025-06-18','capabilities':{},'clientInfo':{'name':'fixture','version':'1'}})
            check('Stdio connector performs MCP initialization','result' in initialized)
            connector.stdin.write(json.dumps({'jsonrpc':'2.0','method':'notifications/initialized'})+'\n')
            connector.stdin.flush()
            tools = rpc(2,'tools/list',{})['result']['tools']
            names = {tool['name'] for tool in tools}
            check('Read-only preset exposes file reads, not shell or GitHub writes',
                  'file_read' in names and 'shell_exec_cmd' not in names and 'github_merge_pr' not in names)
            request = {'name':'file_read','arguments':{'workspace':'workspace','idempotency_key':'fixture-read',
                       'args':{'path':'safety.md'}}}
            first = json.loads(rpc(3,'tools/call',request)['result']['content'][0]['text'])
            second = json.loads(rpc(4,'tools/call',request)['result']['content'][0]['text'])
            check('Real workspace read succeeds and retry preserves durable action identity',
                  first['state']=='succeeded' and first['id']==second['id'])
            denied = rpc(5,'tools/call',{'name':'file_read','arguments':{'workspace':'unscoped',
                     'idempotency_key':'fixture-denied','args':{'path':'safety.md'}}})
            check('Out-of-scope target is denied by gateway core',denied['result']['isError'])
            request = urllib.request.Request(connection['url']+'/admin/actions',headers={'Authorization':'Bearer '+token})
            try:
                urllib.request.urlopen(request,context=ca,timeout=5)
                raise AssertionError('Agent gained operator access')
            except urllib.error.HTTPError as error:
                check('Agent credential cannot access operator routes',error.code==403)
            connector.stdin.close()
            connector.wait(timeout=10)
            check('Stdio connector shuts down when its client exits',connector.returncode==0)
        finally:
            for process in [connector,gateway]:
                if process is not None and process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
    check('Credentials are absent from gateway and connector logs',
          token not in (work/'gateway.log').read_text() and token not in (work/'connector.log').read_text())
print('PASS',len(checks),'onboarding checks; local workspace only, no sandbox isolation claim',flush=True)
