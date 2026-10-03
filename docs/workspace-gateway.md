# Circuit Workspace & Shell Action Gateway

Circuit provides bounded execution for autonomous agents performing local shell commands and file operations within sandboxed workspace boundaries.

Like the GitHub adapter, the gateway holds execution authority. Each agent receives explicit workspace allowlists, operation permissions, durable call quotas, and mandatory operator approval for destructive operations.

## Capabilities

- **Workspace Path Sandboxing:** All operations are constrained to an explicit local directory root. Path traversal (`../`), null-byte injection, absolute path escapes, and symlinks pointing outside the workspace root are rejected before execution.
- **Destructive Command Review:** Commands are parsed and analyzed using standard Bash AST inspection (`mvdan.cc/sh/v3`). Potentially destructive utilities (`rm`, `chmod`, `chown`, `kill`, `sudo`, `dd`, `mkfs`) and consequential git operations (`git reset`, `git clean`, `git push`, `git rebase`) automatically pause for operator approval of the exact command digest.
- **Controlled File Mutations:** 
  - `write_file` performs atomic writes with parent directory creation and 2 MiB bounds. Overwriting an existing file requires human operator approval.
  - `delete_file` always requires operator review before deleting files or directories. Workspace roots cannot be deleted.
  - Workspaces can be marked `read_only: true` to prevent any writes or deletions entirely.
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
| `exec_cmd` | `command` | `cwd` (relative), `timeout_sec` | Destructive commands require operator approval |
| `read_file` | `path` (relative) | — | Allowed within workspace |
| `write_file` | `path` (relative), `content` | `encoding` (`base64` or plain), `overwrite` (bool) | Overwrites require operator approval |
| `delete_file` | `path` (relative) | `recursive` (bool) | Mandatory operator approval |
| `list_dir` | — | `path` (relative, default root) | Allowed within workspace |

## MCP Tools

When connected via Streamable HTTP MCP (`/mcp`) using an agent bearer token:

- `shell_exec_cmd`: Execute a bounded command in the workspace.
- `file_read`: Read a file from the workspace.
- `file_write`: Write a file in the workspace (prompts for operator review if overwriting).
- `file_delete`: Delete a file or directory (prompts for operator review).
- `file_list_dir`: List files and subdirectories.
- `circuit_action_status`: Poll action status by ID while waiting for operator approval.
