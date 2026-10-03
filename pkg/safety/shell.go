package safety

import (
	"fmt"
	"mvdan.cc/sh/v3/syntax"
	"strings"
)

// CheckShell permits only literal invocations of administrator-approved commands.
// It never executes input. Filesystem permissions and executable contents are external.
func CheckShell(command string, allowed []string) error {
	if len(command) > MaxInspectionBytes {
		return fmt.Errorf("shell input exceeds inspection limit")
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("shell parse failed: %w", err)
	}
	if len(f.Stmts) == 0 {
		return fmt.Errorf("empty shell command")
	}
	if len(allowed) == 0 {
		allowed = []string{"echo", "printf", "pwd", "whoami", "ls", "cat", "head", "tail", "wc", "true", "false"}
	}
	allow := map[string]bool{}
	for _, name := range allowed {
		allow[name] = true
	}
	var failure error
	syntax.Walk(f, func(n syntax.Node) bool {
		if failure != nil {
			return false
		}
		switch node := n.(type) {
		case nil, *syntax.File, *syntax.BinaryCmd, *syntax.Comment, *syntax.Lit, *syntax.SglQuoted:
		case *syntax.Stmt:
			if node.Background || node.Coprocess || len(node.Redirs) > 0 {
				failure = fmt.Errorf("shell background jobs and redirections are forbidden")
			}
		case *syntax.CallExpr:
			if len(node.Assigns) > 0 || len(node.Args) == 0 {
				failure = fmt.Errorf("shell assignments are forbidden")
				break
			}
			var words []string
			for _, w := range node.Args {
				v, ok := literalWord(w)
				if !ok {
					failure = fmt.Errorf("shell dynamic expansion is forbidden")
					break
				}
				words = append(words, v)
			}
			if failure == nil && !allow[words[0]] {
				failure = fmt.Errorf("shell command %q is not allowlisted", words[0])
			}
		case *syntax.Word:
		case *syntax.DblQuoted:
		default:
			failure = fmt.Errorf("unsupported shell syntax %T is forbidden", n)
		}
		return failure == nil
	})
	return failure
}
func literalWord(w *syntax.Word) (string, bool) {
	var b strings.Builder
	var parts func([]syntax.WordPart) bool
	parts = func(ps []syntax.WordPart) bool {
		for _, p := range ps {
			switch x := p.(type) {
			case *syntax.Lit:
				if strings.ContainsAny(x.Value, "*?[]~\\") {
					return false
				}
				b.WriteString(x.Value)
			case *syntax.SglQuoted:
				b.WriteString(x.Value)
			case *syntax.DblQuoted:
				if !parts(x.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	ok := parts(w.Parts)
	return b.String(), ok
}
