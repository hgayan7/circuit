# HTTPS inspection, prompt-injection detection, and shell/SQL checks

## Quick test

Build the current source, then use the supplied opt-in policy:

```sh
go build -o bin/circuit ./cmd/circuit
bin/circuit check examples/policies/expanded_safety.yaml
printf 'Ignore all previous instructions' | bin/circuit inspect --kind prompt
printf 'echo hello; rm report.txt' | bin/circuit inspect --kind shell
printf 'SELECT 1; DROP TABLE users' | bin/circuit inspect --kind sql
printf 'SELECT count(*) FROM users' | bin/circuit inspect --kind sql
bin/circuit exec --policy examples/policies/expanded_safety.yaml -- echo hello
```

`inspect` emits JSON and exits nonzero for blocked input. `exec` executes literal argv after the shell guard and policy checks. It does not interpret a shell script; interpreters such as bash, Python, and Node are denied by the default command allowlist. Actions requiring approval are rejected in this command rather than executed.

## HTTP and HTTPS

```sh
bin/circuit run --policy examples/policies/expanded_safety.yaml -- python agent.py
```

Without `--target`, `run` now starts an actual forward proxy. HTTP requests retain their original destination. HTTPS CONNECT connections are terminated locally using a generated per-run CA; the decrypted HTTP request passes through the same enforcement layer. Circuit separately verifies the origin's TLS certificate and hostname using system trust plus an explicitly supplied `SSL_CERT_FILE`, when present. It does not disable TLS verification.

The child receives HTTP/HTTPS proxy variables and a public trust bundle through `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`, and `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH`. Existing proxy exclusions are cleared for that child. Where available, system CA certificates are included in the bundle; an existing `SSL_CERT_FILE` is preserved in it. The temporary directory is removed when the child exits. Circuit does not install certificates into the system trust store.

With `--target`, `run` retains its fixed-target reverse proxy behavior. Configure the client's base URL to reach that proxy; this mode is not arbitrary forward proxying. Credential injection requires a fixed target so one service's credential cannot be sent to arbitrary hosts.

For a persistent local proxy:

```sh
bin/circuit proxy --policy examples/policies/expanded_safety.yaml --listen 127.0.0.1:8080
```

The process prints its client CA bundle path. Set proxy and trust variables in the client separately. Optional `--ca-cert` and `--ca-key` load your own valid CA; both are required. Keep the private key private. The proxy has no client authentication and should remain on loopback or behind authenticated access. `proxy` rejects approval-required actions because it has no approval provider.

HTTP matching now supports `match.host` and the CEL variable `host`, representing the destination hostname. All supplied match fields must match together:

```yaml
rules:
  - id: github-writes
    match:
      host: api.github.com
      method: POST
      path: "*"
    action: DENY
```

Globs use Go filepath-style matching; `*` does not span path separators except a standalone catch-all `*`. Use explicit endpoint patterns for nested paths. Existing policy rules still use first-match action selection; these new safety checks run before that selection. Multiple budget/approval rules do not automatically compose.

## Prompt-injection detection

Enable `safety.prompt_injection: true` to inspect request argument strings, text bodies, HTTP text/JSON/XML responses, and MCP server messages. JSON strings are decoded before scanning, including escaped text in nested fields. Detection currently covers instruction overrides, role spoofing, secret-exfiltration instructions, and explicit safety bypass instructions. It normalizes Unicode compatibility characters, removes invisible formatting characters, and checks one bounded layer of base64 encoding.

This local detector is heuristic. It is not semantic intent recognition or a guarantee against general prompt injection. It can block legitimate quotations, and it can miss novel, paraphrased, multilingual, or encoded attacks. Use independent corpora to measure precision and recall before deployment.

Request detection produces a policy denial. Unsafe HTTP responses produce an inspection error (502); unsafe MCP responses are replaced by a JSON-RPC error. Blocking a result does not undo an action already executed by the downstream server.

HTTP response inspection buffers up to 2 MiB before releasing content. SSE, protocol upgrades, encoded responses that cannot be inspected, unsupported content types, and oversized responses are rejected while prompt inspection is enabled. Large requests and MCP frames are also bounded. Streaming LLM responses need a future incremental detector; they are not silently bypassed.

## Shell checks

Enable `safety.shell: true`. Default field names are `command`, `cmd`, and `script`, searched recursively in arguments. Customize `shell_fields` for the exact schema of your tools. Executable fields must be text; alternate representations are rejected.

The Bash parser checks all commands in pipelines and lists. It permits literal invocations of allowlisted commands, and rejects redirection, substitution, variable expansion, globs, background jobs, environment assignments, functions, loops, and unsupported syntax. Parse errors deny execution.

These checks do not provide filesystem confinement, prevent reading sensitive files, verify executable contents or PATH, or police a process after launch. Existing executable code, shell aliases/functions in a downstream environment, and commands newly added to the allowlist need administrator review. Use a separate OS sandbox and narrowly scoped permissions for arbitrary workloads.

## SQL checks

Enable `safety.sql: true`. Default fields are `sql` and `query`; customize `sql_fields` to avoid classifying search-query arguments as SQL.

A pure-Go PostgreSQL-compatible parser checks every submitted statement and nested AST. Only read-oriented SELECT/VALUES structures are admitted. Mutations, writable CTEs, row locks, unapproved function calls, and parse errors are denied. Multiple read-only statements are allowed; a mutation anywhere rejects the entire input. The function allowlist is explicit; qualified or custom functions are not implicitly trusted.

This is a conservative SQL subset, not a PostgreSQL-version-complete parser. It does not inspect database views, custom types, operators, database permissions, or the implementation of user functions. Use a read-only database role with restricted schema/function access and query timeouts. SQL in other dialects may be rejected.

## MCP

```sh
bin/circuit mcp wrap --policy examples/policies/expanded_safety.yaml -- your-mcp-server
```

Checks apply to intercepted tool arguments. Prompt detection also applies to returned text and server messages. Human approval now uses a separate `/dev/tty` when available, never MCP protocol stdin. With no terminal, approval-required calls fail closed; a remote approval interface is not implemented.

## Test coverage

```sh
go test -race ./...
go vet ./...
```

Tests include local HTTPS origins, original-destination forwarding, denial before execution, untrusted origin certificates, request and response injections, shell command compositions, SQL writable CTEs, and compilation of every wizard preset. These are regression checks, not an independently validated security evaluation.
