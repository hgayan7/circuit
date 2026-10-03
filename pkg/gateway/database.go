package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// DatabaseTarget defines an isolated, governed database connection.
type DatabaseTarget struct {
	ID          string
	Driver      string
	DSN         string
	ReadOnly    bool
	MaxRows     int
	MaxTimeout  time.Duration
	AllowTables []string
	DenyTables  []string
	DB          *sql.DB
	simulated   bool
	simMu       sync.RWMutex
	simTables   map[string][]map[string]any
}

// NewDatabaseTarget uses simulation only when driver is explicitly "mock".
func NewDatabaseTarget(id, driver, dsn string, readOnly bool, maxRows int, maxTimeout time.Duration, allowTables, denyTables []string) (*DatabaseTarget, error) {
	if id == "" {
		return nil, fmt.Errorf("database ID is required")
	}
	if driver != "mock" && (dsn == "" || driver == "") {
		return nil, fmt.Errorf("database %s requires an explicit driver and nonempty DSN; use driver mock for simulation", id)
	}
	if maxRows <= 0 {
		maxRows = 500
	}
	if maxTimeout <= 0 {
		maxTimeout = 15 * time.Second
	}

	target := &DatabaseTarget{
		ID:          id,
		Driver:      driver,
		DSN:         dsn,
		ReadOnly:    readOnly,
		MaxRows:     maxRows,
		MaxTimeout:  maxTimeout,
		AllowTables: allowTables,
		DenyTables:  denyTables,
		simTables:   make(map[string][]map[string]any),
	}

	if driver == "mock" {
		target.simulated = true
		// Seed default sample data for simulation/demos
		target.simTables["users"] = []map[string]any{
			{"id": 1, "username": "alice", "email": "alice@example.com", "role": "admin"},
			{"id": 2, "username": "bob", "email": "bob@example.com", "role": "engineer"},
		}
		target.simTables["orders"] = []map[string]any{
			{"id": 101, "user_id": 1, "amount": 99.50, "status": "completed"},
			{"id": 102, "user_id": 2, "amount": 25.00, "status": "pending"},
		}
	} else {
		db, err := sql.Open(driver, dsn)
		if err != nil {
			return nil, fmt.Errorf("connecting to database %s: %w", id, err)
		}
		db.SetConnMaxLifetime(5 * time.Minute)
		db.SetMaxIdleConns(5)
		db.SetMaxOpenConns(20)
		target.DB = db
		ctx, cancel := context.WithTimeout(context.Background(), maxTimeout)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("database %s connection check failed", id)
		}
	}

	return target, nil
}

// DatabaseExecutor executes governed SQL queries and mutations on a DatabaseTarget.
type DatabaseExecutor struct {
	target *DatabaseTarget
}

// NewDatabaseExecutor creates a DatabaseExecutor for a target.
func NewDatabaseExecutor(target *DatabaseTarget) *DatabaseExecutor {
	return &DatabaseExecutor{target: target}
}

func (e *DatabaseExecutor) Execute(ctx context.Context, r Request) Outcome {
	ctx, cancel := context.WithTimeout(ctx, e.target.MaxTimeout)
	defer cancel()
	switch r.Operation {
	case "query_sql":
		return e.querySQL(ctx, r)
	case "exec_sql":
		return e.execSQL(ctx, r)
	case "list_tables":
		return e.listTables(ctx, r)
	case "describe_table":
		return e.describeTable(ctx, r)
	default:
		return Outcome{Error: fmt.Sprintf("unsupported database operation %q", r.Operation)}
	}
}

