package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLAnalyzer(t *testing.T) {
	tests := []struct {
		name          string
		query         string
		statementType string
		isReadOnly    bool
		isDestructive bool
		tables        []string
		wantErr       bool
	}{
		{
			name:          "simple select",
			query:         "SELECT id, name FROM users WHERE id = 1;",
			statementType: "SELECT",
			isReadOnly:    true,
			isDestructive: false,
			tables:        []string{"users"},
		},
		{
			name:          "select with join and alias",
			query:         "SELECT u.id, o.amount FROM users u JOIN orders o ON u.id = o.user_id WHERE o.status = 'active';",
			statementType: "SELECT",
			isReadOnly:    true,
			isDestructive: false,
			tables:        []string{"users", "orders"},
		},
		{
			name:          "select with for update lock",
			query:         "SELECT * FROM accounts WHERE id = 42 FOR UPDATE;",
			statementType: "SELECT",
			isReadOnly:    false,
			isDestructive: false,
			tables:        []string{"accounts"},
		},
		{
			name:          "insert into table",
			query:         "INSERT INTO audit_log (action, user_id) VALUES ('login', 123);",
			statementType: "INSERT",
			isReadOnly:    false,
			isDestructive: false,
			tables:        []string{"audit_log"},
		},
		{
			name:          "safe update with where",
			query:         "UPDATE users SET email = 'alice@new.org' WHERE id = 1;",
			statementType: "UPDATE",
			isReadOnly:    false,
			isDestructive: false,
			tables:        []string{"users"},
		},
		{
			name:          "destructive update without where",
			query:         "UPDATE users SET role = 'guest';",
			statementType: "UPDATE",
			isReadOnly:    false,
			isDestructive: true,
			tables:        []string{"users"},
		},
		{
			name:          "safe delete with where",
			query:         "DELETE FROM sessions WHERE expired_at < NOW();",
			statementType: "DELETE",
			isReadOnly:    false,
			isDestructive: false,
			tables:        []string{"sessions"},
		},
		{
			name:          "destructive delete without where",
			query:         "DELETE FROM sessions;",
			statementType: "DELETE",
			isReadOnly:    false,
			isDestructive: true,
			tables:        []string{"sessions"},
		},
		{
			name:          "drop table",
			query:         "DROP TABLE sensitive_data;",
			statementType: "DROP TABLE",
			isReadOnly:    false,
			isDestructive: true,
			tables:        []string{"sensitive_data"},
		},
		{
			name:          "truncate table",
			query:         "TRUNCATE TABLE logs;",
			statementType: "TRUNCATE",
			isReadOnly:    false,
			isDestructive: true,
			tables:        []string{"logs"},
		},
		{
			name:          "alter table",
			query:         "ALTER TABLE users DROP COLUMN ssn;",
			statementType: "ALTER TABLE",
			isReadOnly:    false,
			isDestructive: true,
			tables:        []string{"users"},
		},
		{
			name:    "multi statement query rejected",
			query:   "SELECT 1; DROP TABLE users;",
			wantErr: true,
		},
		{
			name:    "empty query rejected",
			query:   "   ",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			analysis, err := AnalyzeSQL(tc.query)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.statementType, analysis.StatementType)
			assert.Equal(t, tc.isReadOnly, analysis.IsReadOnly)
			assert.Equal(t, tc.isDestructive, analysis.IsDestructive)
			for _, expectedTable := range tc.tables {
				assert.Contains(t, analysis.Tables, expectedTable)
			}
		})
	}
}

func TestCheckTableAccess(t *testing.T) {
	allowlist := []string{"users", "orders", "products"}
	denylist := []string{"secrets", "admin_tokens"}

	// Allowed tables
	err := CheckTableAccess([]string{"users", "orders"}, allowlist, denylist)
	assert.NoError(t, err)

	// Denied table
	err = CheckTableAccess([]string{"users", "secrets"}, allowlist, denylist)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sensitive table \"secrets\" is forbidden")

	// Table not in allowlist
	err = CheckTableAccess([]string{"users", "billing"}, allowlist, denylist)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table \"billing\" is not in the allowed tables list")

	// Case-insensitivity check
	err = CheckTableAccess([]string{"USERS", "ORDERS"}, allowlist, denylist)
	assert.NoError(t, err)

	err = CheckTableAccess([]string{"ADMIN_TOKENS"}, allowlist, denylist)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sensitive table \"admin_tokens\" is forbidden")
}

