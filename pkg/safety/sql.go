package safety

import (
	"fmt"
	"github.com/auxten/postgresql-parser/pkg/sql/parser"
	"github.com/auxten/postgresql-parser/pkg/sql/sem/tree"
	"reflect"
	"strings"
)

// CheckSQL admits read-only PostgreSQL-compatible ASTs; unknown syntax is denied.
// Database roles must still enforce read-only permissions: views and operators can
// invoke database code whose effects cannot be inferred from submitted SQL alone.
func CheckSQL(query string, allowedFunctions []string) error {
	if len(query) > MaxInspectionBytes {
		return fmt.Errorf("SQL input exceeds inspection limit")
	}
	statements, err := parser.Parse(query)
	if err != nil {
		return fmt.Errorf("SQL parse failed: %w", err)
	}
	if len(statements) == 0 {
		return fmt.Errorf("empty SQL query")
	}
	if len(allowedFunctions) == 0 {
		allowedFunctions = []string{"count", "sum", "avg", "min", "max", "lower", "upper", "length", "coalesce", "abs", "round"}
	}
	allow := map[string]bool{}
	for _, f := range allowedFunctions {
		allow[strings.ToLower(f)] = true
	}
	for _, s := range statements {
		if _, ok := s.AST.(*tree.Select); !ok {
			return fmt.Errorf("SQL statement %T is not read-only SELECT", s.AST)
		}
		if err := inspectSQL(reflect.ValueOf(s.AST), allow, 0); err != nil {
			return err
		}
	}
	return nil
}
func inspectSQL(v reflect.Value, allow map[string]bool, depth int) error {
	if !v.IsValid() {
		return nil
	}
	if depth > 256 {
		return fmt.Errorf("SQL nesting exceeds inspection limit")
	}
	if (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) && v.IsNil() {
		return nil
	}
	if v.CanInterface() {
		switch n := v.Interface().(type) {
		case tree.Statement:
			switch n.(type) {
			case *tree.Select, *tree.SelectClause, *tree.ParenSelect, *tree.UnionClause, *tree.ValuesClause:
			default:
				return fmt.Errorf("nested SQL mutation %T is forbidden", n)
			}
		case *tree.FuncExpr:
			name := strings.ToLower(tree.AsString(&n.Func))
			if !allow[name] {
				return fmt.Errorf("SQL function %q is not allowlisted", name)
			}
		}
		if n, ok := v.Interface().(*tree.Select); ok && len(n.Locking) > 0 {
			return fmt.Errorf("SQL row locks are forbidden")
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return inspectSQL(v.Elem(), allow, depth+1)
	case reflect.Struct:
		for j := 0; j < v.NumField(); j++ {
			if v.Type().Field(j).PkgPath == "" {
				if err := inspectSQL(v.Field(j), allow, depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		for j := 0; j < v.Len(); j++ {
			if err := inspectSQL(v.Index(j), allow, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
