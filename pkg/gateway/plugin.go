package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const PluginProtocol = "circuit-plugin-v1"

type executionIDKey struct{}

// PluginCall carries only an exact approved request and a durable action ID.
// Plugins must deduplicate writes by ActionID, not create their own approvals.
type PluginCall struct {
	Protocol string  `json:"protocol"`
	ActionID string  `json:"action_id"`
	Request  Request `json:"request"`
}
type PluginResult struct {
	Protocol string  `json:"protocol"`
	ActionID string  `json:"action_id"`
	Outcome  Outcome `json:"outcome"`
}

func validatePluginConfig(c CustomToolConfig) error {
	if c.Protocol == "" {
		if c.TokenEnv != "" || c.TokenFile != "" || len(c.ReadOnlyOperations) > 0 {
			return fmt.Errorf("plugin fields require a protocol")
		}
		return nil
	}
	if c.Protocol != PluginProtocol {
		return fmt.Errorf("unsupported plugin protocol")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return fmt.Errorf("plugin requires a fixed HTTPS endpoint (HTTP only on loopback)")
	}
	if c.Method != "POST" || len(c.Headers) != 0 || (c.TokenEnv == "") == (c.TokenFile == "") || len(c.Operations) == 0 || c.Timeout() > 30*time.Second {
		return fmt.Errorf("plugin requires POST, no inline headers, one credential source, declared operations, and at most 30s timeout")
	}
	seen := map[string]bool{}
	for _, op := range c.Operations {
		if !identifier.MatchString(op) || operations[op] || seen[op] {
			return fmt.Errorf("plugin operations must be unique and cannot shadow built-in operations")
		}
		seen[op] = true
	}
	for _, op := range c.ReadOnlyOperations {
		if !seen[op] {
			return fmt.Errorf("read-only operation must be declared")
		}
	}
	return nil
}

func (t *CustomToolTarget) ConfigurePlugin(c CustomToolConfig) error {
	if c.Protocol == "" {
		return nil
	}
	if err := validatePluginConfig(c); err != nil {
		return err
	}
	token, err := credential(c.TokenEnv, c.TokenFile)
	if err != nil {
		return err
	}
	if len(token) < 32 {
		return fmt.Errorf("plugin credential must have at least 32 characters")
	}
	c.Operations = append([]string(nil), c.Operations...)
	c.ReadOnlyOperations = append([]string(nil), c.ReadOnlyOperations...)
	t.plugin = &c
	t.pluginToken = token
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	t.httpClient = &http.Client{Timeout: c.Timeout(), Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return nil
}

func (e *CustomToolExecutor) executePlugin(ctx context.Context, r Request) Outcome {
	c := e.target.plugin
	mutating := !member(c.ReadOnlyOperations, r.Operation)
	id, _ := ctx.Value(executionIDKey{}).(string)
	if id == "" || r.CustomTool != c.ID || !member(c.Operations, r.Operation) {
		return Outcome{Error: "Plugin dispatch lacks approved action context or scope"}
	}
	body, err := json.Marshal(PluginCall{Protocol: PluginProtocol, ActionID: id, Request: r})
	if err != nil {
		return Outcome{Error: "Cannot encode plugin request"}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Outcome{Error: "Invalid plugin endpoint"}
	}
	req.Header.Set("Authorization", "Bearer "+e.target.pluginToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", id)
	response, err := e.target.httpClient.Do(req)
	if err != nil {
		return Outcome{Error: "Plugin outcome unavailable", Uncertain: mutating}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	var result PluginResult
	if err != nil || len(data) > 4<<20 || response.StatusCode != 200 || json.Unmarshal(data, &result) != nil || result.Protocol != PluginProtocol || result.ActionID != id {
		return Outcome{Status: response.StatusCode, Error: "Plugin response cannot establish an exact outcome", Uncertain: mutating}
	}
	if result.Outcome.Error == "" && (result.Outcome.Status < 200 || result.Outcome.Status >= 300) {
		return Outcome{Error: "Plugin returned an inconsistent outcome", Uncertain: mutating}
	}
	return result.Outcome
}
