#!/usr/bin/env python3
"""Read-only Docker runner checks against an existing disposable fixture gateway."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import tempfile
import threading
import time
import ssl
import urllib.request
import urllib.error
import uuid

p = argparse.ArgumentParser()
p.add_argument('--url')
p.add_argument('--gateway-url', default='https://gateway:8443')
p.add_argument('--network')
p.add_argument('--token-file')
p.add_argument('--ca-cert')
p.add_argument('--image', default='circuit-onboarding-test:local')
a = p.parse_args()
root = Path(__file__).resolve().parents[1]

@contextlib.contextmanager
def fixture():
    if a.url:
        if not all((a.network, a.token_file, a.ca_cert)):
            p.error('Existing fixture requires --network, --token-file, and --ca-cert')
        yield
        return
    if any((a.network, a.token_file, a.ca_cert)):
        p.error('Pass all existing-fixture options together, or none for an isolated workspace fixture')
    name = 'circuit-sandbox-fixture-'+uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix=name) as tmp:
        work = Path(tmp).resolve()
        binary = work/'circuit'
        operator = work/'operator'
        subprocess.run(['go','build','-o',str(binary),'./cmd/circuit'],cwd=root,check=True)
        subprocess.run([str(binary),'setup','--non-interactive','--integration','workspace',
                        '--workspace',str(root/'docs'),'--out',str(operator),'--port','8643'],
                       check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        cert, key = work/'gateway.crt', work/'gateway.key'
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1',
                        '-subj','/CN=gateway','-addext','subjectAltName=DNS:gateway,IP:127.0.0.1',
                        '-keyout',str(key),'-out',str(cert)],check=True,
                       stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        # Only this throwaway fixture relaxes source modes under a private host parent.
        # The non-root gateway reads individual read-only Docker mounts.
        for path in operator.rglob('*'):
            path.chmod(0o755 if path.is_dir() else 0o444)
        operator.chmod(0o755)
        key.chmod(0o444)
        try:
            subprocess.run(['docker','network','create','--internal',name],check=True,stdout=subprocess.DEVNULL)
            subprocess.run(['docker','network','create',name+'-upstream'],check=True,stdout=subprocess.DEVNULL)
            subprocess.run(['docker','run','-d','--name',name,'--network',name+'-upstream',
                            '--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
                            '-p','127.0.0.1::8643','--mount',f'type=bind,src={operator},dst={operator},readonly',
                            '--mount',f'type=bind,src={root/"docs"},dst={root/"docs"},readonly',
                            '--mount',f'type=bind,src={cert},dst=/run/tls.crt,readonly',
                            '--mount',f'type=bind,src={key},dst=/run/tls.key,readonly',
                            '--mount',f'type=volume,src={name},dst=/var/lib/circuit',
                            '--entrypoint','circuit',a.image,'gateway','serve','--config',str(operator/'gateway.yaml'),
                            '--data','/var/lib/circuit/state.db','--listen','0.0.0.0:8643',
                            '--tls-cert','/run/tls.crt','--tls-key','/run/tls.key'],
                           check=True,stdout=subprocess.DEVNULL)
            subprocess.run(['docker','network','connect','--alias','gateway',name,name],check=True)
            port = subprocess.check_output(['docker','port',name,'8643/tcp'],text=True).strip().rsplit(':',1)[1]
            a.url = 'https://127.0.0.1:'+port
            a.gateway_url = 'https://gateway:8643'
            a.network, a.token_file, a.ca_cert = name, str(operator/'secrets/agent-token'), str(cert)
            tls = ssl.create_default_context(cafile=str(cert))
            until = time.monotonic()+30
            while True:
                try:
                    with urllib.request.urlopen(a.url+'/readyz',context=tls,timeout=2) as response:
                        if response.status==200:
                            break
                except (OSError, urllib.error.URLError):
                    if time.monotonic()>until:
                        raise AssertionError('Fixture gateway did not become ready')
                    time.sleep(.1)
            yield
        finally:
            for args in (['rm','-f',name],['volume','rm',name],['network','rm',name],['network','rm',name+'-upstream']):
                subprocess.run(['docker']+args,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=20)
checks = []
def check(name, value):
    if not value:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)
def containers():
    return set(subprocess.check_output(['docker','ps','-a','--filter','name=circuit-agent-',
                                      '--format','{{.Names}}'],text=True).splitlines())
with fixture(), tempfile.TemporaryDirectory(prefix='circuit-sandbox-validation-') as tmp:
    work = Path(tmp)
    binary = work/'circuit'
    subprocess.run(['go','build','-o',str(binary),'./cmd/circuit'],cwd=root,check=True)
    token = work/'token'
    token.write_bytes(Path(a.token_file).read_bytes())
    token.chmod(0o600)
    ca = work/'ca.crt'
    ca.write_bytes(Path(a.ca_cert).read_bytes())
    workspace = work/'workspace'
    workspace.mkdir(mode=0o755)
    base = [str(binary),'agent','run','--image',a.image,'--network',a.network,
            '--url',a.url,'--gateway-url',a.gateway_url,'--token-file',str(token),
            '--ca-cert',str(ca),'--workspace',str(workspace)]
    before = containers()
    shell = '''set -eu
test "$(id -u)" = 10001
test ! -e /var/run/docker.sock
test ! -e /run/circuit/admin-token
test ! -e /run/circuit/github-app.pem
test -r /run/circuit/agent-token
test "$(awk '/CapEff/ {print $2}' /proc/self/status)" = 0000000000000000
grep -q 'NoNewPrivs:[[:space:]]*1' /proc/self/status
if touch /workspace/blocked 2>/dev/null; then exit 1; fi
if touch /rootfs-blocked 2>/dev/null; then exit 1; fi
if timeout 4 wget -q -O /tmp/egress https://api.github.com 2>/dev/null; then exit 1; fi
echo isolation-ok
'''
    result = subprocess.run(base+['--','/bin/sh','-c',shell],capture_output=True,text=True,timeout=40)
    check('Non-root, zero capabilities, no new privileges, restricted mounts, blocked external egress',
          result.returncode==0 and 'isolation-ok' in result.stdout)
    check('Normal exit removes the owned agent container',containers()==before)
    with (work/'connector.log').open('w') as log:
        process = subprocess.Popen(base+['--','circuit','connect','--url',a.gateway_url,
                                   '--token-file','/run/circuit/agent-token','--mounted-token',
                                   '--ca-cert','/run/circuit/ca.crt'],stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE,stderr=log,text=True)
        lines = queue.Queue()
        def reader():
            for line in process.stdout:
                lines.put(line)
            lines.put('')
        threading.Thread(target=reader,daemon=True).start()
        def rpc(number, method, params):
            process.stdin.write(json.dumps({'jsonrpc':'2.0','id':number,'method':method,'params':params})+'\n')
            process.stdin.flush()
            while True:
                line = lines.get(timeout=25)
                if not line:
                    raise AssertionError('Container connector exited unexpectedly')
                reply = json.loads(line)
                if reply.get('id')==number:
                    return reply
        try:
            reply = rpc(1,'initialize',{'protocolVersion':'2025-06-18','capabilities':{},
                                      'clientInfo':{'name':'sandbox-fixture','version':'1'}})
            check('Container connector initializes MCP over verified gateway TLS','result' in reply)
            process.stdin.write(json.dumps({'jsonrpc':'2.0','method':'notifications/initialized'})+'\n')
            process.stdin.flush()
            tools = rpc(2,'tools/list',{})['result']['tools']
            check('Container connector discovers scoped agent tools',len(tools)>0)
            process.stdin.close()
            process.wait(timeout=20)
            check('MCP EOF cleans up the container',process.returncode==0 and containers()==before)
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=25)
    process = subprocess.Popen(base+['--','/bin/sh','-c','echo running; sleep 120'],
                               stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True)
    started = queue.Queue()
    threading.Thread(target=lambda: started.put(process.stdout.readline()),daemon=True).start()
    try:
        check('Long-running sandbox starts',started.get(timeout=25).strip()=='running')
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=25)
        check('Interrupt removes only the owned container',process.returncode!=0 and containers()==before)
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=25)
    check('Runner/connector logs do not contain the scoped credential',
          token.read_text().strip() not in (work/'connector.log').read_text())
print(json.dumps({'checks':checks,'count':len(checks)}))
