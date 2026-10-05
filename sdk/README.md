# Circuit Agent Clients

Generated REST bindings for TypeScript, Python and Go share [one OpenAPI contract](../api/openapi.yaml). The safety-aware facades submit once, poll exact approvals, and stop on uncertain/denied/failed outcomes. They hold only an agent credential, not provider credentials.

See the [integration quickstart and language examples](../docs/isolated-agents.md) and [packed artifact installation](../docs/local-release-testing.md). These packages are installed from source or repository archives, not npm/PyPI. Runtime qualification covers these three clients against a real TLS gateway and REST fixture, including clean archive installations, not arbitrary providers.

Regenerate maintained bindings with `sh scripts/generate-sdks.sh`. Generate another language with `sh scripts/generate-client.sh GENERATOR NEW_OUTPUT_DIRECTORY`; configure its TLS/authentication/timeout/state behavior before use. Never edit generated wire models or APIs directly. Handwritten facades and adapters are `typescript/src/circuit.ts`, `typescript/node.cjs`, `python/circuit_client/safe.py`, and `go/circuit.go`.

Clients are protocol compatibility, not isolation. The recommended `circuit up` plus `agent run --dir` deployment supplies the mandatory network boundary. An unrestricted host app can still bypass Circuit.
