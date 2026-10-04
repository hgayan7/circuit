package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type SandboxOptions struct {
	Image, Network, Workspace, GatewayURL, Runtime string
	Connection                                     Connection
	Writable                                       bool
	Command                                        []string
}

// SandboxArgs builds a fixed restricted container spec, not arbitrary Docker flags.
func SandboxArgs(o SandboxOptions, staged string) ([]string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`).MatchString(o.Image) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(o.Network) || len(o.Command) == 0 || o.Command[0] == "" || strings.HasPrefix(o.Command[0], "-") {
		return nil, fmt.Errorf("sandbox requires a trusted image, internal network, and an explicit command")
	}
	if o.Runtime != "" && o.Runtime != "runsc" {
		return nil, fmt.Errorf("optional sandbox runtime must be runsc; install/configure gVisor separately")
	}
	u, err := url.Parse(o.GatewayURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("container gateway URL must be an HTTPS origin reachable on the internal network")
	}
	workspace, err := filepath.EvalSymlinks(o.Workspace)
	if err != nil {
		return nil, fmt.Errorf("sandbox workspace must exist")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() || filepath.Dir(workspace) == workspace || strings.Contains(workspace, ",") {
		return nil, fmt.Errorf("sandbox workspace must be a non-root directory without commas")
	}
	for _, secret := range []string{o.Connection.TokenFile, o.Connection.CACert} {
		if secret == "" {
			return nil, fmt.Errorf("sandbox needs an agent credential file and an explicit public CA")
		}
		path, err := filepath.EvalSymlinks(secret)
		if err != nil {
			return nil, fmt.Errorf("sandbox connection file unavailable")
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, fmt.Errorf("connection files must be outside the mounted agent workspace")
		}
	}
	if strings.Contains(staged, ",") {
		return nil, fmt.Errorf("staged secret path must not contain commas")
	}
	args := []string{"run", "--rm", "--interactive", "--init", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "512m", "--cpus", "1", "--user", "10001:10001", "--network", o.Network, "--workdir", "/workspace", "--tmpfs", "/tmp:rw,noexec,nosuid,size=128m,mode=1777"}
	if o.Runtime != "" {
		args = append(args, "--runtime", o.Runtime)
	}
	for _, name := range []string{"agent-token", "ca.crt", "mcp.json"} {
		args = append(args, "--mount", "type=bind,src="+filepath.Join(staged, name)+",dst=/run/circuit/"+name+",readonly")
	}
	mount := "type=bind,src=" + workspace + ",dst=/workspace"
	if !o.Writable {
		mount += ",readonly"
	}
	args = append(args, "--mount", mount)
	args = append(args, "--env", "CIRCUIT_GATEWAY_URL="+o.GatewayURL, "--env", "CIRCUIT_TOKEN_FILE=/run/circuit/agent-token", "--env", "CIRCUIT_CA_CERT=/run/circuit/ca.crt", "--env", "NODE_EXTRA_CA_CERTS=/run/circuit/ca.crt", "--env", "SSL_CERT_FILE=/run/circuit/ca.crt", "--env", "REQUESTS_CA_BUNDLE=/run/circuit/ca.crt", "--env", "CIRCUIT_MCP_CONFIG=/run/circuit/mcp.json", "--entrypoint", o.Command[0], o.Image)
	return append(args, o.Command[1:]...), nil
}

func CheckInternalNetwork(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", name)
	cmd.Stderr = nil
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("Docker network unavailable; use the gateway deployment's agent-only network")
	}
	var networks []struct {
		Internal bool `json:"Internal"`
	}
	if json.Unmarshal(data, &networks) != nil || len(networks) != 1 || !networks[0].Internal {
		return fmt.Errorf("sandbox requires an internal Docker network; bridge/host networks are rejected")
	}
	return nil
}

// StageSandbox mounts only agent connection files. The private host parent is not mounted.
func StageSandbox(o SandboxOptions) (string, error) {
	if _, err := SandboxArgs(o, "/fixture/staged"); err != nil {
		return "", err
	}
	if _, err := Client(o.Connection); err != nil {
		return "", err
	}
	token, err := privateFile(o.Connection.TokenFile)
	if err != nil {
		return "", err
	}
	ca, err := os.ReadFile(o.Connection.CACert)
	if err != nil {
		return "", fmt.Errorf("public CA unavailable")
	}
	dir, err := os.MkdirTemp("", "circuit-agent-secrets-")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(dir)
		}
	}()
	mcpConfig := map[string]any{"mcpServers": map[string]any{"circuit": map[string]any{"command": "circuit", "args": []string{"connect", "--url", o.GatewayURL, "--token-file", "/run/circuit/agent-token", "--mounted-token", "--ca-cert", "/run/circuit/ca.crt"}}}}
	data, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return "", err
	}
	for name, data := range map[string][]byte{"agent-token": token, "ca.crt": ca, "mcp.json": data} {
		path := filepath.Join(dir, name)
		if err := write(path, data); err != nil {
			return "", err
		}
		if err := os.Chmod(path, 0444); err != nil {
			return "", err
		}
	}
	complete = true
	return dir, nil
}
