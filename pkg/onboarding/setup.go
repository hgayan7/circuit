// Package onboarding builds local BYOK setups using the existing gateway contract.
package onboarding

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/gateway"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Directory, Integration, Preset, Repository, KeyFile, DSNEnv, Workspace string
	Endpoint, PluginTokenFile                                              string
	PluginOperations, PluginReadOnly                                       []string
	AppID, InstallationID                                                  int64
	Port                                                                   int
	Executable                                                             string
}
type Connection struct {
	URL       string `json:"url"`
	TokenFile string `json:"token_file"`
	CACert    string `json:"ca_cert"`
}
type Setup struct {
	Version     int        `json:"version"`
	Integration string     `json:"integration"`
	Preset      string     `json:"preset"`
	Config      string     `json:"config"`
	State       string     `json:"state"`
	Listen      string     `json:"listen"`
	TLSCert     string     `json:"tls_cert"`
	TLSKey      string     `json:"tls_key"`
	Connection  Connection `json:"connection"`
}

func DefaultDirectory() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".circuit-operator/setup"
	}
	return filepath.Join(dir, "circuit", "setup")
}

func canonicalDestination(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	ancestor := absolute
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("setup parent is unavailable")
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	remainder, err := filepath.Rel(ancestor, absolute)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, remainder), nil
}

func BuildConfig(o Options, dir string) (*gateway.Config, error) {
	if o.Preset == "" {
		o.Preset = "read-only"
	}
	if o.Port < 1 || o.Port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535")
	}
	c := &gateway.Config{Name: "agent-actions", ApprovalTTL: "1h"}
	a := gateway.Agent{ID: "agent", TokenFile: filepath.Join(dir, "secrets", "agent-token")}
	for _, role := range []string{"admin", "reviewer", "observer"} {
		c.Operators = append(c.Operators, gateway.OperatorConfig{ID: role, Role: role, TokenFile: filepath.Join(dir, "secrets", role+"-token")})
	}
	var writes []string
	switch o.Integration {
	case "github":
		if o.AppID <= 0 || o.InstallationID <= 0 || o.KeyFile == "" {
			return nil, fmt.Errorf("GitHub requires App ID, installation ID, and a private-key file; create/install an App at https://github.com/settings/apps")
		}
		a.Repositories = []string{o.Repository}
		a.BranchPrefix = "circuit/"
		a.Actions = []string{"read_file", "get_pr"}
		if o.Preset == "pr-author" || o.Preset == "pr-author-with-merge" {
			writes = []string{"create_branch", "put_file", "create_pr"}
			a.Actions = append(a.Actions, writes...)
			if o.Preset == "pr-author-with-merge" {
				a.Actions = append(a.Actions, "merge_pr")
				writes = append(writes, "merge_pr")
			}
		} else if o.Preset != "read-only" {
			return nil, fmt.Errorf("GitHub presets: read-only, pr-author, pr-author-with-merge")
		}
		key, err := filepath.Abs(o.KeyFile)
		if err != nil {
			return nil, err
		}
		c.GitHubApp = &gateway.GitHubAppConfig{AppID: o.AppID, InstallationID: o.InstallationID, PrivateKeyFile: key}
	case "postgres":
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(o.DSNEnv) {
			return nil, fmt.Errorf("PostgreSQL needs --dsn-env naming an environment variable, not the connection string")
		}
		if o.Preset != "read-only" && o.Preset != "review-writes" {
			return nil, fmt.Errorf("PostgreSQL presets: read-only, review-writes")
		}
		c.Databases = []gateway.DatabaseConfig{{ID: "database", Driver: "postgres", DSNEnv: o.DSNEnv, ReadOnly: o.Preset == "read-only", MaxRows: 100, MaxTimeout: "15s"}}
		a.Databases = []string{"database"}
		a.Actions = []string{"query_sql", "list_tables", "describe_table"}
		if o.Preset == "review-writes" {
			writes = []string{"exec_sql"}
			a.Actions = append(a.Actions, writes...)
		}
	case "workspace":
		if o.Preset != "read-only" || o.Workspace == "" {
			return nil, fmt.Errorf("workspace setup requires --workspace and the read-only preset; shell sandboxing is not supplied")
		}
		path, err := filepath.EvalSymlinks(o.Workspace)
		if err != nil {
			return nil, err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		setupPath, err := canonicalDestination(dir)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(path, setupPath)
		if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return nil, fmt.Errorf("setup secrets must be outside the agent workspace; choose an operator-only --out directory")
		}
		c.Workspaces = []gateway.WorkspaceConfig{{ID: "workspace", Path: path, ReadOnly: true, MaxTimeout: "15s"}}
		a.Workspaces = []string{"workspace"}
		a.Actions = []string{"read_file", "list_dir"}
	case "plugin":
		if o.Preset != "read-only" && o.Preset != "review-writes" {
			return nil, fmt.Errorf("plugin presets: read-only, review-writes")
		}
		ops := o.PluginOperations
		if o.Preset == "read-only" {
			ops = o.PluginReadOnly
		}
		if len(ops) == 0 || o.PluginTokenFile == "" {
			return nil, fmt.Errorf("plugin needs declared operations and a separate --plugin-token-file; read-only presets need --read-only-operations")
		}
		path, err := filepath.Abs(o.PluginTokenFile)
		if err != nil {
			return nil, err
		}
		c.CustomTools = []gateway.CustomToolConfig{{ID: "plugin", Name: "BYOK plugin", Endpoint: o.Endpoint, Method: "POST", Protocol: gateway.PluginProtocol, TokenFile: path, Operations: ops, ReadOnlyOperations: o.PluginReadOnly, MaxTimeout: "30s"}}
		a.CustomTools = []string{"plugin"}
		a.Actions = append([]string(nil), ops...)
		if o.Preset == "review-writes" {
			for _, op := range ops {
				if !contains(o.PluginReadOnly, op) {
					writes = append(writes, op)
				}
			}
		}
	default:
		return nil, fmt.Errorf("integration must be github, postgres, workspace, or plugin")
	}
	if len(writes) > 0 {
		c.Rules = []gateway.Rule{{ID: "review-writes", Actions: writes, Action: config.ActionRequireApproval, Reason: "Review the exact change before execution"}}
		c.Limits = append(c.Limits, gateway.Limit{ID: "writes-per-hour", Actions: writes, Scope: "agent", Window: "1h", MaxCalls: 5})
	}
	c.Limits = append(c.Limits, gateway.Limit{ID: "actions-per-hour", Actions: a.Actions, Scope: "agent", Window: "1h", MaxCalls: 100})
	c.Agents = []gateway.Agent{a}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	return gateway.ParseConfig(strings.NewReader(string(data)))
}
func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

