# Database Gateway Action Adapter

Circuit provides a secure, durable, and policy-governed gateway adapter for databases. Like the GitHub and Workspace adapters, the Database adapter allows autonomous AI agents to query and mutate SQL databases while enforcing strict safety boundaries:

- **SQL AST Analysis**: Every statement is parsed using a full PostgreSQL AST parser (`github.com/auxten/postgresql-parser`) before execution. Statements cannot be concealed by obfuscated whitespace, comments, or SQL injection tricks.
- **Read-Only vs Mutation Separation**: Read actions (`query_sql`) only permit non-locking `SELECT` statements. Mutations (`INSERT`, `UPDATE`, `DELETE`, DDL) must go through `exec_sql`.
- **Table Allow/Denylists**: Restricts agents to permitted tables and explicitly blocks access to sensitive tables (e.g. `users`, `secrets`, `admin_tokens`), evaluated statically on all referenced tables across `SELECT`, `JOIN`, subqueries, and CTEs.
- **Destructive Statement Protection**: Destructive DDL (`DROP TABLE`, `TRUNCATE`, `ALTER TABLE`, `DROP DATABASE`) and unconstrained DML (`UPDATE` or `DELETE` without a `WHERE` clause) automatically trigger **mandatory human operator approval**.
- **Affected-Row Bounds & Transaction Rollback**: When `max_affected_rows` is specified on `exec_sql`, the statement is executed within an isolated transaction. If `RowsAffected()` exceeds the allowed threshold, Circuit immediately rolls back the transaction and returns a 400 error.
- **Row Limits**: `max_rows` truncates result sets to prevent memory exhaustion or token explosion when feeding results back into LLM contexts.
- **Durable Budgets & Velocity Limits**: Scoped rate limits per `database` or `agent_database` prevent runaways.
- **Zero-Dependency Mock Mode**: Built-in simulated database engine enables unit testing and rapid prototyping without running an external database daemon.

---

## Configuration

Databases are declared in your Circuit gateway YAML configuration under `databases`:

```yaml
name: production-database-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 1h

databases:
  - id: analytics
    driver: postgres # or mock for local testing
    dsn_env: ANALYTICS_DB_DSN
    read_only: false
    max_rows: 500
    max_timeout: 15s
    allow_tables: ["page_views", "events", "orders", "metrics"]
    deny_tables: ["secrets", "user_credentials", "payment_tokens"]

  - id: reporting-replica
    driver: postgres
    dsn_env: REPLICA_DB_DSN
    read_only: true
    max_rows: 1000
    max_timeout: 30s

agents:
  - id: analytics-agent
    token_env: AGENT_TOKEN
    databases: ["analytics", "reporting-replica"]
    actions:
      - query_sql
      - exec_sql
      - list_tables
      - describe_table

limits:
  - id: hourly-query-limit
    actions: ["query_sql"]
    scope: database
    window: 1h
    max_calls: 1000

  - id: mutation-velocity-limit
    actions: ["exec_sql"]
    scope: agent_database
    window: 10m
    max_calls: 20
```

---

## Available Actions & MCP Tools

Circuit automatically exposes the following actions over REST (`POST /v1/actions`) and the Model Context Protocol (MCP `/mcp`):

| Action | MCP Tool Name | Description | Required Arguments | Optional Arguments |
|---|---|---|---|---|
| `query_sql` | `db_query` | Run a read-only `SELECT` query against the database target. | `query` | `database`, `max_rows`, `timeout_sec` |
| `exec_sql` | `db_exec` | Execute a SQL mutation (`INSERT`, `UPDATE`, `DELETE`, DDL). Destructive statements require human operator approval. | `query` | `database`, `timeout_sec`, `max_affected_rows` |
| `list_tables` | `db_list_tables` | List accessible tables in the database target (filtered by allow/denylists). | _none_ | `database`, `schema` |
| `describe_table` | `db_describe_table` | Inspect table columns, types, and nullability. | `table` | `database`, `schema` |

---

## Safety Guarantees

### 1. AST-Based Static Safety Analysis
Circuit parses every incoming SQL query into an Abstract Syntax Tree using `github.com/auxten/postgresql-parser/pkg/sql/parser`:
- **Statement Type Classification**: Classifies statements into `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `DROP TABLE`, `TRUNCATE`, `ALTER TABLE`, `CREATE TABLE`, etc.
- **Lock Detection**: `SELECT ... FOR UPDATE` or `FOR SHARE` locks rows and is therefore classified as a mutation, rejecting it on `query_sql`.
- **Table Extraction**: Walks the AST to extract all referenced table names, ensuring tables hidden within joins, subqueries, or sub-selects are subject to `allow_tables` and `deny_tables` checks.
- **Multiple Statement Rejection**: Queries containing multiple statements (e.g. `SELECT 1; DROP TABLE users;`) are strictly forbidden and rejected before execution.

### 2. Mandatory Approval for Destructive Statements
When `exec_sql` is invoked with a query that has potential catastrophic consequences:
- `DROP TABLE`
- `DROP DATABASE`
- `TRUNCATE`
- `ALTER TABLE`
- `UPDATE` without a `WHERE` clause
- `DELETE` without a `WHERE` clause

Circuit sets the action state to `pending` and halts execution until an authorized human operator inspects the statement in Circuit's web UI (`http://127.0.0.1:8080/`) or via the Admin API (`POST /admin/actions/{id}/decision`).

### 3. Affected-Row Bounding & Automatic Rollback
For large-scale updates or deletions, agents or policies can set `max_affected_rows`:
```json
{
  "operation": "exec_sql",
  "database": "analytics",
  "args": {
    "query": "DELETE FROM events WHERE created_at < '2024-01-01';",
    "max_affected_rows": 1000
  }
}
```
If the query matches 1,001 rows, Circuit rolls back the active transaction and returns:
```json
{
  "status": 400,
  "error": "statement affected 1001 rows, exceeding allowed limit of 1000 (transaction rolled back)"
}
```

---

## Verification & Serving

Validate your database gateway configuration:
```bash
circuit gateway check gateway.yaml
```

Serve the gateway with database adapters:
```bash
CIRCUIT_ADMIN_TOKEN="..." AGENT_TOKEN="..." ANALYTICS_DB_DSN="..." circuit gateway serve --config gateway.yaml
```
