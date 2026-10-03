package gateway

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/auxten/postgresql-parser/pkg/sql/parser"
	"github.com/auxten/postgresql-parser/pkg/sql/sem/tree"
)

// SQLAnalysis holds safety analysis metrics of a parsed SQL statement.
type SQLAnalysis struct {
	StatementType  string   `json:"statement_type"`
	IsReadOnly     bool     `json:"is_read_only"`
	IsDestructive  bool     `json:"is_destructive"`
	HasWhereClause bool     `json:"has_where_clause"`
	Tables         []string `json:"tables"`
	Reason         string   `json:"reason,omitempty"`
}

// AnalyzeSQL parses and inspects a SQL query to determine read/write classification,
// referenced tables, and whether operator review is required for destructive operations.
func AnalyzeSQL(query string) (*SQLAnalysis, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty SQL query")
	}
	if len(query) > 2<<20 {
		return nil, fmt.Errorf("SQL query exceeds 2 MiB inspection limit")
	}
	stmts, err := parser.Parse(query)
	if err != nil {
		return nil, fmt.Errorf("SQL parse failed: %w", err)
	}
	if len(stmts) == 0 {
		return nil, fmt.Errorf("no statements found in SQL query")
	}
	if len(stmts) > 1 {
		return nil, fmt.Errorf("multiple SQL statements in a single execution are forbidden")
	}

	analysis := &SQLAnalysis{
		IsReadOnly:     true,
		HasWhereClause: true,
		Tables:         []string{},
	}

	tableMap := make(map[string]bool)
	stmt := stmts[0].AST

	switch s := stmt.(type) {
	case *tree.Select:
		analysis.StatementType = "SELECT"
		analysis.IsReadOnly = true
		if len(s.Locking) > 0 {
			analysis.IsReadOnly = false
			analysis.Reason = "SELECT with row locks is treated as a mutation"
		}
	case *tree.Insert:
		analysis.StatementType = "INSERT"
		analysis.IsReadOnly = false
	case *tree.Update:
		analysis.StatementType = "UPDATE"
		analysis.IsReadOnly = false
		if s.Where == nil {
			analysis.HasWhereClause = false
			analysis.IsDestructive = true
			analysis.Reason = "UPDATE without WHERE clause affects all rows and requires operator review"
		}
	case *tree.Delete:
		analysis.StatementType = "DELETE"
		analysis.IsReadOnly = false
		if s.Where == nil {
			analysis.HasWhereClause = false
			analysis.IsDestructive = true
			analysis.Reason = "DELETE without WHERE clause affects all rows and requires operator review"
		}
	case *tree.DropTable:
		analysis.StatementType = "DROP TABLE"
		analysis.IsReadOnly = false
		analysis.IsDestructive = true
		analysis.Reason = "DROP TABLE is destructive and requires operator review"
	case *tree.Truncate:
		analysis.StatementType = "TRUNCATE"
		analysis.IsReadOnly = false
		analysis.IsDestructive = true
		analysis.Reason = "TRUNCATE is destructive and requires operator review"
	case *tree.AlterTable:
		analysis.StatementType = "ALTER TABLE"
		analysis.IsReadOnly = false
		analysis.IsDestructive = true
		analysis.Reason = "ALTER TABLE modifies schema and requires operator review"
	case *tree.CreateTable:
		analysis.StatementType = "CREATE TABLE"
		analysis.IsReadOnly = false
	case *tree.DropDatabase:
		analysis.StatementType = "DROP DATABASE"
		analysis.IsReadOnly = false
		analysis.IsDestructive = true
		analysis.Reason = "DROP DATABASE is destructive and requires operator review"
	default:
		analysis.StatementType = fmt.Sprintf("%T", stmt)
		analysis.IsReadOnly = false
		analysis.IsDestructive = true
		analysis.Reason = fmt.Sprintf("consequential statement type %T requires operator review", stmt)
	}

	extractTables(reflect.ValueOf(stmt), tableMap, 0)
	for t := range tableMap {
		if t != "" {
			analysis.Tables = append(analysis.Tables, t)
		}
	}
	return analysis, nil
}

func extractTables(v reflect.Value, tableMap map[string]bool, depth int) {
	if !v.IsValid() || depth > 256 {
		return
	}
	if (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) && v.IsNil() {
		return
	}
	if v.CanInterface() {
		if tn, ok := v.Interface().(*tree.TableName); ok && tn != nil {
			tableMap[strings.ToLower(tn.Table())] = true
		} else if tn, ok := v.Interface().(tree.TableName); ok {
			tableMap[strings.ToLower((&tn).Table())] = true
		} else if un, ok := v.Interface().(*tree.UnresolvedObjectName); ok && un != nil {
			tbl := un.ToTableName()
			tableMap[strings.ToLower((&tbl).Table())] = true
		} else if un, ok := v.Interface().(tree.UnresolvedObjectName); ok {
			tbl := un.ToTableName()
			tableMap[strings.ToLower((&tbl).Table())] = true
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		extractTables(v.Elem(), tableMap, depth+1)
	case reflect.Struct:
		for j := 0; j < v.NumField(); j++ {
			if v.Type().Field(j).PkgPath == "" {
				extractTables(v.Field(j), tableMap, depth+1)
			}
		}
	case reflect.Slice, reflect.Array:
		for j := 0; j < v.Len(); j++ {
			extractTables(v.Index(j), tableMap, depth+1)
		}
	}
}

// CheckTableAccess verifies whether the extracted tables are permitted by the allowlist and denylist.
func CheckTableAccess(tables []string, allowlist, denylist []string) error {
	allowMap := make(map[string]bool)
	for _, a := range allowlist {
		allowMap[strings.ToLower(a)] = true
	}
	denyMap := make(map[string]bool)
	for _, d := range denylist {
		denyMap[strings.ToLower(d)] = true
	}

	for _, tbl := range tables {
		tbl = strings.ToLower(tbl)
		if denyMap[tbl] {
			return fmt.Errorf("access to sensitive table %q is forbidden by denylist", tbl)
		}
		if len(allowlist) > 0 && !allowMap[tbl] {
			return fmt.Errorf("table %q is not in the allowed tables list", tbl)
		}
	}
	return nil
}
