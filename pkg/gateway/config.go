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

type WorkspaceConfig struct {
	ID         string `yaml:"id" json:"id"`
	Path       string `yaml:"path" json:"path"`
	ReadOnly   bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
	MaxTimeout string `yaml:"max_timeout,omitempty" json:"max_timeout,omitempty"`
	timeout    time.Duration
}

func (w *WorkspaceConfig) Timeout() time.Duration {
	return w.timeout
}

type DatabaseConfig struct {
	ID          string        `yaml:"id" json:"id"`
	Driver      string        `yaml:"driver,omitempty" json:"driver,omitempty"`
	DSN         string        `yaml:"dsn,omitempty" json:"dsn,omitempty"`
	DSNEnv      string        `yaml:"dsn_env,omitempty" json:"dsn_env,omitempty"`
	ReadOnly    bool          `yaml:"read_only,omitempty" json:"read_only,omitempty"`
	MaxRows     int           `yaml:"max_rows,omitempty" json:"max_rows,omitempty"`
	MaxTimeout  string        `yaml:"max_timeout,omitempty" json:"max_timeout,omitempty"`
	AllowTables []string      `yaml:"allow_tables,omitempty" json:"allow_tables,omitempty"`
	DenyTables  []string      `yaml:"deny_tables,omitempty" json:"deny_tables,omitempty"`
	timeout     time.Duration
}

func (d *DatabaseConfig) Timeout() time.Duration {
	return d.timeout
}

type CloudEnvironmentConfig struct {
	ID              string        `yaml:"id" json:"id"`
	Name            string        `yaml:"name,omitempty" json:"name,omitempty"`
	Production      bool          `yaml:"production,omitempty" json:"production,omitempty"`
	AllowedServices []string      `yaml:"allowed_services,omitempty" json:"allowed_services,omitempty"`
	MaxReplicas     int           `yaml:"max_replicas,omitempty" json:"max_replicas,omitempty"`
	MinReplicas     int           `yaml:"min_replicas,omitempty" json:"min_replicas,omitempty"`
	MaxTimeout      string        `yaml:"max_timeout,omitempty" json:"max_timeout,omitempty"`
	timeout         time.Duration
}

func (e *CloudEnvironmentConfig) Timeout() time.Duration {
	return e.timeout
}

type CommunicationConfig struct {
	ID                         string   `yaml:"id" json:"id"`
	Kind                       string   `yaml:"kind,omitempty" json:"kind,omitempty"`
	AllowedChannels            []string `yaml:"allowed_channels,omitempty" json:"allowed_channels,omitempty"`
	InternalDomains            []string `yaml:"internal_domains,omitempty" json:"internal_domains,omitempty"`
	MaxRecipients              int      `yaml:"max_recipients,omitempty" json:"max_recipients,omitempty"`
	RequireApprovalForExternal bool     `yaml:"require_approval_for_external,omitempty" json:"require_approval_for_external,omitempty"`
}

type PaymentAccountConfig struct {
	ID                        string   `yaml:"id" json:"id"`
	Name                      string   `yaml:"name" json:"name"`
	Currency                  string   `yaml:"currency,omitempty" json:"currency,omitempty"`
	AllowedDestinations       []string `yaml:"allowed_destinations,omitempty" json:"allowed_destinations,omitempty"`
	MaxTransactionAmount      float64  `yaml:"max_transaction_amount,omitempty" json:"max_transaction_amount,omitempty"`
	AutoApprovalThreshold     float64  `yaml:"auto_approval_threshold,omitempty" json:"auto_approval_threshold,omitempty"`
	RequireApprovalForRefunds bool     `yaml:"require_approval_for_refunds,omitempty" json:"require_approval_for_refunds,omitempty"`
	InitialBalance            float64  `yaml:"initial_balance,omitempty" json:"initial_balance,omitempty"`
	MaxTimeout                string   `yaml:"max_timeout,omitempty" json:"max_timeout,omitempty"`
	timeout                   time.Duration
}

func (p *PaymentAccountConfig) Timeout() time.Duration {
	if p.timeout <= 0 {
		return 30 * time.Second
	}
	return p.timeout
}

