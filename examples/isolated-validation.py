#!/usr/bin/env python3
"""Disposable real Docker gateway/agent/firewall and approval rehearsal. No production credentials."""
import argparse
import json
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import urllib.request
import ssl

root = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument('--gateway-image', default='circuit-gateway:local')
p.add_argument('--boundary-image', default='circuit-boundary:local')
p.add_argument('--agent-image', default='python:3.13-alpine')
p.add_argument('--binary', type=Path, help='Use an extracted release binary')
a = p.parse_args()
checks = []
def run(*args, **kwargs):
    try:
        return subprocess.run(list(args), check=True, text=True, **kwargs)
    except subprocess.CalledProcessError as error:
        print(error.stdout or '', error.stderr or '', flush=True)
        raise
def check(name, condition):
    if not condition:
        raise AssertionError(name)
    checks.append(name)
    print('PASS', name, flush=True)

with tempfile.TemporaryDirectory(prefix='circuit-isolated-') as temp:
    work = Path(temp)
    binary, operator, workspace = work/'circuit', work/'operator', work/'workspace'
    workspace.mkdir()
    if a.binary:
        binary = a.binary.resolve()
    else:
        run('go','build','-o',str(binary),'./cmd/circuit',cwd=root)
    token = secrets.token_urlsafe(32)
    provider_token = work/'provider-token'
    provider_token.write_text(token)
    provider_token.chmod(0o600)
    fixture_token = work/'fixture-token'
    fixture_token.write_text(token)
    fixture_token.chmod(0o444)
    cert, key = work/'upstream.crt', work/'upstream.key'
    name = 'circuit-upstream-'+secrets.token_hex(6)
    peer_name = name+'-peer'
    run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1',
        '-keyout',str(key),'-out',str(cert),'-subj',f'/CN={name}',
        '-addext',f'subjectAltName=DNS:{name}',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    cert.chmod(0o444)
    key.chmod(0o444)
    fixture = work/'fixture.py'
    fixture.write_text('''import http.server,json,ssl
calls=[]
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(calls).encode())
 def do_POST(self):
  if self.headers.get('Authorization') != 'Bearer '+open('/fixture/token').read().strip() or self.path != '/reserve':
   self.send_error(403);return
  calls.append(json.loads(self.rfile.read(int(self.headers['Content-Length']))))
  self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"ok":true}')
 def log_message(self,*args):pass
s=http.server.HTTPServer(('0.0.0.0',8444),Handler)
c=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);c.load_cert_chain('/fixture/cert','/fixture/key')
s.socket=c.wrap_socket(s.socket,server_side=True);s.serve_forever()
''')
    manifest = work/'upstream.json'
    manifest.write_text(json.dumps({'custom_tools':[{'id':'inventory','protocol':'rest-routes-v1',
        'endpoint':f'https://{name}:8444','token_file':str(provider_token),'ca_cert':str(cert),
        'operations':['reserve'],'routes':[{'operation':'reserve','method':'POST','path':'/reserve',
        'input_schema':{'type':'object','required':['sku'],'additionalProperties':False,
                        'properties':{'sku':{'type':'string'}}}}]}]}))
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
    run(str(binary),'setup','--non-interactive','--integration','middleware','--preset','review-writes',
        '--upstream-manifest',str(manifest),'--out',str(operator),'--port',str(port),stdout=subprocess.DEVNULL)
    d = None
    try:
        run(str(binary),'up','--dir',str(operator),'--gateway-image',a.gateway_image)
        d = json.loads((operator/'deployment/deployment.json').read_text())
        run('docker','run','-d','--name',name,'--read-only','--cap-drop','ALL',
            '--network',d['project']+'_upstream',
            '-v',f'{fixture}:/fixture/server.py:ro','-v',f'{cert}:/fixture/cert:ro',
            '-v',f'{key}:/fixture/key:ro','-v',f'{fixture_token}:/fixture/token:ro',
            '--entrypoint','python','python:3.13-alpine','/fixture/server.py',stdout=subprocess.DEVNULL)
        inspect = json.loads(run('docker','inspect',d['gateway'],capture_output=True).stdout)[0]
        gateway_ip = inspect['NetworkSettings']['Networks'][d['network']]['IPAddress']
        host_ip = run('docker','network','inspect',d['network'],'--format','{{(index .IPAM.Config 0).Gateway}}',capture_output=True).stdout.strip()
        upstream = json.loads(run('docker','inspect',name,capture_output=True).stdout)[0]
        upstream_ip = upstream['NetworkSettings']['Networks'][d['project']+'_upstream']['IPAddress']
        # A live same-network peer is reachable without the boundary, not just a closed port.
        run('docker','run','-d','--name',peer_name,'--read-only','--cap-drop','ALL',
            '--network',d['network'],'--entrypoint','python',a.agent_image,
            '-m','http.server','8444',stdout=subprocess.DEVNULL)
        peer = json.loads(run('docker','inspect',peer_name,capture_output=True).stdout)[0]
        peer_ip = peer['NetworkSettings']['Networks'][d['network']]['IPAddress']
        run('docker','run','--rm','--network',d['network'],'--entrypoint','python',a.agent_image,
            '-c',f"import socket,time;time.sleep(.5);socket.create_connection(('{peer_ip}',8444),timeout=3).close()",stdout=subprocess.DEVNULL)
        base = [str(binary),'agent','run','--dir',str(operator),'--image',a.agent_image,
                '--boundary-image',a.boundary_image,'--workspace',str(workspace),'--']
        script = '''import os,socket,ssl,json,urllib.request
from pathlib import Path
assert os.getuid()==10001
assert 'CapEff:\\t0000000000000000' in Path('/proc/self/status').read_text()
for path in ['/var/run/docker.sock','/run/circuit/tls.key','/run/circuit/upstream-0.token']:
 assert not Path(path).exists()
try:
 Path('/workspace/unsafe').write_text('bypass')
except OSError:pass
else:raise AssertionError('writable workspace')
for host,port in TCP_TARGETS:
 try:
  with socket.create_connection((host,port),timeout=1):pass
 except OSError:pass
 else:raise AssertionError('TCP bypass '+host)
for dns in ['127.0.0.11','1.1.1.1']:
 with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as s:
  s.settimeout(1)
  try:
   s.sendto(bytes.fromhex('123401000001000000000000')+b'\\x07example\\x03com\\x00\\x00\\x01\\x00\\x01',(dns,53))
   s.recvfrom(4096)
  except OSError:pass
  else:raise AssertionError('DNS bypass')
token=Path(os.environ['CIRCUIT_TOKEN_FILE']).read_text().strip()
req=urllib.request.Request(os.environ['CIRCUIT_GATEWAY_URL']+'/v1/actions',
 data=b'{"operation":"reserve","custom_tool":"inventory","args":{"sku":"fixture"}}',
 headers={'Authorization':'Bearer '+token,'Idempotency-Key':'isolated-write','Content-Type':'application/json'})
with urllib.request.urlopen(req,context=ssl.create_default_context(cafile=os.environ['CIRCUIT_CA_CERT']),timeout=20) as r:
 print(r.read().decode())
'''
        (workspace/'probe.py').write_text('TCP_TARGETS='+repr([('1.1.1.1',443),(host_ip,80),(upstream_ip,8444),(peer_ip,8444),(gateway_ip,443),('2606:4700:4700::1111',443)])+'\n'+script)
        workspace.chmod(0o755)
        result = run(*(base+['python','/workspace/probe.py']),capture_output=True)
        action = json.loads(result.stdout)
        check('Agent non-root, zero capabilities, read-only workspace, no provider/operator keys or Docker socket',action['state']=='pending')
        check('Public, host, upstream, reachable internal peer, wrong gateway port, DNS and IPv6 bypass attempts fail',action['state']=='pending')
        context = ssl.create_default_context(cafile=str(operator/'secrets/tls.crt'))
        url = f'https://127.0.0.1:{port}'
        def request(path, credential, body=None):
            req = urllib.request.Request(url+path,data=json.dumps(body).encode() if body is not None else None,
                    headers={'Authorization':'Bearer '+credential,'Content-Type':'application/json'})
            with urllib.request.urlopen(req,context=context,timeout=20) as response:
                return json.load(response)
        # Fixture observation happens in the trusted upstream container, never the agent.
        def calls():
            return json.loads(run('docker','exec',name,'python','-c',
                "import urllib.request,ssl;print(urllib.request.urlopen('https://127.0.0.1:8444',context=ssl._create_unverified_context()).read().decode())",capture_output=True).stdout)
        check('Pending mutation has not reached upstream',calls()==[])
        reviewer = (operator/'secrets/reviewer-token').read_text().strip()
        approved = request('/admin/actions/'+action['id']+'/decision',reviewer,{'digest':action['digest'],'decision':'approve'})
        check('Exact reviewer approval executes once through gateway-owned provider credential',approved['state']=='succeeded' and len(calls())==1)
        agent_token = (operator/'secrets/agent-token').read_text().strip()
        check('Agent can poll terminal status without another write',request('/v1/actions/'+action['id'],agent_token)['state']=='succeeded' and len(calls())==1)
        # SDK fixture can reuse this operator directory while the test is active.
        residual = run('docker','ps','-aq','--filter','label=circuit.project='+d['project'],'--filter','label=circuit.role=boundary',capture_output=True).stdout
        check('Agent exit removes its dedicated firewall namespace',not residual.strip())
        failed = subprocess.run(base[:-1]+['--boundary-image',a.gateway_image,'--','python','/workspace/probe.py'],capture_output=True,text=True,timeout=30)
        check('Firewall initialization failure prevents agent launch',failed.returncode!=0 and 'agent was not launched' in failed.stderr)
    finally:
        subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        subprocess.run(['docker','rm','-f',peer_name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if d:
            subprocess.run([str(binary),'down','--dir',str(operator)],check=True)
            subprocess.run(['docker','volume','rm',d['project']+'_state'],check=True,stdout=subprocess.DEVNULL)
print(json.dumps({'checks':checks,'count':len(checks)}))
