// Package initwizard implements the interactive `circuit init` wizard.
// It asks a few targeted questions and generates a tailored circuit.yaml.
package initwizard

import (
	"bufio"
	"fmt"
	"github.com/hgayan7/circuit/pkg/config"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// answers holds all wizard responses before rendering.
type answers struct {
	Mode            string // "run" | "mcp"
	Stack           string // "openai" | "anthropic" | "stripe" | "postgres" | "custom"
	HasBudget       bool
	MaxActions      int
	MaxSpend        float64
	HasAudit        bool
	RequireApproval bool
}

// Run executes the wizard, writing to outDir/circuit.yaml.
func Run(in io.Reader, out io.Writer, outDir string) error {
	scanner := bufio.NewScanner(in)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "⚡ Circuit Init — generate a circuit.yaml for your project")
	fmt.Fprintln(out, strings.Repeat("─", 52))

	a := &answers{}

	// ── Step 1: Integration mode ────────────────────────────
	fmt.Fprintln(out, "\nHow are you running your agent?")
	fmt.Fprintln(out, "  1) circuit run   — wrap an agent process (HTTP APIs)")
	fmt.Fprintln(out, "  2) circuit mcp   — wrap an MCP stdio server (tool calls)")
	fmt.Fprint(out, "Choice [1]: ")

	modeStr, err := readLine(scanner)
	if err != nil {
		return fmt.Errorf("aborted: %w", err)
	}
	if modeStr == "" || modeStr == "1" {
		a.Mode = "run"
	} else if modeStr == "2" {
		a.Mode = "mcp"
	} else {
		a.Mode = "run"
	}

	// ── Step 2: Stack ───────────────────────────────────────
	fmt.Fprintln(out, "\nWhat API or server are you protecting?")
	fmt.Fprintln(out, "  1) OpenAI")
	fmt.Fprintln(out, "  2) Anthropic")
	fmt.Fprintln(out, "  3) Postgres MCP")
	fmt.Fprintln(out, "  4) Stripe")
	fmt.Fprintln(out, "  5) Custom / Other")
	fmt.Fprint(out, "Choice [1]: ")

	stackStr, err := readLine(scanner)
	if err != nil {
		return fmt.Errorf("aborted: %w", err)
	}
	switch stackStr {
	case "2":
		a.Stack = "anthropic"
	case "3":
		a.Stack = "postgres"
	case "4":
		a.Stack = "stripe"
	case "5":
		a.Stack = "custom"
	default:
		a.Stack = "openai"
	}

	// ── Step 3: Budget ──────────────────────────────────────
	fmt.Fprint(out, "\nAdd spending/action budgets? [y/N]: ")
	budgetStr, err := readLine(scanner)
	if err != nil {
		return fmt.Errorf("aborted: %w", err)
	}
	if strings.ToLower(budgetStr) == "y" {
		a.HasBudget = true

		fmt.Fprint(out, "  Max actions per hour [100]: ")
		actStr, err := readLine(scanner)
		if err != nil {
			return fmt.Errorf("aborted: %w", err)
		}
		if actStr == "" {
			a.MaxActions = 100
		} else {
			n, _ := strconv.Atoi(actStr)
			if n > 0 {
				a.MaxActions = n
			} else {
				a.MaxActions = 100
			}
		}

		fmt.Fprint(out, "  Max spend per hour in USD [5.00]: ")
		spendStr, err := readLine(scanner)
		if err != nil {
			return fmt.Errorf("aborted: %w", err)
		}
		if spendStr == "" {
			a.MaxSpend = 5.00
		} else {
			f, _ := strconv.ParseFloat(spendStr, 64)
			if f > 0 {
				a.MaxSpend = f
			} else {
				a.MaxSpend = 5.00
			}
		}
	}

	// ── Step 4: Audit ───────────────────────────────────────
	fmt.Fprint(out, "\nEnable audit log? [y/N]: ")
	auditStr, err := readLine(scanner)
	if err != nil {
		return fmt.Errorf("aborted: %w", err)
	}
	a.HasAudit = strings.ToLower(auditStr) == "y"

	// ── Step 5: HITL ────────────────────────────────────────
	fmt.Fprint(out, "\nRequire human approval for sensitive actions? [y/N]: ")
	hitlStr, err := readLine(scanner)
	if err != nil {
		return fmt.Errorf("aborted: %w", err)
	}
	a.RequireApproval = strings.ToLower(hitlStr) == "y"

	// ── Render ──────────────────────────────────────────────
	outPath := filepath.Join(outDir, "circuit.yaml")
	if _, err := os.Stat(outPath); err == nil {
		fmt.Fprintf(out, "\n⚠️  circuit.yaml already exists in %s — skipping write.\n", outDir)
		fmt.Fprintln(out, "   Delete or rename it first if you want to regenerate.")
		return nil
	}

	content, err := render(a)
	if err != nil {
		return fmt.Errorf("render failed: %w", err)
	}

	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	printNextSteps(out, a, outPath)
	return nil
}