// Create never overwrites a setup and keeps generated secrets out of client exports.
func Create(o Options) (*Setup, error) {
	dir, err := canonicalDestination(o.Directory)
	if err != nil {
		return nil, err
	}
	if o.Preset == "" {
		o.Preset = "read-only"
	}
	c, err := BuildConfig(o, dir)
	if err != nil {
		return nil, err
	}
	if err := CheckCredentials(c); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("choose a new setup directory; existing files are never overwritten: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(dir)
		}
	}()
	if err := os.Mkdir(filepath.Join(dir, "secrets"), 0700); err != nil {
		return nil, err
	}
	for _, role := range []string{"agent", "admin", "reviewer", "observer"} {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := write(filepath.Join(dir, "secrets", role+"-token"), []byte(base64.RawURLEncoding.EncodeToString(b)+"\n")); err != nil {
			return nil, err
		}
	}
	s := &Setup{Version: 1, Integration: o.Integration, Preset: o.Preset, Config: filepath.Join(dir, "gateway.yaml"), State: filepath.Join(dir, "gateway.db"), Listen: fmt.Sprintf("127.0.0.1:%d", o.Port), TLSCert: filepath.Join(dir, "secrets", "tls.crt"), TLSKey: filepath.Join(dir, "secrets", "tls.key")}
	s.Connection = Connection{URL: fmt.Sprintf("https://127.0.0.1:%d", o.Port), TokenFile: c.Agents[0].TokenFile, CACert: s.TLSCert}
	if err := certificate(s.TLSCert, s.TLSKey); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := write(s.Config, data); err != nil {
		return nil, err
	}
	for name, value := range map[string]any{"setup.json": s, "agent-connection.json": s.Connection} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := write(filepath.Join(dir, name), data); err != nil {
			return nil, err
		}
	}
	executable := o.Executable
	if executable == "" {
		executable = "circuit"
	}
	client := map[string]any{"mcpServers": map[string]any{"circuit": map[string]any{"command": executable, "args": []string{"connect", "--url", s.Connection.URL, "--token-file", s.Connection.TokenFile, "--ca-cert", s.Connection.CACert}}}}
	data, err = json.MarshalIndent(client, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := write(filepath.Join(dir, "mcp.json"), data); err != nil {
		return nil, err
	}
	complete = true
	return s, nil
}
func write(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func privateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("credential must be a private regular file (chmod 600)")
	}
	return os.ReadFile(path)
}
func CheckCredentials(c *gateway.Config) error {
	if c.GitHubApp != nil {
		data, err := privateFile(c.GitHubApp.PrivateKeyFile)
		if err != nil {
			return err
		}
		if _, err := gateway.ParseRSAPrivateKey(data); err != nil {
			return fmt.Errorf("GitHub key is not a valid RSA PEM")
		}
	}
	for _, d := range c.Databases {
		if strings.TrimSpace(os.Getenv(d.DSNEnv)) == "" {
			return fmt.Errorf("set %s on the gateway host before setup/start; do not give the database credential to the agent", d.DSNEnv)
		}
	}
	for _, p := range c.CustomTools {
		data, err := privateFile(p.TokenFile)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) < 32 {
			return fmt.Errorf("plugin token needs at least 32 characters")
		}
	}
	for _, w := range c.Workspaces {
		info, err := os.Stat(w.Path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("workspace directory must exist")
		}
	}
	return nil
}

func CheckTokens(c *gateway.Config) error {
	var paths []string
	for _, a := range c.Agents {
		paths = append(paths, a.TokenFile)
	}
	for _, o := range c.Operators {
		paths = append(paths, o.TokenFile)
	}
	for _, p := range c.CustomTools {
		paths = append(paths, p.TokenFile)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		data, err := privateFile(path)
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(data))
		if len(token) < 32 || strings.ContainsAny(token, "\r\n") || seen[token] {
			return fmt.Errorf("agent, operator, and plugin tokens must be distinct, at least 32 characters, and single-line")
		}
		seen[token] = true
	}
	return nil
}
func Load(dir string) (*Setup, error) {
	data, err := os.ReadFile(filepath.Join(dir, "setup.json"))
	if err != nil {
		return nil, fmt.Errorf("setup not found; run circuit setup first")
	}
	var s Setup
	if err := json.Unmarshal(data, &s); err != nil || s.Version != 1 {
		return nil, fmt.Errorf("unsupported or invalid setup manifest")
	}
	return &s, nil
}
func certificate(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Circuit local setup"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(30 * 24 * time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := write(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})); err != nil {
		return err
	}
	return write(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
