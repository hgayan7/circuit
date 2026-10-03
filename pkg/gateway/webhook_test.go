package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func signWebhookPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookSignatureVerification(t *testing.T) {
	secret := "my-secret-webhook-key"
	payload := []byte(`{"action":"opened","repository":{"full_name":"acme/app"}}`)
	validSig := signWebhookPayload(secret, payload)

	require.True(t, VerifyWebhookSignature(secret, validSig, payload))

	// Wrong signature
	require.False(t, VerifyWebhookSignature(secret, "sha256=0000000000000000000000000000000000000000000000000000000000000000", payload))

	// Wrong payload
	require.False(t, VerifyWebhookSignature(secret, validSig, []byte(`{"tampered":true}`)))

	// Missing prefix
	require.False(t, VerifyWebhookSignature(secret, strings.TrimPrefix(validSig, "sha256="), payload))

	// Empty inputs
	require.False(t, VerifyWebhookSignature("", validSig, payload))
	require.False(t, VerifyWebhookSignature(secret, "", payload))
	require.False(t, VerifyWebhookSignature(secret, "sha256=invalid-hex", payload))
}

func TestWebhookHTTPEndpoint(t *testing.T) {
	const secret = "webhook-test-secret"
	cfg := testConfig(t, `webhook:
  secret_env: GITHUB_WEBHOOK_SECRET
  path: /webhooks/github
`)
	e := &countingExecutor{}
	s, _ := testService(t, cfg, e)
	handler, err := NewHTTPHandler(s, Tokens{
		Admin:         adminToken,
		Agents:        map[string]string{"agent": agentToken},
		WebhookSecret: secret,
	})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()

	// 1. Missing signature
	req, _ := http.NewRequest("POST", server.URL+"/webhooks/github", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 2. Ping event with valid signature
	pingBody := []byte(`{"zen":"Responsive is better than fast."}`)
	req, _ = http.NewRequest("POST", server.URL+"/webhooks/github", bytes.NewReader(pingBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", signWebhookPayload(secret, pingBody))
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var pingResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pingResp))
	require.Equal(t, "pong", pingResp["message"])

	// 3. Webhook secret not configured returns 503
	handlerNoSecret, err := NewHTTPHandler(s, Tokens{
		Admin:  adminToken,
		Agents: map[string]string{"agent": agentToken},
	})
	require.NoError(t, err)
	serverNoSecret := httptest.NewServer(handlerNoSecret)
	defer serverNoSecret.Close()

	req, _ = http.NewRequest("POST", serverNoSecret.URL+"/webhooks/github", bytes.NewReader(pingBody))
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestWebhookReconciliationWorkflow(t *testing.T) {
	s, _ := testService(t, testConfig(t, ""), &countingExecutor{})
	req := Request{Operation: "merge_pr", Repository: "acme/app", Args: map[string]any{"number": 42, "sha": DemoSHA}}
	a := &Action{ID: newID(), AgentID: "agent", Request: req, Digest: hashRequest("agent", req), PolicyDigest: s.cfg.digest, State: "uncertain", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, s.store.db.Update(func(tx *bolt.Tx) error { return saveAction(tx, a, "test") }))
	payload := func(sha string) []byte {
		return []byte(`{"action":"closed","repository":{"full_name":"acme/app"},"pull_request":{"number":42,"merged":true,"merge_commit_sha":"` + DemoSHA + `","head":{"sha":"` + sha + `"}}}`)
	}
	res, err := s.ReconcileWebhook(context.Background(), "pull_request", payload(strings.Repeat("b", 40)))
	require.NoError(t, err)
	require.Zero(t, res.Reconciled)
	res, err = s.ReconcileWebhook(context.Background(), "pull_request", payload(DemoSHA))
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	updated, err := s.store.Get(a.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updated.State)
	events, err := s.store.Events(a.ID)
	require.NoError(t, err)
	require.Equal(t, "webhook", events[len(events)-1].Actor)
	res, err = s.ReconcileWebhook(context.Background(), "pull_request", payload(DemoSHA))
	require.NoError(t, err)
	require.Zero(t, res.Reconciled)
	for _, op := range []string{"put_file", "create_issue", "create_pr", "update_issue", "create_branch"} {
		req.Operation = op
		a.ID = newID()
		a.Request = req
		a.Digest = hashRequest("agent", req)
		a.State = "uncertain"
		require.NoError(t, s.store.db.Update(func(tx *bolt.Tx) error { return saveAction(tx, a, "test") }))
		res, err = s.ReconcileWebhook(context.Background(), "pull_request", payload(DemoSHA))
		require.NoError(t, err)
		require.Zero(t, res.Reconciled)
		updated, err = s.store.Get(a.ID)
		require.NoError(t, err)
		require.Equal(t, "uncertain", updated.State)
	}
}

func TestConfigGitHubAppAndWebhookValidation(t *testing.T) {
	// 1. Valid GitHub App and Webhook config
	validYAML := `name: test-app
github_app:
  app_id: 12345
  private_key_env: GITHUB_APP_PRIVATE_KEY
  installation_id: 67890
webhook:
  secret_env: GITHUB_WEBHOOK_SECRET
  path: /custom/webhook
agents:
- id: agent
  token_env: AGENT_TOKEN
  repositories: [acme/app]
  actions: [create_pr]
`
	cfg, err := ParseConfig(strings.NewReader(validYAML))
	require.NoError(t, err)
	require.NotNil(t, cfg.GitHubApp)
	require.EqualValues(t, 12345, cfg.GitHubApp.AppID)
	require.Equal(t, "GITHUB_APP_PRIVATE_KEY", cfg.GitHubApp.PrivateKeyEnv)
	require.EqualValues(t, 67890, cfg.GitHubApp.InstallationID)
	require.NotNil(t, cfg.Webhook)
	require.Equal(t, "/custom/webhook", cfg.Webhook.Path)

	// 2. Invalid App ID
	invalidAppID := strings.Replace(validYAML, "app_id: 12345", "app_id: -5", 1)
	_, err = ParseConfig(strings.NewReader(invalidAppID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "app_id must be a positive integer")

	// 3. Missing private key config
	noKey := strings.Replace(validYAML, "private_key_env: GITHUB_APP_PRIVATE_KEY", "", 1)
	_, err = ParseConfig(strings.NewReader(noKey))
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires either private_key_env or private_key_file")

	// 4. Invalid Webhook path (does not start with /)
	invalidPath := strings.Replace(validYAML, "path: /custom/webhook", "path: custom/webhook", 1)
	_, err = ParseConfig(strings.NewReader(invalidPath))
	require.Error(t, err)
	require.Contains(t, err.Error(), "webhook path must start with /")
}