// readLine reads one line from the scanner, trimming whitespace.
// Returns io.EOF error when the scanner is exhausted.
func readLine(s *bufio.Scanner) (string, error) {
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return strings.TrimSpace(s.Text()), nil
}

// ── Templates ────────────────────────────────────────────────────────────────

func render(a *answers) (string, error) {
	action := config.ActionAllow
	if a.RequireApproval {
		action = config.ActionRequireApproval
	}
	rule := config.Rule{ID: "agent-actions", Action: action, Match: config.MatchCriteria{Endpoint: "*"}}
	pol := config.Policy{Name: a.Stack + "-guard", Version: config.DefaultVersion, DefaultAction: config.ActionAllow, Safety: config.SafetyConfig{PromptInjection: true}}
	switch a.Stack {
	case "openai":
		rule.Match = config.MatchCriteria{Endpoint: "POST /v1/chat/completions"}
	case "anthropic":
		rule.Match = config.MatchCriteria{Endpoint: "POST /v1/messages"}
	case "stripe":
		rule.Match = config.MatchCriteria{Endpoint: "POST /v1/charges"}
	case "postgres":
		rule.Match = config.MatchCriteria{Tool: "*"}
		pol.Safety.SQL = true
	}
	if a.HasBudget {
		rule.Budget = &config.BudgetConfig{Window: "1h", MaxCalls: int64(a.MaxActions)}
		if a.Stack == "stripe" {
			rule.Budget.MaxAmount = a.MaxSpend
			rule.Budget.AmountField = "double(int(args.amount)) / 100.0"
		}
	}
	if a.RequireApproval {
		rule.Escalation = &config.EscalationConfig{Channel: "cli", Timeout: "30s"}
	}
	pol.Rules = []config.Rule{rule}
	data, err := yaml.Marshal(pol)
	if err != nil {
		return "", err
	}
	content := "# Generated by circuit init\n" + string(data)
	if a.HasAudit {
		content += "\n# Enable audit logging with --audit circuit.audit.ndjson\n"
	}
	if a.HasBudget && (a.Stack == "openai" || a.Stack == "anthropic") {
		content += "\n# Action limits enabled. Token-based monetary accounting is not implemented.\n"
	}
	return content, nil
}

// ── Next steps ───────────────────────────────────────────────────────────────

func printNextSteps(out io.Writer, a *answers, outPath string) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "✅ Created", outPath)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Next steps:")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  1. Review and edit circuit.yaml to tune your rules")
	fmt.Fprintln(out, "  2. Validate the policy:")
	fmt.Fprintln(out, "       circuit check")
	fmt.Fprintln(out, "")

	if a.Mode == "mcp" {
		fmt.Fprintln(out, "  3. Wrap your MCP server:")
		fmt.Fprintln(out, "       circuit mcp wrap -- npx -y @modelcontextprotocol/server-postgres")
	} else {
		fmt.Fprintln(out, "  3. Run your agent with Circuit enforcing policies:")
		fmt.Fprintln(out, "       circuit run -- python agent.py")
	}
	fmt.Fprintln(out, "")
}
