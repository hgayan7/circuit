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
	e := &countingExecutor{}
	s, _ := testService(t, testConfig(t, ""), e)

	// Seed several uncertain actions (e.g. from network timeouts or recovered after crash)
	now := time.Now().UTC()
	makeUncertainAction := func(op string, args map[string]any) *Action {
		req := Request{Operation: op, Repository: "acme/app", Args: args}
		a := &Action{
			ID:           newID(),
			AgentID:      "agent",
			Request:      req,
			Digest:       hashRequest("agent", req),
			PolicyDigest: s.cfg.digest,
			State:        "uncertain",
			Reason:       "Network timeout during execution; outcome requires reconciliation",
			CreatedAt:    now,
			ExpiresAt:    now.Add(time.Hour),
		}
		err := s.store.db.Update(func(tx *bolt.Tx) error {
			return saveAction(tx, a, "test")
		})
		require.NoError(t, err)
		return a
	}

	aMerge := makeUncertainAction("merge_pr", map[string]any{"number": float64(42), "sha": DemoSHA})
	aCreatePR := makeUncertainAction("create_pr", map[string]any{"head": "circuit/fix-bug", "base": "main", "title": "Fix critical bug"})
	aCreateBranch := makeUncertainAction("create_branch", map[string]any{"branch": "circuit/new-feature", "sha": DemoSHA})
	aPutFile := makeUncertainAction("put_file", map[string]any{"branch": "circuit/new-feature", "path": "pkg/fix.go", "content": "Y29udGVudA==", "message": "Add fix"})
	aCreateIssue := makeUncertainAction("create_issue", map[string]any{"title": "Found memory leak", "body": "Details here"})
	aUpdateIssue := makeUncertainAction("update_issue", map[string]any{"number": float64(10), "state": "closed"})

	// 1. Reconcile merge_pr via pull_request closed/merged event
	mergePayload := []byte(`{
		"action": "closed",
		"repository": {"full_name": "acme/app"},
		"pull_request": {
			"number": 42,
			"merged": true,
			"merge_commit_sha": "` + DemoSHA + `"
		}
	}`)
	res, err := s.ReconcileWebhook(context.Background(), "pull_request", mergePayload)
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	require.Contains(t, res.MatchedActions, aMerge.ID)

	updatedMerge, err := s.store.Get(aMerge.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updatedMerge.State)
	require.Contains(t, updatedMerge.Reason, "Reconciled via webhook: PR #42 confirmed merged")
	events, err := s.store.Events(aMerge.ID)
	require.NoError(t, err)
	require.Equal(t, "webhook", events[len(events)-1].Actor)

	// 2. Reconcile create_pr via pull_request opened event
	prPayload := []byte(`{
		"action": "opened",
		"repository": {"full_name": "acme/app"},
		"pull_request": {
			"number": 43,
			"head": {"ref": "circuit/fix-bug"},
			"base": {"ref": "main"},
			"title": "Fix critical bug",
			"html_url": "https://github.com/acme/app/pull/43"
		}
	}`)
	res, err = s.ReconcileWebhook(context.Background(), "pull_request", prPayload)
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	require.Contains(t, res.MatchedActions, aCreatePR.ID)

	updatedPR, err := s.store.Get(aCreatePR.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updatedPR.State)
	require.Contains(t, updatedPR.Reason, "PR #43 confirmed opened")

	// 3. Reconcile create_branch via create event
	branchPayload := []byte(`{
		"ref_type": "branch",
		"ref": "circuit/new-feature",
		"repository": {"full_name": "acme/app"}
	}`)
	res, err = s.ReconcileWebhook(context.Background(), "create", branchPayload)
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	require.Contains(t, res.MatchedActions, aCreateBranch.ID)

	updatedBranch, err := s.store.Get(aCreateBranch.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updatedBranch.State)

	// 4. Reconcile put_file via push event containing commit with file path
	pushPayload := []byte(`{
		"ref": "refs/heads/circuit/new-feature",
		"repository": {"full_name": "acme/app"},
		"commits": [
			{
				"id": "1111111111111111111111111111111111111111",
				"added": ["pkg/fix.go"],
				"modified": []
			}
		]
	}`)
	res, err = s.ReconcileWebhook(context.Background(), "push", pushPayload)
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	require.Contains(t, res.MatchedActions, aPutFile.ID)

	updatedFile, err := s.store.Get(aPutFile.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updatedFile.State)

	// 5. Reconcile create_issue via issues opened event
	issueOpenPayload := []byte(`{
		"action": "opened",
		"repository": {"full_name": "acme/app"},
		"issue": {
			"number": 88,
			"title": "Found memory leak",
			"state": "open",
			"html_url": "https://github.com/acme/app/issues/88"
		}
	}`)
	res, err = s.ReconcileWebhook(context.Background(), "issues", issueOpenPayload)
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	require.Contains(t, res.MatchedActions, aCreateIssue.ID)

	updatedIssue, err := s.store.Get(aCreateIssue.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updatedIssue.State)

	// 6. Reconcile update_issue via issues closed event
	issueClosePayload := []byte(`{
		"action": "closed",
		"repository": {"full_name": "acme/app"},
		"issue": {
			"number": 10,
			"state": "closed"
		}
	}`)
	res, err = s.ReconcileWebhook(context.Background(), "issues", issueClosePayload)
	require.NoError(t, err)
	require.Equal(t, 1, res.Reconciled)
	require.Contains(t, res.MatchedActions, aUpdateIssue.ID)

	updatedIssue10, err := s.store.Get(aUpdateIssue.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", updatedIssue10.State)

	// 7. Non-matching repository returns 0 reconciled
	otherRepoPayload := []byte(`{
		"action": "closed",
		"repository": {"full_name": "other/repo"},
		"pull_request": {"number": 999, "merged": true}
	}`)
	res, err = s.ReconcileWebhook(context.Background(), "pull_request", otherRepoPayload)
	require.NoError(t, err)
	require.Equal(t, 0, res.Reconciled)
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
