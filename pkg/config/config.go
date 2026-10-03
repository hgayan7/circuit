package config

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultVersion is the default API schema version for policies.
const DefaultVersion = "v1alpha1"

// ActionType defines the decision made by a rule.
type ActionType string

const (
	ActionAllow           ActionType = "ALLOW"
	ActionDeny            ActionType = "DENY"
	ActionRequireApproval ActionType = "REQUIRE_APPROVAL"
)

// IsValid checks if an action string is one of the recognized ActionTypes.
func (a ActionType) IsValid() bool {
	switch a {
	case ActionAllow, ActionDeny, ActionRequireApproval:
		return true
	default:
		return false
	}
}

// MatchCriteria defines conditions for intercepting a tool or endpoint.
type MatchCriteria struct {
	Host     string `yaml:"host,omitempty"`
	Tool     string `yaml:"tool,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Method   string `yaml:"method,omitempty"`
	Path     string `yaml:"path,omitempty"`
}

// EscalationConfig defines where to route approval requests.
type EscalationConfig struct {
	Channel string `yaml:"channel"`
	Target  string `yaml:"target"`
	Timeout string `yaml:"timeout,omitempty"`
}

// BudgetConfig defines cumulative limits over a sliding time window.
type BudgetConfig struct {
	Window         string        `yaml:"window"`
	WindowDuration time.Duration `yaml:"-"`
	MaxCalls       int64         `yaml:"max_calls,omitempty"`
	MaxAmount      float64       `yaml:"max_amount,omitempty"`
	Currency       string        `yaml:"currency,omitempty"`
	AmountField    string        `yaml:"amount_field,omitempty"`
}

// Rule defines a single policy check.
type Rule struct {
	ID          string            `yaml:"id"`
	Description string            `yaml:"description,omitempty"`
	Match       MatchCriteria     `yaml:"match"`
	Condition   string            `yaml:"condition,omitempty"`
	Action      ActionType        `yaml:"action"`
	Reason      string            `yaml:"reason,omitempty"`
	Escalation  *EscalationConfig `yaml:"escalation,omitempty"`
	Budget      *BudgetConfig     `yaml:"budget,omitempty"`
}

// SafetyConfig enables bounded inspection of content and executable arguments.
type SafetyConfig struct {
	PromptInjection     bool     `yaml:"prompt_injection,omitempty"`
	Shell               bool     `yaml:"shell,omitempty"`
	SQL                 bool     `yaml:"sql,omitempty"`
	ShellFields         []string `yaml:"shell_fields,omitempty"`
	SQLFields           []string `yaml:"sql_fields,omitempty"`
	AllowedCommands     []string `yaml:"allowed_commands,omitempty"`
	AllowedSQLFunctions []string `yaml:"allowed_sql_functions,omitempty"`
}

// Policy defines the complete governance specification.
type Policy struct {
	Safety        SafetyConfig `yaml:"safety,omitempty"`
	Version       string       `yaml:"version,omitempty"`
	Name          string       `yaml:"name"`
	Description   string       `yaml:"description,omitempty"`
	DefaultAction ActionType   `yaml:"default_action,omitempty"`
	Rules         []Rule       `yaml:"rules"`
}

// ParsePolicy parses and validates a policy from an io.Reader.
func ParsePolicy(r io.Reader) (*Policy, error) {
	var policy Policy
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("failed to parse yaml policy: %w", err)
	}

	if policy.Version == "" {
		policy.Version = DefaultVersion
	}

	if policy.DefaultAction == "" {
		policy.DefaultAction = ActionAllow
	} else if !policy.DefaultAction.IsValid() {
		return nil, fmt.Errorf("invalid default_action: %s", policy.DefaultAction)
	}

	for i := range policy.Rules {
		rule := &policy.Rules[i]
		if strings.TrimSpace(rule.ID) == "" {
			return nil, fmt.Errorf("rule id is required at index %d", i)
		}
		if !rule.Action.IsValid() {
			return nil, fmt.Errorf("invalid action '%s' in rule '%s'", rule.Action, rule.ID)
		}
		if rule.Match.Host == "" && rule.Match.Tool == "" && rule.Match.Endpoint == "" && (rule.Match.Method == "" || rule.Match.Path == "") {
			return nil, fmt.Errorf("rule '%s' must specify at least match.tool, match.endpoint, or match.method+path", rule.ID)
		}

		if rule.Budget != nil && rule.Budget.Window != "" {
			d, err := time.ParseDuration(rule.Budget.Window)
			if err != nil {
				return nil, fmt.Errorf("invalid budget window '%s' in rule '%s': %w", rule.Budget.Window, rule.ID, err)
			}
			rule.Budget.WindowDuration = d
		}
	}

	return &policy, nil
}

// LoadPolicyFile reads and parses a policy from a file path.
func LoadPolicyFile(filePath string) (*Policy, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open policy file: %w", err)
	}
	defer file.Close()

	return ParsePolicy(file)
}
