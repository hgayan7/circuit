package gateway

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func credential(env, path string) (string, error) {
	if path == "" {
		return os.Getenv(env), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open credential file %s", path)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return "", fmt.Errorf("cannot read bounded credential file %s", path)
	}
	return strings.TrimSpace(string(data)), nil
}

// LoadTokens keeps secrets out of configuration and supports mounted secret files.
// Named operators disable the legacy shared administrator credential entirely.
func LoadTokens(c *Config) (Tokens, error) {
	tokens := Tokens{Agents: map[string]string{}, Operators: map[string]string{}}
	if len(c.Operators) == 0 {
		tokens.Admin = os.Getenv(c.AdminTokenEnv)
	}
	for _, a := range c.Agents {
		value, err := credential(a.TokenEnv, a.TokenFile)
		if err != nil {
			return Tokens{}, err
		}
		tokens.Agents[a.ID] = value
	}
	for _, operator := range c.Operators {
		value, err := credential(operator.TokenEnv, operator.TokenFile)
		if err != nil {
			return Tokens{}, err
		}
		tokens.Operators[operator.ID] = value
	}
	if c.Webhook != nil {
		value, err := credential(c.Webhook.SecretEnv, c.Webhook.SecretFile)
		if err != nil {
			return Tokens{}, err
		}
		if len(value) < 32 {
			return Tokens{}, fmt.Errorf("configured webhook needs a secret of at least 32 characters")
		}
		tokens.WebhookSecret = value
	}
	return tokens, nil
}

// ValidateProduction gates the first supported production profile: GitHub only.
// It cannot prove external agent isolation, which deployment must enforce.
func (c *Config) ValidateProduction() error {
	if c.Simulation || c.GitHubApp == nil || len(c.Operators) == 0 ||
		len(c.Workspaces)+len(c.Databases)+len(c.Environments)+len(c.Communications)+len(c.PaymentAccounts)+len(c.CustomTools) != 0 {
		return fmt.Errorf("production profile requires GitHub App authentication, named operators, and only real GitHub targets")
	}
	hasAdmin := false
	for _, operator := range c.Operators {
		hasAdmin = hasAdmin || operator.Role == "admin"
	}
	if !hasAdmin {
		return fmt.Errorf("production profile requires a named admin for recovery")
	}
	for _, agent := range c.Agents {
		if len(agent.Repositories) == 0 || len(agent.Workspaces)+len(agent.Databases)+len(agent.Environments)+len(agent.Channels)+len(agent.Accounts)+len(agent.CustomTools) != 0 {
			return fmt.Errorf("production profile agents must have only GitHub repository targets")
		}
		for _, operation := range agent.Actions {
			switch operation {
			case "read_file", "get_pr", "create_branch", "put_file", "create_pr", "merge_pr", "create_issue", "update_issue":
			default:
				return fmt.Errorf("production profile allows only GitHub actions")
			}
		}
	}
	return nil
}
