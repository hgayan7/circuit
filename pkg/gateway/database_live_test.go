package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// This test requires a disposable local database owned by the fixture user.
func TestPostgresLive(t *testing.T) {
	dsn := os.Getenv("CIRCUIT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CIRCUIT_TEST_POSTGRES_DSN to a disposable PostgreSQL database")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.Ping())
	_, err = db.Exec(`DROP TABLE IF EXISTS circuit_pilot_items, circuit_pilot_secrets;
 CREATE TABLE circuit_pilot_items(id integer PRIMARY KEY, value integer NOT NULL);
 INSERT INTO circuit_pilot_items VALUES (1,10),(2,20),(3,30);
 CREATE TABLE circuit_pilot_secrets(value text);
 INSERT INTO circuit_pilot_secrets VALUES ('fixture-only');`)
	require.NoError(t, err)
	defer db.Exec("DROP TABLE circuit_pilot_items, circuit_pilot_secrets")
	target, err := NewDatabaseTarget("local", "postgres", dsn, false, 2, time.Second, []string{"circuit_pilot_items"}, []string{"circuit_pilot_secrets"})
	require.NoError(t, err)
	defer target.DB.Close()
	ex := NewDatabaseExecutor(target)
	run := func(op, q string, extra map[string]any) Outcome {
		args := map[string]any{"query": q}
		for k, v := range extra {
			args[k] = v
		}
		return ex.Execute(context.Background(), Request{Operation: op, Database: "local", Args: args})
	}
	t.Run("real query and bounded result", func(t *testing.T) {
		out := run("query_sql", "SELECT * FROM circuit_pilot_items ORDER BY id", nil)
		require.Equal(t, 200, out.Status, out.Error)
		var body map[string]any
		require.NoError(t, json.Unmarshal(out.Body, &body))
		require.Equal(t, float64(2), body["row_count"])
		require.Equal(t, true, body["truncated"])
	})
	t.Run("row bound rolls back", func(t *testing.T) {
		out := run("exec_sql", "UPDATE circuit_pilot_items SET value=999 WHERE id > 0", map[string]any{"max_affected_rows": 1})
		require.Equal(t, 400, out.Status)
		require.Contains(t, out.Error, "rolled back")
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM circuit_pilot_items WHERE value=999").Scan(&count))
		require.Zero(t, count)
	})
	t.Run("bounded mutation commits", func(t *testing.T) {
		out := run("exec_sql", "UPDATE circuit_pilot_items SET value=11 WHERE id=1", map[string]any{"max_affected_rows": 1})
		require.Equal(t, 200, out.Status, out.Error)
		var value int
		require.NoError(t, db.QueryRow("SELECT value FROM circuit_pilot_items WHERE id=1").Scan(&value))
		require.Equal(t, 11, value)
	})
	t.Run("table and nested mutation restrictions", func(t *testing.T) {
		require.Equal(t, 403, run("query_sql", "SELECT * FROM circuit_pilot_secrets", nil).Status)
		require.NotEqual(t, 200, run("query_sql", "WITH x AS (DELETE FROM circuit_pilot_items RETURNING *) SELECT * FROM x", nil).Status)
		require.NotEqual(t, 200, run("query_sql", "SELECT nextval('unknown_sequence')", nil).Status)
	})
	t.Run("timeout and permission failure", func(t *testing.T) {
		started := time.Now()
		out := run("exec_sql", "SELECT pg_sleep(5)", nil)
		require.NotEqual(t, 200, out.Status)
		require.Less(t, time.Since(started), 3*time.Second)
		_, err := db.Exec("CREATE ROLE circuit_pilot_reader; GRANT SELECT ON circuit_pilot_items TO circuit_pilot_reader")
		require.NoError(t, err)
		defer db.Exec("DROP OWNED BY circuit_pilot_reader; DROP ROLE circuit_pilot_reader")
		tx, err := db.Begin()
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = tx.Exec("SET LOCAL ROLE circuit_pilot_reader")
		require.NoError(t, err)
		_, err = tx.Exec("UPDATE circuit_pilot_items SET value=12 WHERE id=1")
		require.Error(t, err)
	})
	t.Run("read-only target", func(t *testing.T) {
		target.ReadOnly = true
		require.Equal(t, 403, run("exec_sql", "DELETE FROM circuit_pilot_items WHERE id=1", nil).Status)
		target.ReadOnly = false
	})
	t.Run("schema metadata", func(t *testing.T) {
		out := ex.Execute(context.Background(), Request{Operation: "list_tables"})
		require.Equal(t, 200, out.Status, out.Error)
		require.Contains(t, string(out.Body), "circuit_pilot_items")
		require.NotContains(t, string(out.Body), "circuit_pilot_secrets")
		out = ex.Execute(context.Background(), Request{Operation: "describe_table", Args: map[string]any{"table": "circuit_pilot_items"}})
		require.Equal(t, 200, out.Status, out.Error)
		require.Contains(t, string(out.Body), "integer")
	})
	t.Run("official MCP over HTTP with real PostgreSQL", func(t *testing.T) {
		cfg, err := ParseConfig(strings.NewReader(`
name: live-mcp
databases:
  - id: local
    dsn_env: CIRCUIT_TEST_POSTGRES_DSN
    allow_tables: [circuit_pilot_items]
agents:
  - id: reader
    token_env: READER_TOKEN
    databases: [local]
    actions: [query_sql]
`))
		require.NoError(t, err)
		store, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
		require.NoError(t, err)
		defer store.Close()
		svc, err := NewService(cfg, store, NewRouterExecutor(nil, nil, map[string]*DatabaseExecutor{"local": ex}, nil, nil, nil, nil))
		require.NoError(t, err)
		handler, err := NewHTTPHandler(svc, Tokens{Admin: adminToken, Agents: map[string]string{"reader": agentToken}})
		require.NoError(t, err)
		server := httptest.NewServer(handler)
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client := mcp.NewClient(&mcp.Implementation{Name: "live-postgres", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: authTransport{agentToken}}}, nil)
		require.NoError(t, err)
		defer session.Close()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "db_query", Arguments: map[string]any{"database": "local", "idempotency_key": "live-db", "args": map[string]any{"query": "SELECT value FROM circuit_pilot_items WHERE id=1"}}})
		require.NoError(t, err)
		require.False(t, result.IsError)
		var action Action
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &action))
		require.Equal(t, "succeeded", action.State)
		require.Contains(t, string(action.Outcome.Body), "11")
	})
}
