#!/usr/bin/env python3
"""Install packed SDKs in disposable consumer directories and exercise the release CLI."""
import argparse
from pathlib import Path
import subprocess
import tarfile
import tempfile

root = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument('--artifacts', required=True, type=Path)
p.add_argument('--binary', required=True, type=Path)
p.add_argument('--expected-version', help='Require the exact CLI version in downloaded artifacts')
a = p.parse_args()
artifacts, binary = a.artifacts.resolve(), a.binary.resolve()
def run(*args, **kwargs):
    return subprocess.run(list(map(str,args)), check=True, **kwargs)

with tempfile.TemporaryDirectory(prefix='circuit-release-') as temp:
    work = Path(temp)
    result = run(binary,'version',capture_output=True,text=True)
    version = (result.stdout+result.stderr).strip()
    if a.expected_version:
        assert version == 'circuit v'+a.expected_version,version
    print(version,flush=True)
    for command in [('setup',),('up',),('agent','run'),('gateway','backup'),('gateway','restore')]:
        run(binary,*command,'--help',stdout=subprocess.DEVNULL)
    run('python3','-m','venv',work/'venv')
    wheel = next(artifacts.glob('circuit_agent_client-*.whl'))
    run(work/'venv/bin/pip','install',wheel)
    # Import without the source tree on sys.path.
    run(work/'venv/bin/python','-c','from circuit_client.safe import Circuit, ActionStopped',cwd=work)
    node = work/'node'
    node.mkdir()
    run('npm','init','-y',cwd=node,stdout=subprocess.DEVNULL)
    run('npm','install',next(artifacts.glob('circuit-agent-client-*.tgz')),cwd=node)
    go = work/'go'
    go.mkdir()
    with tarfile.open(artifacts/'circuit-go-sdk-0.2.0.tar.gz') as archive:
        archive.extractall(go,filter='data')
    run('go','test','./...',cwd=go)
    run('python3',root/'examples/sdk-validation.py','--binary',binary,
        '--python',work/'venv/bin/python','--node-package',node/'node_modules/@circuit/agent-client',
        '--go-package',go)
    print('PASS Release binary plus clean wheel/npm archive/Go archive installations',flush=True)
