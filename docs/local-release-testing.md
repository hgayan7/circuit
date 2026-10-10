# Local Candidate Qualification

No publication is needed for these checks. `npm pack` builds a local archive; it does not publish. Dependency installation may download packages. All local output belongs under ignored `.circuit/` directories. The owner permits commits, tags, and validated release artifacts only in `hgayan7/circuit`; external registries and other repositories are excluded.

## Build And Install Local Artifacts

From the source checkout, choose a new output directory:

```sh
go build -ldflags '-X main.version=0.3.0-local' -o .circuit/circuit-030-local ./cmd/circuit
sh scripts/package-sdks.sh .circuit/local-sdk-artifacts
python3 examples/release-validation.py --artifacts .circuit/local-sdk-artifacts \
  --binary .circuit/circuit-030-local
```

This builds a Python wheel/source archive, a Node package archive, a Go SDK archive, and the OpenAPI contract. The validation installs them in clean temporary consumer directories and exercises read, exact approval, denial, and uncertain no-replay behavior against the actual TLS gateway. It does not import Python from the source tree or use the unpackaged Node module.

To install for your own local app:

```sh
python -m pip install .circuit/local-sdk-artifacts/circuit_agent_client-0.2.0-py3-none-any.whl
npm install /absolute/path/to/.circuit/local-sdk-artifacts/circuit-agent-client-0.2.0.tgz
# Extract circuit-go-sdk-0.2.0.tar.gz and use a local Go module replace.
```

These filenames identify local candidate packages, not published stable packages. The commands use npm only as a local Node build/install tool.

## Isolated Integration And Upgrade

Build the gateway/boundary images from the same checkout using the [quickstart](isolated-agents.md), then test the candidate CLI:

```sh
python3 examples/isolated-validation.py --binary .circuit/circuit-030-local
python3 examples/compatibility-validation.py --previous /path/to/verified/v0.2.0/circuit \
  --candidate .circuit/circuit-030-local
go test -race ./...
go vet ./...
```

The isolation fixture follows `setup -> up -> agent run` and checks real bypass attempts, approval execution, credentials, and cleanup. SDK archive versions remain 0.2.0 because the wire contract is unchanged. The compatibility fixture starts exactly one binary at a time on the same disposable state; it does not touch a running deployment. Its unchanged legacy HTTP fixture is a storage/client compatibility check, not production qualification of legacy HTTP.

## Explicit Exceptions

- On 2026-10-05, the owner requested skipping the uninterrupted 72-hour soak. It is **waived for this local qualification, not completed or passed**. Short checks do not establish multi-day reliability.
- npm/PyPI, container-registry, Homebrew, and other external publication are explicitly excluded. SDKs can be installed from local archives or validated release assets in this repository.
- Independent security review remains deferred. Automated checks are not independent certification.
- A temporary clean consumer environment on this machine is not a separate clean machine. CI repeats clean installs on an ephemeral Linux runner; release publication additionally downloads and tests exact draft artifacts before making the repository release public.
- No real email is sent by these fixtures. Any live test email may be sent only to `hgayan7@gmail.com`; unclear destinations require owner confirmation.
- Actual BYOK providers and production infrastructure still require operator acceptance. See [compatibility](compatibility.md) and the [release checklist](release-checklist.md).
