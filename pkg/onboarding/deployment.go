package onboarding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hgayan7/circuit/pkg/gateway"
	"gopkg.in/yaml.v3"
)

type Deployment struct {
	Project string `json:"project"`
	Compose string `json:"compose"`
	Network string `json:"network"`
	Gateway string `json:"gateway"`
	Digest  string `json:"digest"`
}

func validImage(image string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`).MatchString(image)
}

// PrepareDeployment exports only individual gateway mounts. Agents never mount this directory.
func PrepareDeployment(dir, image string) (*Deployment, error) {
	if !validImage(image) {
		return nil, fmt.Errorf("a trusted gateway image is required")
	}
	s, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.State); err == nil {
		return nil, fmt.Errorf("existing host gateway state found; migrate a verified backup with the restore reconciliation barrier before Docker deployment; refusing to start a second empty action store")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	c, err := gateway.LoadConfig(s.Config)
	if err != nil {
		return nil, err
	}
	if err := c.ValidateProduction(); err != nil {
		return nil, err
	}
	if err := CheckCredentials(c); err != nil {
		return nil, err
	}
	if err := CheckTokens(c); err != nil {
		return nil, err
	}
	dir, err = canonicalDestination(dir)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(dir))
	project := "circuit-" + hex.EncodeToString(sum[:6])
	output := filepath.Join(dir, "deployment")
	d := &Deployment{Project: project, Compose: filepath.Join(output, "compose.yaml"), Network: project + "_agents", Gateway: project + "-gateway"}
	files := map[string][]byte{}
	mounts := []any{}
	add := func(path, name string) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("deployment mount unavailable: %s", name)
		}
		files[name] = data
		destination := "/run/circuit/" + name
		mounts = append(mounts, map[string]any{"type": "bind", "source": filepath.Join(output, name), "target": destination, "read_only": true})
		return destination, nil
	}
	for i := range c.Agents {
		c.Agents[i].TokenFile, err = add(c.Agents[i].TokenFile, fmt.Sprintf("agent-%d.token", i))
		if err != nil {
			return nil, err
		}
	}
	for i := range c.Operators {
		c.Operators[i].TokenFile, err = add(c.Operators[i].TokenFile, fmt.Sprintf("operator-%d.token", i))
		if err != nil {
			return nil, err
		}
	}
	if c.GitHubApp != nil {
		c.GitHubApp.PrivateKeyFile, err = add(c.GitHubApp.PrivateKeyFile, "github.pem")
		if err != nil {
			return nil, err
		}
	}
	for i := range c.CustomTools {
		tool := &c.CustomTools[i]
		tool.TokenFile, err = add(tool.TokenFile, fmt.Sprintf("upstream-%d.token", i))
		if err != nil {
			return nil, err
		}
		if tool.CACert != "" {
			tool.CACert, err = add(tool.CACert, fmt.Sprintf("upstream-%d.crt", i))
			if err != nil {
				return nil, err
			}
		}
	}
	if c.Webhook != nil {
		return nil, fmt.Errorf("generated isolated deployment does not expose webhooks; use the operator deployment for signed delivery")
	}
	if _, err := add(s.TLSCert, "tls.crt"); err != nil {
		return nil, err
	}
	if _, err := add(s.TLSKey, "tls.key"); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	files["gateway.yaml"] = data
	mounts = append(mounts, map[string]any{"type": "bind", "source": filepath.Join(output, "gateway.yaml"), "target": "/etc/circuit/gateway.yaml", "read_only": true}, map[string]any{"type": "volume", "source": "state", "target": "/var/lib/circuit"})
	_, port, err := net.SplitHostPort(s.Listen)
	if err != nil {
		return nil, err
	}
	service := map[string]any{
		"image": image, "container_name": d.Gateway, "restart": "unless-stopped",
		"command":   []string{"gateway", "serve", "--production", "--config", "/etc/circuit/gateway.yaml", "--data", "/var/lib/circuit/gateway.db", "--listen", "0.0.0.0:8443", "--tls-cert", "/run/circuit/tls.crt", "--tls-key", "/run/circuit/tls.key"},
		"read_only": true, "user": "10001:10001", "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"},
		"pids_limit": 128, "mem_limit": "512m", "cpus": 1, "tmpfs": []string{"/tmp:rw,noexec,nosuid,size=64m"},
		"ports": []string{"127.0.0.1:" + port + ":8443"}, "volumes": mounts,
		"networks":    []string{"agents", "upstream"},
		"labels":      map[string]string{"circuit.project": project, "circuit.role": "gateway"},
		"healthcheck": map[string]any{"test": []string{"CMD", "circuit", "gateway", "healthcheck", "--url", "https://127.0.0.1:8443", "--ca-cert", "/run/circuit/tls.crt"}, "interval": "5s", "timeout": "3s", "retries": 10},
	}
	compose := map[string]any{"services": map[string]any{"gateway": service}, "networks": map[string]any{"agents": map[string]any{"name": d.Network, "internal": true}, "upstream": map[string]any{}}, "volumes": map[string]any{"state": map[string]any{}}}
	files["compose.yaml"], err = yaml.Marshal(compose)
	if err != nil {
		return nil, err
	}
	serialized, _ := json.Marshal(files)
	digest := sha256.Sum256(serialized)
	d.Digest = hex.EncodeToString(digest[:])
	if existing, err := LoadDeployment(dir); err == nil {
		if existing.Digest != d.Digest {
			return nil, fmt.Errorf("deployment configuration changed; stop it with circuit down, then remove only the generated deployment directory and run circuit up again (state volume is retained)")
		}
		return existing, nil
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return nil, fmt.Errorf("deployment directory must be new or contain a valid deployment manifest: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(output)
		}
	}()
	for name, data := range files {
		path := filepath.Join(output, name)
		if err := write(path, data); err != nil {
			return nil, err
		}
		// The private parent is never mounted; Docker's non-root gateway needs read access.
		if err := os.Chmod(path, 0444); err != nil {
			return nil, err
		}
	}
	data, _ = json.MarshalIndent(d, "", "  ")
	if err := write(filepath.Join(output, "deployment.json"), data); err != nil {
		return nil, err
	}
	complete = true
	return d, nil
}

func LoadDeployment(dir string) (*Deployment, error) {
	data, err := os.ReadFile(filepath.Join(dir, "deployment", "deployment.json"))
	if err != nil {
		return nil, fmt.Errorf("isolated deployment not found; run circuit up --dir first")
	}
	var d Deployment
	if json.Unmarshal(data, &d) != nil || d.Project == "" || d.Compose == "" || d.Network == "" || d.Gateway == "" {
		return nil, fmt.Errorf("invalid deployment manifest")
	}
	return &d, nil
}

func CheckWorkspaceIsolation(s *Setup, workspace string) error {
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	c, err := gateway.LoadConfig(s.Config)
	if err != nil {
		return err
	}
	paths := []string{s.Config, s.TLSKey, s.Connection.TokenFile}
	if c.GitHubApp != nil {
		paths = append(paths, c.GitHubApp.PrivateKeyFile)
	}
	for _, operator := range c.Operators {
		paths = append(paths, operator.TokenFile)
	}
	for _, tool := range c.CustomTools {
		paths = append(paths, tool.TokenFile)
	}
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("known credential/configuration file unavailable")
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return fmt.Errorf("agent workspace contains operator configuration or a known provider/agent credential; use a separate clean workspace")
		}
	}
	return nil
}
