# Circuit Workspace & Shell Action Gateway

Circuit provides root-scoped file operations and local shell execution with approval by default. File operations use Go's `os.Root`; shell execution is not an OS sandbox and can access the host outside the workspace. Use an independently configured OS sandbox before relying on shell isolation.

Like the GitHub adapter, the gateway holds execution authority. Each agent receives explicit workspace allowlists, operation permissions, durable call quotas, and operator approval defaults for destructive operations. In v0.3.0, [explicit gateway rules](gateway-policy.md) can authorize these operations automatically; scope, read-only restrictions, and external sandbox requirements still apply.

## Capabilities

- **File Root Boundaries:** File operations are constrained to an explicit local directory root, including protection against symlink replacement races. Hard links, bind mounts, and independently privileged processes remain deployment concerns.
- **Shell Review:** Every `exec_cmd` defaults to approval of the exact command, including interpreters and redirects; an explicit matching ALLOW rule can authorize it automatically. AST inspection adds review context but is not a security boundary. The shell receives a minimal PATH/HOME/TMPDIR environment, not gateway provider or operator credentials.
- **Controlled File Mutations:** 
  - `write_file` performs atomic writes with parent directory creation and 2 MiB bounds. Overwriting an existing file defaults to human operator approval; an explicit matching ALLOW rule can authorize it automatically.
  - `delete_file` always requires operator review before deleting files or directories. Workspace roots cannot be deleted.
  - Workspaces marked `read_only: true` reject writes, deletion, and shell execution.
- **Bounded Resource Limits:**
  - Configurable execution timeouts per workspace (default 60s) with clean process group termination on timeout.
  - Output buffers capped at 512 KiB stdout/stderr to prevent memory exhaustion.
  - Action velocity limits persist across restarts and reserve quota during pending reviews.
- **Dual Interface:** Accessible via structured REST API (`POST /v1/actions`) and official Streamable HTTP MCP (`shell_exec_cmd`, `file_read`, `file_write`, `file_delete`, `file_list_dir`).

## Configuration

In `gateway.yaml`:

```yaml
name: engineering-agent-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN

workspaces:
  - id: project-root
    path: /path/to/project
    max_timeout: 30s
    read_only: false

agents:
  - id: coding-agent
    token_env: CIRCUIT_AGENT_TOKEN
    workspaces: [project-root]
    actions:
      - exec_cmd
      - read_file
      - write_file
      - delete_file
      - list_dir

rules:
  - id: review-docker-commands
    actions: [exec_cmd]
    condition: "args.command.contains('docker')"
    action: REQUIRE_APPROVAL
    reason: Docker operations require human sign-off

limits:
  - id: command-velocity
    actions: [exec_cmd]
    scope: agent_workspace
    window: 1h
    max_calls: 50
```

## Supported Operations

| Operation | Required Arguments | Optional Arguments | Default Safety |
| --- | --- | --- | --- |
| `exec_cmd` | `command` | `cwd` (relative), `timeout_sec` | Approval by default; explicit ALLOW supported; external sandbox required |
| `read_file` | `path` (relative) | — | Allowed within workspace |
| `write_file` | `path` (relative), `content` | `encoding` (`base64` or plain), `overwrite` (bool) | Overwrites default to approval; explicit ALLOW supported |
| `delete_file` | `path` (relative) | `recursive` (bool) | Approval by default; explicit ALLOW supported |
| `list_dir` | — | `path` (relative, default root) | Allowed within workspace |

## MCP Tools

When connected via Streamable HTTP MCP (`/mcp`) using an agent bearer token:

- `shell_exec_cmd`: Execute a bounded command in the workspace.
- `file_read`: Read a file from the workspace.
- `file_write`: Write a file in the workspace (prompts for operator review if overwriting).
- `file_delete`: Delete a file or directory (prompts for operator review).
- `file_list_dir`: List files and subdirectories.
- `circuit_action_status`: Poll action status by ID while waiting for operator approval.

Existing files are never replaced implicitly: `write_file` without `overwrite: true` fails with a conflict. Explicit replacement requires review. See [validation status](validation-status.md) for local race and operator workflow tests.