func TestDatabaseExecutorSimulated(t *testing.T) {
	ctx := context.Background()
	target, err := NewDatabaseTarget("prod-db", "mock", "", false, 10, 5*time.Second, []string{"users", "orders"}, []string{"secrets"})
	require.NoError(t, err)
	exec := NewDatabaseExecutor(target)

	// 1. Safe query_sql
	res := exec.Execute(ctx, Request{
		Operation: "query_sql",
		Database:  "prod-db",
		Args: map[string]any{
			"query": "SELECT * FROM users;",
		},
	})
	assert.Equal(t, 200, res.Status)
	assert.Empty(t, res.Error)
	var queryData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &queryData))
	assert.Equal(t, float64(2), queryData["row_count"])

	// 2. Reject mutation via query_sql
	resMut := exec.Execute(ctx, Request{
		Operation: "query_sql",
		Database:  "prod-db",
		Args: map[string]any{
			"query": "INSERT INTO users (id, username) VALUES (3, 'charlie');",
		},
	})
	assert.Equal(t, 400, resMut.Status)
	assert.Contains(t, resMut.Error, "query_sql only permits read-only SELECT")

	// 3. Reject access to unauthorized table via query_sql
	resDenied := exec.Execute(ctx, Request{
		Operation: "query_sql",
		Database:  "prod-db",
		Args: map[string]any{
			"query": "SELECT * FROM secrets;",
		},
	})
	assert.Equal(t, 403, resDenied.Status)
	assert.Contains(t, resDenied.Error, "forbidden by denylist")

	// 4. Exec SQL mutation
	resExec := exec.Execute(ctx, Request{
		Operation: "exec_sql",
		Database:  "prod-db",
		Args: map[string]any{
			"query": "INSERT INTO users (id, username) VALUES (3, 'charlie');",
		},
	})
	assert.Equal(t, 200, resExec.Status)
	assert.Empty(t, resExec.Error)

	// 5. List tables
	resList := exec.Execute(ctx, Request{
		Operation: "list_tables",
		Database:  "prod-db",
		Args:      map[string]any{},
	})
	assert.Equal(t, 200, resList.Status)
	var listData map[string]any
	require.NoError(t, json.Unmarshal(resList.Body, &listData))
	tables, ok := listData["tables"].([]any)
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(tables), 2)

	// 6. Describe table
	resDesc := exec.Execute(ctx, Request{
		Operation: "describe_table",
		Database:  "prod-db",
		Args: map[string]any{
			"table": "users",
		},
	})
	assert.Equal(t, 200, resDesc.Status)
	var descData map[string]any
	require.NoError(t, json.Unmarshal(resDesc.Body, &descData))
	assert.Equal(t, "users", descData["table"])
}

