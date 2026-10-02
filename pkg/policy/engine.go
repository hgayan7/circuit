package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"github.com/hgayan7/circuit/pkg/budget"
	"github.com/hgayan7/circuit/pkg/config"
)

// EvaluationContext contains the runtime details of the intercepted request.
type EvaluationContext struct {
	Tool      string         `json:"tool,omitempty"`
	Endpoint  string         `json:"endpoint,omitempty"`
	Method    string         `json:"method,omitempty"`
	Path      string         `json:"path,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// EvaluationResult contains the verdict of the policy engine.
type EvaluationResult struct {
	Action     config.ActionType        `json:"action"`
	RuleID     string                   `json:"rule_id,omitempty"`
	Reason     string                   `json:"reason,omitempty"`
	Escalation *config.EscalationConfig `json:"escalation,omitempty"`
	Rule       *config.Rule             `json:"-"`
}

// compiledRule holds pre-compiled CEL programs for condition and budget calculations.
type compiledRule struct {
	rule          config.Rule
	program       cel.Program
	amountProgram cel.Program
}

// Engine evaluates intercepted requests against compiled policies and cumulative budgets.
type Engine struct {
	policy        *config.Policy
	compiledRules []compiledRule
	celEnv        *cel.Env
	budgeter      budget.Budgeter
}

// Option allows configuring optional Engine settings.
type Option func(*Engine)

// WithBudgeter configures a custom budget tracker.
func WithBudgeter(b budget.Budgeter) Option {
	return func(e *Engine) {
		e.budgeter = b
	}
}

// NewEngine initializes and pre-compiles all CEL rules in the policy.
func NewEngine(pol *config.Policy, opts ...Option) (*Engine, error) {
	if pol == nil {
		return nil, fmt.Errorf("policy cannot be nil")
	}

	// Declare standard variables accessible in CEL expressions
	env, err := cel.NewEnv(
		cel.Variable("args", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("tool", cel.StringType),
		cel.Variable("endpoint", cel.StringType),
		cel.Variable("method", cel.StringType),
		cel.Variable("path", cel.StringType),
		cel.Variable("session_id", cel.StringType),
		cel.Variable("agent_id", cel.StringType),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cel environment: %w", err)
	}

	var compiled []compiledRule
	for _, rule := range pol.Rules {
		cr := compiledRule{rule: rule}
		if rule.Condition != "" {
			ast, issues := env.Compile(rule.Condition)
			if issues != nil && issues.Err() != nil {
				return nil, fmt.Errorf("failed to compile condition in rule '%s': %w", rule.ID, issues.Err())
			}
			prg, err := env.Program(ast)
			if err != nil {
				return nil, fmt.Errorf("failed to create cel program for rule '%s': %w", rule.ID, err)
			}
			cr.program = prg
		}

		if rule.Budget != nil && rule.Budget.AmountField != "" {
			ast, issues := env.Compile(rule.Budget.AmountField)
			if issues != nil && issues.Err() != nil {
				return nil, fmt.Errorf("failed to compile budget amount_field in rule '%s': %w", rule.ID, issues.Err())
			}
			prg, err := env.Program(ast)
			if err != nil {
				return nil, fmt.Errorf("failed to create budget amount program for rule '%s': %w", rule.ID, err)
			}
			cr.amountProgram = prg
		}

		compiled = append(compiled, cr)
	}

	engine := &Engine{
		policy:        pol,
		compiledRules: compiled,
		celEnv:        env,
		budgeter:      budget.NewMemoryBudgeter(),
	}

	for _, opt := range opts {
		opt(engine)
	}

	return engine, nil
}

// matches checks whether an evaluation context matches a rule's match criteria.
func (e *Engine) matches(rule *config.Rule, ctx *EvaluationContext) bool {
	match := rule.Match

	// 1. Tool Matching (MCP or function calls)
	if match.Tool != "" && ctx.Tool != "" {
		if match.Tool == ctx.Tool {
			return true
		}
		if ok, _ := filepath.Match(match.Tool, ctx.Tool); ok {
			return true
		}
	}

	// 2. Endpoint Matching (e.g. "POST /v1/refunds")
	if match.Endpoint != "" {
		targetEndpoint := ctx.Endpoint
		if targetEndpoint == "" && ctx.Method != "" && ctx.Path != "" {
			targetEndpoint = ctx.Method + " " + ctx.Path
		}
		if targetEndpoint != "" {
			if match.Endpoint == targetEndpoint {
				return true
			}
			if ok, _ := filepath.Match(match.Endpoint, targetEndpoint); ok {
				return true
			}
		}
	}

	// 3. Method & Path Matching
	if match.Method != "" && match.Path != "" && ctx.Method != "" && ctx.Path != "" {
		if strings.EqualFold(match.Method, ctx.Method) {
			if match.Path == ctx.Path {
				return true
			}
			if ok, _ := filepath.Match(match.Path, ctx.Path); ok {
				return true
			}
		}
	}

	return false
}

func toFloat64(val any) (float64, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", val)
	}
}

// Evaluate evaluates an incoming request context against the policy rules in order.
func (e *Engine) Evaluate(ctx context.Context, evalCtx *EvaluationContext) (*EvaluationResult, error) {
	if evalCtx == nil {
		return nil, fmt.Errorf("evaluation context cannot be nil")
	}

	args := evalCtx.Args
	if args == nil {
		args = make(map[string]any)
	}

	activation := map[string]any{
		"args":       args,
		"tool":       evalCtx.Tool,
		"endpoint":   evalCtx.Endpoint,
		"method":     evalCtx.Method,
		"path":       evalCtx.Path,
		"session_id": evalCtx.SessionID,
		"agent_id":   evalCtx.AgentID,
	}

	for _, cr := range e.compiledRules {
		if !e.matches(&cr.rule, evalCtx) {
			continue
		}

		// If there is a CEL condition, evaluate it
		if cr.program != nil {
			out, _, err := cr.program.Eval(activation)
			if err != nil {
				return nil, fmt.Errorf("error evaluating rule '%s': %w", cr.rule.ID, err)
			}

			boolVal, ok := out.Value().(bool)
			if !ok || !boolVal {
				// Condition was false, continue to next rule
				continue
			}
		}

		// If rule specifies a cumulative budget, evaluate it
		if cr.rule.Budget != nil && e.budgeter != nil {
			budgetKey := "global:" + cr.rule.ID
			if evalCtx.SessionID != "" {
				budgetKey = "sess:" + evalCtx.SessionID + ":" + cr.rule.ID
			} else if evalCtx.AgentID != "" {
				budgetKey = "agent:" + evalCtx.AgentID + ":" + cr.rule.ID
			}

			var amount float64
			if cr.amountProgram != nil {
				val, _, err := cr.amountProgram.Eval(activation)
				if err != nil {
					return nil, fmt.Errorf("error extracting budget amount in rule '%s': %w", cr.rule.ID, err)
				}
				converted, err := toFloat64(val.Value())
				if err != nil {
					return nil, fmt.Errorf("invalid budget amount in rule '%s': %w", cr.rule.ID, err)
				}
				amount = converted
			}

			bRes, err := e.budgeter.RecordAndCheck(ctx, budgetKey, cr.rule.Budget, amount)
			if err != nil {
				return nil, fmt.Errorf("budget check error in rule '%s': %w", cr.rule.ID, err)
			}
			if !bRes.Allowed {
				return &EvaluationResult{
					Action: config.ActionDeny,
					RuleID: cr.rule.ID,
					Reason: bRes.Reason,
					Rule:   &cr.rule,
				}, nil
			}
		}

		// Match found and condition satisfied
		return &EvaluationResult{
			Action:     cr.rule.Action,
			RuleID:     cr.rule.ID,
			Reason:     cr.rule.Reason,
			Escalation: cr.rule.Escalation,
			Rule:       &cr.rule,
		}, nil
	}

	// Default fallback
	return &EvaluationResult{
		Action: e.policy.DefaultAction,
		Reason: "Default policy action applied",
	}, nil
}
