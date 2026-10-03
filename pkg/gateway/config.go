// Package gateway implements a durable, scoped GitHub action gateway.
package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"github.com/hgayan7/circuit/pkg/config"
	"gopkg.in/yaml.v3"
)

type Agent struct {
	ID           string   `yaml:"id" json:"id"`
	TokenEnv     string   `yaml:"token_env" json:"token_env"`
	Repositories []string `yaml:"repositories" json:"repositories"`
	Actions      []string `yaml:"actions" json:"actions"`
	BranchPrefix string   `yaml:"branch_prefix" json:"branch_prefix"`
}
type Rule struct {
	ID           string            `yaml:"id" json:"id"`
	Actions      []string          `yaml:"actions" json:"actions"`
	Repositories []string          `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Condition    string            `yaml:"condition,omitempty" json:"condition,omitempty"`
	Action       config.ActionType `yaml:"action" json:"action"`
	Reason       string            `yaml:"reason,omitempty" json:"reason,omitempty"`
	program      cel.Program
}
type Limit struct {
	ID       string   `yaml:"id" json:"id"`
	Actions  []string `yaml:"actions" json:"actions"`
	Scope    string   `yaml:"scope" json:"scope"`
	Window   string   `yaml:"window" json:"window"`
	MaxCalls int      `yaml:"max_calls" json:"max_calls"`
	duration time.Duration
}
type GitHubAppConfig struct {
	AppID          int64  `yaml:"app_id" json:"app_id"`
	PrivateKeyEnv  string `yaml:"private_key_env,omitempty" json:"private_key_env,omitempty"`
	PrivateKeyFile string `yaml:"private_key_file,omitempty" json:"private_key_file,omitempty"`
	InstallationID int64  `yaml:"installation_id,omitempty" json:"installation_id,omitempty"`
}

type WebhookConfig struct {
	SecretEnv string `yaml:"secret_env,omitempty" json:"secret_env,omitempty"`
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`
}