func TestDatabaseGatewayApprovalAndLimits(t *testing.T) {
	ctx := context.Background()
	cfgYAML := `
name: database-gateway
admin_token_env: ADMIN_TOKEN
approval_ttl: 10m
databases:
  - id: analytics
    driver: mock
    allow_tables: ["users", "orders", "events"]
    deny_tables: ["secrets"]
    max_rows: 50
    max_timeout: 5s
agents:
  - id: analyst
    token_env: ANALYST_TOKEN
    databases: ["analytics"]
    actions: ["query_sql", "exec_sql", "list_tables", "describe_table"]
limits:
  - id: query-budget
    actions: ["query_sql"]
    scope: database
    window: 1m
    max_calls: 3
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	target, err := NewDatabaseTarget("analytics", "mock", "", false, 50, 5*time.Second, []string{"users", "orders", "events"}, []string{"secrets"})
	require.NoError(t, err)
	dbExec := NewDatabaseExecutor(target)
	router := NewRouterExecutor(nil, nil, map[string]*DatabaseExecutor{"analytics": dbExec}, nil, nil, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	// 1. Normal read-only query is allowed without approval
	act1, err := svc.Submit(ctx, "analyst", "key-read-1", Request{
		Operation: "query_sql",
		Database:  "analytics",
		Args: map[string]any{
			"query": "SELECT * FROM users WHERE id = 1;",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act1.State)

	// 2. Destructive SQL (e.g. TRUNCATE) requires operator approval
	act2, err := svc.Submit(ctx, "analyst", "key-truncate-1", Request{
		Operation: "exec_sql",
		Database:  "analytics",
		Args: map[string]any{
			"query": "TRUNCATE TABLE events;",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act2.State)
	assert.Contains(t, act2.Reason, "TRUNCATE is destructive")

	// Decide: Operator approves
	decidedAct, err := svc.Decide(ctx, act2.ID, act2.Digest, "approve")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", decidedAct.State)
	assert.Equal(t, "operator", decidedAct.ApprovedBy)

	// 3. Destructive UPDATE without WHERE clause requires operator approval
	act3, err := svc.Submit(ctx, "analyst", "key-update-all", Request{
		Operation: "exec_sql",
		Database:  "analytics",
		Args: map[string]any{
			"query": "UPDATE users SET role = 'anon';",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act3.State)
	assert.Contains(t, act3.Reason, "UPDATE without WHERE clause")

	// Decide: Operator rejects
	rejectedAct, err := svc.Decide(ctx, act3.ID, act3.Digest, "reject")
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejectedAct.State)

	// 4. Denied table access is rejected by policy/scope
	act4, err := svc.Submit(ctx, "analyst", "key-secrets-query", Request{
		Operation: "query_sql",
		Database:  "analytics",
		Args: map[string]any{
			"query": "SELECT * FROM secrets;",
		},
	})
	require.NoError(t, err)
	// Execution failed because table was denied
	assert.Equal(t, "failed", act4.State)
	assert.Contains(t, act4.Outcome.Error, "forbidden by denylist")

	// 5. Test Velocity / Budget limits (max_calls: 3 for query_sql on database)
	// act1 and act4 each consumed 1 call (total 2).
	// act5 is the 3rd call (reaches limit).
	act5, err := svc.Submit(ctx, "analyst", "key-read-2", Request{
		Operation: "query_sql",
		Database:  "analytics",
		Args:      map[string]any{"query": "SELECT * FROM orders;"},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act5.State)

	// act6 is the 4th call, exceeding the max_calls: 3 budget
	act6, err := svc.Submit(ctx, "analyst", "key-read-3", Request{
		Operation: "query_sql",
		Database:  "analytics",
		Args:      map[string]any{"query": "SELECT * FROM events;"},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", act6.State)
	assert.Contains(t, act6.Reason, "budget query-budget exhausted")
}

func TestDatabaseMCPIntegration(t *testing.T) {
	ctx := context.Background()
	cfgYAML := `
name: database-mcp-gateway
admin_token_env: ADMIN_TOKEN
approval_ttl: 10m
databases:
  - id: reporting
    driver: mock
    allow_tables: ["users", "orders"]
agents:
  - id: reporter
    token_env: REPORTER_TOKEN
    databases: ["reporting"]
    actions: ["query_sql", "exec_sql", "list_tables", "describe_table"]
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	target, err := NewDatabaseTarget("reporting", "mock", "", false, 50, 5*time.Second, []string{"users", "orders"}, nil)
	require.NoError(t, err)
	dbExec := NewDatabaseExecutor(target)
	router := NewRouterExecutor(nil, nil, map[string]*DatabaseExecutor{"reporting": dbExec}, nil, nil, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	tokens := Tokens{
		Admin:  "admin-secret-token-32-chars-long-circuit",
		Agents: map[string]string{"reporter": "reporter-secret-token-32-chars-long"},
	}
	handler, err := NewHTTPHandler(svc, tokens)
	require.NoError(t, err)

	server := handler.mcpServer(cfg.Agents[0])
	require.NotNil(t, server)

	// Test db_query tool execution directly through service / router
	act, err := svc.Submit(ctx, "reporter", "mcp-db-query-1", Request{
		Operation: "query_sql",
		Database:  "reporting",
		Args: map[string]any{
			"query": "SELECT * FROM users;",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act.State)
	assert.NotNil(t, act.Outcome)
	assert.Equal(t, 200, act.Outcome.Status)
}