func (e *DatabaseExecutor) querySQL(ctx context.Context, r Request) Outcome {
	query := text(r.Args, "query")
	if strings.TrimSpace(query) == "" {
		return Outcome{Error: "query is required"}
	}

	analysis, err := AnalyzeSQL(query)
	if err != nil {
		return Outcome{Error: fmt.Sprintf("SQL safety analysis failed: %v", err)}
	}
	if !analysis.IsReadOnly {
		return Outcome{Status: 400, Error: "query_sql only permits read-only SELECT statements; use exec_sql for mutations"}
	}
	if err := CheckTableAccess(analysis.Tables, e.target.AllowTables, e.target.DenyTables); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	timeout := e.target.MaxTimeout
	if tSec := number(r.Args, "timeout_sec"); tSec > 0 {
		userTimeout := time.Duration(tSec) * time.Second
		if userTimeout < timeout {
			timeout = userTimeout
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxRows := e.target.MaxRows
	if mr := number(r.Args, "max_rows"); mr > 0 && mr < maxRows {
		maxRows = mr
	}

	startTime := time.Now()

	if e.target.simulated {
		return e.simulatedQuery(analysis, maxRows, startTime)
	}

	tx, err := e.target.DB.BeginTx(callCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Outcome{Status: 500, Error: "beginning read-only transaction failed"}
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(callCtx, "SET LOCAL search_path = public, pg_catalog"); err != nil {
		return Outcome{Status: 500, Error: "setting query schema failed"}
	}
	rows, err := tx.QueryContext(callCtx, query)
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("query failed: %v", err)}
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("reading columns: %v", err)}
	}

	var results [][]any
	count := 0
	truncated := false

	for rows.Next() {
		if count >= maxRows {
			truncated = true
			break
		}
		values := make([]any, len(cols))
		scanArgs := make([]any, len(cols))
		for i := range values {
			scanArgs[i] = &values[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return Outcome{Status: 500, Error: fmt.Sprintf("scanning row: %v", err)}
		}
		// Convert byte slices to strings for JSON serializability
		cleanRow := make([]any, len(values))
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				cleanRow[i] = string(b)
			} else {
				cleanRow[i] = v
			}
		}
		results = append(results, cleanRow)
		count++
	}
	if err := rows.Err(); err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("reading query results: %v", err)}
	}

	duration := time.Since(startTime)
	resPayload := map[string]any{
		"columns":     cols,
		"rows":        results,
		"row_count":   count,
		"truncated":   truncated,
		"duration_ms": duration.Milliseconds(),
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

func (e *DatabaseExecutor) simulatedQuery(analysis *SQLAnalysis, maxRows int, start time.Time) Outcome {
	e.target.simMu.RLock()
	defer e.target.simMu.RUnlock()

	var cols []string
	var rows [][]any
	truncated := false

	tableName := "default"
	if len(analysis.Tables) > 0 {
		tableName = analysis.Tables[0]
	}

	data, ok := e.target.simTables[tableName]
	if ok && len(data) > 0 {
		for k := range data[0] {
			cols = append(cols, k)
		}
		for i, record := range data {
			if i >= maxRows {
				truncated = true
				break
			}
			row := make([]any, len(cols))
			for j, col := range cols {
				row[j] = record[col]
			}
			rows = append(rows, row)
		}
	} else {
		cols = []string{"result"}
		rows = [][]any{{"simulated query executed successfully"}}
	}

	duration := time.Since(start)
	resPayload := map[string]any{
		"columns":     cols,
		"rows":        rows,
		"row_count":   len(rows),
		"truncated":   truncated,
		"simulated":   true,
		"duration_ms": duration.Milliseconds(),
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

func (e *DatabaseExecutor) execSQL(ctx context.Context, r Request) Outcome {
	if e.target.ReadOnly {
		return Outcome{Status: 403, Error: "database is configured as read-only; mutations are forbidden"}
	}

	query := text(r.Args, "query")
	if strings.TrimSpace(query) == "" {
		return Outcome{Error: "query is required"}
	}

	analysis, err := AnalyzeSQL(query)
	if err != nil {
		return Outcome{Error: fmt.Sprintf("SQL safety analysis failed: %v", err)}
	}
	if err := CheckTableAccess(analysis.Tables, e.target.AllowTables, e.target.DenyTables); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	timeout := e.target.MaxTimeout
	if tSec := number(r.Args, "timeout_sec"); tSec > 0 {
		userTimeout := time.Duration(tSec) * time.Second
		if userTimeout < timeout {
			timeout = userTimeout
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxAffected := number(r.Args, "max_affected_rows")
	startTime := time.Now()

	if e.target.simulated {
		e.target.simMu.Lock()
		defer e.target.simMu.Unlock()
		duration := time.Since(startTime)
		resPayload := map[string]any{
			"rows_affected":  1,
			"statement_type": analysis.StatementType,
			"simulated":      true,
			"duration_ms":    duration.Milliseconds(),
		}
		body, _ := json.Marshal(resPayload)
		return Outcome{Status: 200, Body: body}
	}

	tx, err := e.target.DB.BeginTx(callCtx, nil)
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("beginning transaction: %v", err)}
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(callCtx, "SET LOCAL search_path = public, pg_catalog"); err != nil {
		return Outcome{Status: 500, Error: "setting mutation schema failed"}
	}
	res, err := tx.ExecContext(callCtx, query)
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("executing statement: %v", err)}
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil && maxAffected > 0 {
		return Outcome{Status: 500, Error: "cannot verify affected-row limit; transaction rolled back"}
	}
	lastInsertID, _ := res.LastInsertId()

	if maxAffected > 0 && int(rowsAffected) > maxAffected {
		return Outcome{
			Status: 400,
			Error:  fmt.Sprintf("statement affected %d rows, exceeding allowed limit of %d (transaction rolled back)", rowsAffected, maxAffected),
		}
	}

	if err := tx.Commit(); err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("committing transaction: %v", err), Uncertain: true}
	}

	duration := time.Since(startTime)
	resPayload := map[string]any{
		"rows_affected":  rowsAffected,
		"last_insert_id": lastInsertID,
		"statement_type": analysis.StatementType,
		"duration_ms":    duration.Milliseconds(),
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

func (e *DatabaseExecutor) listTables(ctx context.Context, _ Request) Outcome {
	if e.target.simulated {
		e.target.simMu.RLock()
		defer e.target.simMu.RUnlock()
		tables := []string{}
		for tbl := range e.target.simTables {
			if CheckTableAccess([]string{tbl}, e.target.AllowTables, e.target.DenyTables) == nil {
				tables = append(tables, tbl)
			}
		}
		body, _ := json.Marshal(map[string]any{"tables": tables, "count": len(tables), "simulated": true})
		return Outcome{Status: 200, Body: body}
	}

	query := `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name;`
	rows, err := e.target.DB.QueryContext(ctx, query)
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("listing tables: %v", err)}
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return Outcome{Status: 500, Error: "reading table metadata failed"}
		}
		if CheckTableAccess([]string{name}, e.target.AllowTables, e.target.DenyTables) == nil {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		return Outcome{Status: 500, Error: "reading table metadata failed"}
	}

	body, _ := json.Marshal(map[string]any{"tables": tables, "count": len(tables)})
	return Outcome{Status: 200, Body: body}
}

func (e *DatabaseExecutor) describeTable(ctx context.Context, r Request) Outcome {
	table := text(r.Args, "table")
	if table == "" {
		return Outcome{Error: "table argument is required"}
	}
	if err := CheckTableAccess([]string{table}, e.target.AllowTables, e.target.DenyTables); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	if e.target.simulated {
		e.target.simMu.RLock()
		defer e.target.simMu.RUnlock()
		cols := []map[string]any{}
		if data, ok := e.target.simTables[table]; ok && len(data) > 0 {
			for k := range data[0] {
				cols = append(cols, map[string]any{
					"column_name": k,
					"data_type":   "text",
					"is_nullable": "YES",
				})
			}
		}
		body, _ := json.Marshal(map[string]any{"table": table, "columns": cols, "simulated": true})
		return Outcome{Status: 200, Body: body}
	}

	query := `SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position;`
	rows, err := e.target.DB.QueryContext(ctx, query, table)
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("describing table %s: %v", table, err)}
	}
	defer rows.Close()

	var columns []map[string]any
	for rows.Next() {
		var cName, dType, nullable string
		if err := rows.Scan(&cName, &dType, &nullable); err != nil {
			return Outcome{Status: 500, Error: "reading column metadata failed"}
		}
		columns = append(columns, map[string]any{
			"column_name": cName,
			"data_type":   dType,
			"is_nullable": nullable,
		})
	}
	if err := rows.Err(); err != nil {
		return Outcome{Status: 500, Error: "reading column metadata failed"}
	}

	body, _ := json.Marshal(map[string]any{"table": table, "columns": columns})
	return Outcome{Status: 200, Body: body}
}