type Config struct {
	Name           string              `yaml:"name" json:"name"`
	AdminTokenEnv  string              `yaml:"admin_token_env" json:"admin_token_env"`
	GitHubTokenEnv string              `yaml:"github_token_env,omitempty" json:"github_token_env,omitempty"`
	GitHubApp      *GitHubAppConfig    `yaml:"github_app,omitempty" json:"github_app,omitempty"`
	Webhook        *WebhookConfig      `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	ApprovalTTL    string              `yaml:"approval_ttl" json:"approval_ttl"`
	Agents         []Agent             `yaml:"agents" json:"agents"`
	Rules          []Rule              `yaml:"rules,omitempty" json:"rules,omitempty"`
	Limits         []Limit             `yaml:"limits,omitempty" json:"limits,omitempty"`
	Safety         config.SafetyConfig `yaml:"safety,omitempty" json:"safety"`
	ttl            time.Duration
	digest         string
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var operations = map[string]bool{"read_file": true, "get_pr": true, "create_branch": true, "put_file": true, "create_pr": true, "merge_pr": true, "create_issue": true, "update_issue": true}

func member(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseConfig(f)
}
func ParseConfig(r io.Reader) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c.Name == "" {
		return nil, fmt.Errorf("gateway name is required")
	}
	if c.AdminTokenEnv == "" {
		c.AdminTokenEnv = "CIRCUIT_ADMIN_TOKEN"
	}
	if c.GitHubApp != nil {
		if c.GitHubApp.AppID <= 0 {
			return nil, fmt.Errorf("github_app app_id must be a positive integer")
		}
		if c.GitHubApp.PrivateKeyEnv == "" && c.GitHubApp.PrivateKeyFile == "" {
			return nil, fmt.Errorf("github_app requires either private_key_env or private_key_file")
		}
		if c.GitHubApp.InstallationID < 0 {
			return nil, fmt.Errorf("github_app installation_id cannot be negative")
		}
	} else if c.GitHubTokenEnv == "" {
		c.GitHubTokenEnv = "GITHUB_TOKEN"
	}
	if c.Webhook != nil {
		if c.Webhook.SecretEnv == "" {
			c.Webhook.SecretEnv = "GITHUB_WEBHOOK_SECRET"
		}
		if c.Webhook.Path == "" {
			c.Webhook.Path = "/webhooks/github"
		}
		if !strings.HasPrefix(c.Webhook.Path, "/") {
			return nil, fmt.Errorf("webhook path must start with /")
		}
	}
	if c.ApprovalTTL == "" {
		c.ApprovalTTL = "1h"
	}
	ttl, err := time.ParseDuration(c.ApprovalTTL)
	if err != nil || ttl <= 0 || ttl > 24*time.Hour {
		return nil, fmt.Errorf("approval_ttl must be positive and at most 24h")
	}
	c.ttl = ttl
	if len(c.Agents) == 0 {
		return nil, fmt.Errorf("at least one agent is required")
	}
	ids := map[string]bool{}
	for j := range c.Agents {
		a := &c.Agents[j]
		if !identifier.MatchString(a.ID) || ids[a.ID] || a.TokenEnv == "" {
			return nil, fmt.Errorf("agents need unique valid IDs and token_env")
		}
		ids[a.ID] = true
		if len(a.Repositories) == 0 || len(a.Actions) == 0 {
			return nil, fmt.Errorf("agent %s needs repositories and actions", a.ID)
		}
		for k, repo := range a.Repositories {
			if !repoPattern.MatchString(repo) {
				return nil, fmt.Errorf("invalid repository %q", repo)
			}
			a.Repositories[k] = strings.ToLower(repo)
		}
		for _, op := range a.Actions {
			if !operations[op] {
				return nil, fmt.Errorf("unsupported action %q", op)
			}
		}
		if a.BranchPrefix == "" {
			a.BranchPrefix = "circuit/"
		}
		if !validRef(strings.TrimSuffix(a.BranchPrefix, "/")) || !strings.HasSuffix(a.BranchPrefix, "/") {
			return nil, fmt.Errorf("branch_prefix must be a valid prefix ending in /")
		}
	}
	env, err := cel.NewEnv(cel.Variable("args", cel.MapType(cel.StringType, cel.DynType)), cel.Variable("action", cel.StringType), cel.Variable("repository", cel.StringType), cel.Variable("agent_id", cel.StringType))
	if err != nil {
		return nil, err
	}
	ids = map[string]bool{}
	for j := range c.Rules {
		rule := &c.Rules[j]
		if !identifier.MatchString(rule.ID) || ids[rule.ID] || len(rule.Actions) == 0 || !rule.Action.IsValid() {
			return nil, fmt.Errorf("invalid rule %q", rule.ID)
		}
		ids[rule.ID] = true
		for _, op := range rule.Actions {
			if !operations[op] {
				return nil, fmt.Errorf("unknown rule action %q", op)
			}
		}
		for k, repo := range rule.Repositories {
			if !repoPattern.MatchString(repo) {
				return nil, fmt.Errorf("invalid rule repository")
			}
			rule.Repositories[k] = strings.ToLower(repo)
		}
		if rule.Condition != "" {
			ast, issues := env.Compile(rule.Condition)
			if issues != nil && issues.Err() != nil {
				return nil, issues.Err()
			}
			if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
				return nil, fmt.Errorf("rule %s condition must be boolean", rule.ID)
			}
			rule.program, err = env.Program(ast, cel.CostLimit(10000))
			if err != nil {
				return nil, err
			}
		}
	}
	ids = map[string]bool{}
	for j := range c.Limits {
		limit := &c.Limits[j]
		if !identifier.MatchString(limit.ID) || ids[limit.ID] || limit.MaxCalls <= 0 || len(limit.Actions) == 0 {
			return nil, fmt.Errorf("invalid limit %q", limit.ID)
		}
		ids[limit.ID] = true
		switch limit.Scope {
		case "global", "agent", "repository", "agent_repository":
		default:
			return nil, fmt.Errorf("invalid budget scope %q", limit.Scope)
		}
		for _, op := range limit.Actions {
			if !operations[op] {
				return nil, fmt.Errorf("unknown limit action %q", op)
			}
		}
		limit.duration, err = time.ParseDuration(limit.Window)
		if err != nil || limit.duration <= 0 {
			return nil, fmt.Errorf("invalid limit window")
		}
	}
	encoded, _ := json.Marshal(c)
	digest := sha256.Sum256(encoded)
	c.digest = hex.EncodeToString(digest[:])
	return &c, nil
}
func (c *Config) agent(id string) *Agent {
	for j := range c.Agents {
		if c.Agents[j].ID == id {
			return &c.Agents[j]
		}
	}
	return nil
}
func (c *Config) evaluate(agentID string, req Request) (config.ActionType, string, error) {
	agent := c.agent(agentID)
	if agent == nil || !member(agent.Repositories, req.Repository) || !member(agent.Actions, req.Operation) {
		return config.ActionDeny, "Agent is not permitted to use this action or repository", nil
	}
	if err := validateScope(agent, req); err != nil {
		return config.ActionDeny, err.Error(), nil
	}
	verdict := config.ActionAllow
	reason := "Within agent scope"
	if req.Operation == "merge_pr" {
		verdict = config.ActionRequireApproval
		reason = "Merge requires approval of the exact head commit"
	}
	for _, rule := range c.Rules {
		if !member(rule.Actions, req.Operation) || (len(rule.Repositories) > 0 && !member(rule.Repositories, req.Repository)) {
			continue
		}
		if rule.program != nil {
			out, _, err := rule.program.Eval(map[string]any{"args": req.Args, "action": req.Operation, "repository": req.Repository, "agent_id": agentID})
			if err != nil {
				return config.ActionDeny, "Policy evaluation failed", err
			}
			match, ok := out.Value().(bool)
			if !ok {
				return config.ActionDeny, "Policy condition must be boolean", fmt.Errorf("non-boolean policy result")
			}
			if !match {
				continue
			}
		}
		if rule.Action == config.ActionDeny {
			return config.ActionDeny, rule.Reason, nil
		}
		if rule.Action == config.ActionRequireApproval {
			verdict = config.ActionRequireApproval
			reason = rule.Reason
		}
	}
	return verdict, reason, nil
}