type Agent struct {
	ID           string   `yaml:"id" json:"id"`
	TokenEnv     string   `yaml:"token_env" json:"token_env"`
	Repositories []string `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Workspaces   []string `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
	Databases    []string `yaml:"databases,omitempty" json:"databases,omitempty"`
	Environments []string `yaml:"environments,omitempty" json:"environments,omitempty"`
	Channels     []string `yaml:"channels,omitempty" json:"channels,omitempty"`
	Accounts     []string `yaml:"accounts,omitempty" json:"accounts,omitempty"`
	Actions      []string `yaml:"actions" json:"actions"`
	BranchPrefix string   `yaml:"branch_prefix,omitempty" json:"branch_prefix,omitempty"`
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
	Name            string                   `yaml:"name" json:"name"`
	AdminTokenEnv   string                   `yaml:"admin_token_env" json:"admin_token_env"`
	GitHubTokenEnv  string                   `yaml:"github_token_env,omitempty" json:"github_token_env,omitempty"`
	GitHubApp       *GitHubAppConfig         `yaml:"github_app,omitempty" json:"github_app,omitempty"`
	Webhook         *WebhookConfig           `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	Workspaces      []WorkspaceConfig        `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
	Databases       []DatabaseConfig         `yaml:"databases,omitempty" json:"databases,omitempty"`
	Environments    []CloudEnvironmentConfig `yaml:"environments,omitempty" json:"environments,omitempty"`
	Communications  []CommunicationConfig    `yaml:"communications,omitempty" json:"communications,omitempty"`
	PaymentAccounts []PaymentAccountConfig   `yaml:"payment_accounts,omitempty" json:"payment_accounts,omitempty"`
	ApprovalTTL     string                   `yaml:"approval_ttl" json:"approval_ttl"`
	Agents          []Agent                  `yaml:"agents" json:"agents"`
	Rules           []Rule                   `yaml:"rules,omitempty" json:"rules,omitempty"`
	Limits          []Limit                  `yaml:"limits,omitempty" json:"limits,omitempty"`
	Safety          config.SafetyConfig      `yaml:"safety,omitempty" json:"safety"`
	ttl             time.Duration
	digest          string
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var operations = map[string]bool{
	"read_file": true, "get_pr": true, "create_branch": true, "put_file": true,
	"create_pr": true, "merge_pr": true, "create_issue": true, "update_issue": true,
	"exec_cmd": true, "write_file": true, "delete_file": true, "list_dir": true,
	"query_sql": true, "exec_sql": true, "list_tables": true, "describe_table": true,
	"deploy_service": true, "rollback_deployment": true, "restart_service": true, "get_deployment_status": true, "scale_service": true,
	"send_message": true, "send_email": true, "create_ticket": true, "update_ticket": true, "publish_document": true,
	"transfer_funds": true, "create_charge": true, "issue_refund": true, "get_balance": true,
}

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
	wsIDs := map[string]bool{}
	for j := range c.Workspaces {
		ws := &c.Workspaces[j]
		if !identifier.MatchString(ws.ID) || wsIDs[ws.ID] {
			return nil, fmt.Errorf("workspaces need unique valid IDs")
		}
		wsIDs[ws.ID] = true
		if ws.Path == "" {
			return nil, fmt.Errorf("workspace %s path is required", ws.ID)
		}
		if ws.MaxTimeout == "" {
			ws.MaxTimeout = "60s"
		}
		to, err := time.ParseDuration(ws.MaxTimeout)
		if err != nil || to <= 0 {
			return nil, fmt.Errorf("invalid max_timeout for workspace %s", ws.ID)
		}
		ws.timeout = to
	}
	dbIDs := map[string]bool{}
	for j := range c.Databases {
		db := &c.Databases[j]
		if !identifier.MatchString(db.ID) || dbIDs[db.ID] {
			return nil, fmt.Errorf("databases need unique valid IDs")
		}
		dbIDs[db.ID] = true
		if db.Driver == "" {
			db.Driver = "postgres"
		}
		if db.MaxRows <= 0 {
			db.MaxRows = 500
		}
		if db.MaxTimeout == "" {
			db.MaxTimeout = "15s"
		}
		to, err := time.ParseDuration(db.MaxTimeout)
		if err != nil || to <= 0 {
			return nil, fmt.Errorf("invalid max_timeout for database %s", db.ID)
		}
		db.timeout = to
	}
	envIDs := map[string]bool{}
	for j := range c.Environments {
		ce := &c.Environments[j]
		if !identifier.MatchString(ce.ID) || envIDs[ce.ID] {
			return nil, fmt.Errorf("environments need unique valid IDs")
		}
		envIDs[ce.ID] = true
		if ce.MaxReplicas <= 0 {
			ce.MaxReplicas = 50
		}
		if ce.MaxTimeout == "" {
			ce.MaxTimeout = "30s"
		}
		to, err := time.ParseDuration(ce.MaxTimeout)
		if err != nil || to <= 0 {
			return nil, fmt.Errorf("invalid max_timeout for environment %s", ce.ID)
		}
		ce.timeout = to
	}
	commIDs := map[string]bool{}
	for j := range c.Communications {
		cm := &c.Communications[j]
		if !identifier.MatchString(cm.ID) || commIDs[cm.ID] {
			return nil, fmt.Errorf("communications need unique valid IDs")
		}
		commIDs[cm.ID] = true
		if cm.MaxRecipients <= 0 {
			cm.MaxRecipients = 50
		}
	}
	payIDs := map[string]bool{}
	for j := range c.PaymentAccounts {
		pa := &c.PaymentAccounts[j]
		if !identifier.MatchString(pa.ID) || payIDs[pa.ID] {
			return nil, fmt.Errorf("payment accounts need unique valid IDs")
		}
		payIDs[pa.ID] = true
		if pa.Currency == "" {
			pa.Currency = "USD"
		}
		pa.Currency = strings.ToUpper(pa.Currency)
		if pa.MaxTimeout == "" {
			pa.MaxTimeout = "30s"
		}
		to, err := time.ParseDuration(pa.MaxTimeout)
		if err != nil || to <= 0 {
			return nil, fmt.Errorf("invalid max_timeout for payment account %s", pa.ID)
		}
		pa.timeout = to
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
		if len(a.Repositories) == 0 && len(a.Workspaces) == 0 && len(a.Databases) == 0 && len(a.Environments) == 0 && len(a.Channels) == 0 && len(a.Accounts) == 0 {
			return nil, fmt.Errorf("agent %s needs repositories, workspaces, databases, environments, channels, or accounts", a.ID)
		}
		if len(a.Actions) == 0 {
			return nil, fmt.Errorf("agent %s needs actions", a.ID)
		}
		for k, repo := range a.Repositories {
			if !repoPattern.MatchString(repo) {
				return nil, fmt.Errorf("invalid repository %q", repo)
			}
			a.Repositories[k] = strings.ToLower(repo)
		}
		for _, ws := range a.Workspaces {
			if !identifier.MatchString(ws) {
				return nil, fmt.Errorf("invalid workspace %q for agent %s", ws, a.ID)
			}
		}
		for _, dbID := range a.Databases {
			if !identifier.MatchString(dbID) {
				return nil, fmt.Errorf("invalid database %q for agent %s", dbID, a.ID)
			}
		}
		for _, envID := range a.Environments {
			if !identifier.MatchString(envID) {
				return nil, fmt.Errorf("invalid environment %q for agent %s", envID, a.ID)
			}
		}
		for _, chID := range a.Channels {
			if !identifier.MatchString(chID) {
				return nil, fmt.Errorf("invalid channel %q for agent %s", chID, a.ID)
			}
		}
		for _, accID := range a.Accounts {
			if !identifier.MatchString(accID) {
				return nil, fmt.Errorf("invalid account %q for agent %s", accID, a.ID)
			}
		}
		for _, op := range a.Actions {
			if !operations[op] {
				return nil, fmt.Errorf("unsupported action %q", op)
			}
		}
		if len(a.Repositories) > 0 && a.BranchPrefix == "" {
			a.BranchPrefix = "circuit/"
		}
		if a.BranchPrefix != "" {
			if !validRef(strings.TrimSuffix(a.BranchPrefix, "/")) || !strings.HasSuffix(a.BranchPrefix, "/") {
				return nil, fmt.Errorf("branch_prefix must be a valid prefix ending in /")
			}
		}
	}
	env, err := cel.NewEnv(
		cel.Variable("args", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("action", cel.StringType),
		cel.Variable("repository", cel.StringType),
		cel.Variable("workspace", cel.StringType),
		cel.Variable("database", cel.StringType),
		cel.Variable("environment", cel.StringType),
		cel.Variable("channel", cel.StringType),
		cel.Variable("account", cel.StringType),
		cel.Variable("agent_id", cel.StringType),
	)
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
		case "global", "agent", "repository", "agent_repository", "workspace", "agent_workspace", "database", "agent_database", "environment", "agent_environment", "channel", "agent_channel", "account", "agent_account":
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
	if agent == nil || !member(agent.Actions, req.Operation) {
		return config.ActionDeny, "Agent is not permitted to use this action", nil
	}
	if isPaymentOperation(req) {
		accID := req.Account
		if accID == "" {
			accID = "default"
		}
		if !member(agent.Accounts, accID) {
			return config.ActionDeny, fmt.Sprintf("Agent is not permitted to access payment account %q", accID), nil
		}
	} else if isCommOperation(req) {
		chID := req.Channel
		if chID == "" {
			chID = "default"
		}
		if !member(agent.Channels, chID) {
			return config.ActionDeny, fmt.Sprintf("Agent is not permitted to access communication target %q", chID), nil
		}
	} else if isCloudOperation(req) {
		envID := req.Environment
		if envID == "" {
			envID = "default"
		}
		if !member(agent.Environments, envID) {
			return config.ActionDeny, fmt.Sprintf("Agent is not permitted to access cloud environment %q", envID), nil
		}
	} else if isDatabaseOperation(req) {
		dbID := req.Database
		if dbID == "" {
			dbID = "default"
		}
		if !member(agent.Databases, dbID) {
			return config.ActionDeny, fmt.Sprintf("Agent is not permitted to access database %q", dbID), nil
		}
	} else if isWorkspaceOperation(req) {
		wsID := req.Workspace
		if wsID == "" {
			wsID = "default"
		}
		if !member(agent.Workspaces, wsID) {
			return config.ActionDeny, fmt.Sprintf("Agent is not permitted to access workspace %q", wsID), nil
		}
	} else {
		if !member(agent.Repositories, req.Repository) {
			return config.ActionDeny, "Agent is not permitted to access this repository", nil
		}
	}
	if err := validateScope(agent, req); err != nil {
		return config.ActionDeny, err.Error(), nil
	}
	verdict := config.ActionAllow
	reason := "Within agent scope"
	if req.Operation == "merge_pr" {
		verdict = config.ActionRequireApproval
		reason = "Merge requires approval of the exact head commit"
	} else if req.Operation == "delete_file" {
		verdict = config.ActionRequireApproval
		reason = "Deleting files requires operator approval"
	} else if req.Operation == "write_file" {
		if ow, ok := req.Args["overwrite"].(bool); ok && ow {
			verdict = config.ActionRequireApproval
			reason = "Overwriting existing files requires operator approval"
		}
	} else if req.Operation == "exec_cmd" {
		cmdStr := text(req.Args, "command")
		if isDestructive, r := IsDestructiveCommand(cmdStr); isDestructive {
			verdict = config.ActionRequireApproval
			reason = r
		}
	} else if req.Operation == "exec_sql" {
		queryStr := text(req.Args, "query")
		if analysis, err := AnalyzeSQL(queryStr); err == nil && analysis.IsDestructive {
			verdict = config.ActionRequireApproval
			reason = analysis.Reason
		}
	} else if isCloudOperation(req) {
		envID := req.Environment
		if envID == "" {
			envID = "default"
		}
		for _, e := range c.Environments {
			if e.ID == envID && e.Production {
				verdict = config.ActionRequireApproval
				reason = fmt.Sprintf("All actions in production environment %q require operator approval", envID)
				break
			}
		}
		if req.Operation == "rollback_deployment" {
			verdict = config.ActionRequireApproval
			reason = "Deployment rollback requires operator approval"
		} else if req.Operation == "scale_service" {
			if r, ok := req.Args["replicas"]; ok {
				var reps int
				switch v := r.(type) {
				case float64:
					reps = int(v)
				case int:
					reps = v
				}
				if reps == 0 {
					verdict = config.ActionRequireApproval
					reason = "Scaling service to 0 replicas halts traffic and requires operator approval"
				}
			}
		}
	} else if isCommOperation(req) {
		chID := req.Channel
		if chID == "" {
			chID = "default"
		}
		if req.Operation == "publish_document" {
			verdict = config.ActionRequireApproval
			reason = "Publishing documents broadly requires operator approval"
		} else if req.Operation == "send_message" {
			msg := text(req.Args, "message")
			if strings.Contains(msg, "@channel") || strings.Contains(msg, "@here") || strings.Contains(msg, "@everyone") {
				verdict = config.ActionRequireApproval
				reason = "Broadcast channel mentions (@channel/@here/@everyone) require operator approval"
			}
		} else if req.Operation == "send_email" {
			for _, comm := range c.Communications {
				if comm.ID == chID && comm.RequireApprovalForExternal {
					toRaw := req.Args["to"]
					var recs []string
					switch v := toRaw.(type) {
					case string:
						recs = strings.Split(v, ",")
					case []any:
						for _, item := range v {
							if s, ok := item.(string); ok {
								recs = append(recs, s)
							}
						}
					case []string:
						recs = v
					}
					target := &CommTarget{InternalDomains: comm.InternalDomains}
					if target.HasExternalRecipient(recs) {
						verdict = config.ActionRequireApproval
						reason = "Sending email to external domains requires operator approval"
					}
				}
			}
		}
	} else if isPaymentOperation(req) {
		accID := req.Account
		if accID == "" {
			accID = "default"
		}
		var targetAcc *PaymentAccountConfig
		for j := range c.PaymentAccounts {
			if c.PaymentAccounts[j].ID == accID {
				targetAcc = &c.PaymentAccounts[j]
				break
			}
		}
		amount := floatVal(req.Args, "amount")
		if targetAcc != nil {
			if targetAcc.MaxTransactionAmount > 0 && amount > targetAcc.MaxTransactionAmount {
				return config.ActionDeny, fmt.Sprintf("Transaction amount %.2f exceeds maximum permitted transaction amount of %.2f", amount, targetAcc.MaxTransactionAmount), nil
			}
			if req.Operation == "transfer_funds" {
				if targetAcc.AutoApprovalThreshold > 0 {
					if amount > targetAcc.AutoApprovalThreshold {
						verdict = config.ActionRequireApproval
						reason = fmt.Sprintf("Fund transfer of %.2f exceeds auto-approval threshold of %.2f", amount, targetAcc.AutoApprovalThreshold)
					}
				} else {
					verdict = config.ActionRequireApproval
					reason = "All fund transfers require operator approval"
				}
			} else if req.Operation == "issue_refund" {
				if targetAcc.RequireApprovalForRefunds {
					verdict = config.ActionRequireApproval
					reason = "Refunds require operator approval"
				} else if targetAcc.AutoApprovalThreshold > 0 && amount > targetAcc.AutoApprovalThreshold {
					verdict = config.ActionRequireApproval
					reason = fmt.Sprintf("Refund amount of %.2f exceeds auto-approval threshold of %.2f", amount, targetAcc.AutoApprovalThreshold)
				}
			} else if req.Operation == "create_charge" {
				if targetAcc.AutoApprovalThreshold > 0 && amount > targetAcc.AutoApprovalThreshold {
					verdict = config.ActionRequireApproval
					reason = fmt.Sprintf("Charge amount of %.2f exceeds auto-approval threshold of %.2f", amount, targetAcc.AutoApprovalThreshold)
				}
			}
		} else {
			if req.Operation == "transfer_funds" || req.Operation == "issue_refund" {
				verdict = config.ActionRequireApproval
				reason = "Financial transaction requires operator approval"
			}
		}
	}
	for _, rule := range c.Rules {
		if !member(rule.Actions, req.Operation) || (len(rule.Repositories) > 0 && !member(rule.Repositories, req.Repository)) {
			continue
		}
		if rule.program != nil {
			out, _, err := rule.program.Eval(map[string]any{
				"args":        req.Args,
				"action":      req.Operation,
				"repository":  req.Repository,
				"workspace":   req.Workspace,
				"database":    req.Database,
				"environment": req.Environment,
				"channel":     req.Channel,
				"account":     req.Account,
				"agent_id":    agentID,
			})
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
