package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

const reviewerToken = "reviewer-token-fixture-only-32-characters"
const observerToken = "observer-token-fixture-only-32-characters"

func operatorConfig(t *testing.T) *Config {
	return testConfig(t, `operators:
- id: alice
  role: admin
  token_env: ALICE_TOKEN
- id: bob
  role: reviewer
  token_env: BOB_TOKEN
- id: audit
  role: observer
  token_env: AUDIT_TOKEN
`)
}

func TestNamedOperatorRolesAndAudit(t *testing.T) {
	s, _ := testService(t, operatorConfig(t), &countingExecutor{})
	h, err := NewHTTPHandler(s, Tokens{Admin: "legacy-must-be-disabled-32-characters", Agents: map[string]string{"agent": agentToken}, Operators: map[string]string{"alice": adminToken, "bob": reviewerToken, "audit": observerToken}})
	require.NoError(t, err)
	server := httptest.NewServer(h)
	defer server.Close()
	status, _ := callHTTP(t, server.URL+"/admin/actions", "GET", "legacy-must-be-disabled-32-characters", "", nil)
	require.Equal(t, 401, status)
	status, data := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "review", mergeRequest())
	require.Equal(t, 202, status)
	var a Action
	require.NoError(t, json.Unmarshal(data, &a))
	decision := map[string]string{"digest": a.Digest, "decision": "approve"}
	status, _ = callHTTP(t, server.URL+"/admin/actions/"+a.ID+"/decision", "POST", observerToken, "", decision)
	require.Equal(t, 403, status)
	status, _ = callHTTP(t, server.URL+"/admin/actions/"+a.ID+"/reconcile", "POST", reviewerToken, "", nil)
	require.Equal(t, 403, status)
	status, data = callHTTP(t, server.URL+"/admin/actions/"+a.ID+"/decision", "POST", reviewerToken, "", decision)
	require.Equal(t, 200, status)
	require.NoError(t, json.Unmarshal(data, &a))
	require.Equal(t, "operator:bob", a.ApprovedBy)
	events, err := s.store.Events(a.ID)
	require.NoError(t, err)
	require.Equal(t, "operator:bob", events[1].Actor)
	status, _ = callHTTP(t, server.URL+"/admin/backup", "GET", observerToken, "", nil)
	require.Equal(t, 403, status)
	status, _ = callHTTP(t, server.URL+"/admin/backup", "GET", reviewerToken, "", nil)
	require.Equal(t, 403, status)
	status, data = callHTTP(t, server.URL+"/admin/me", "GET", observerToken, "", nil)
	require.Equal(t, 200, status)
	require.Contains(t, string(data), `"role":"observer"`)
	_, err = s.DecideAs(context.Background(), a.ID, a.Digest, "approve", "audit")
	require.Error(t, err)
}

func TestHealthMetricsLogsAndDrain(t *testing.T) {
	s, _ := testService(t, testConfig(t, ""), &countingExecutor{})
	h, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	var logs bytes.Buffer
	h.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	server := httptest.NewServer(h)
	defer server.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		status, _ := callHTTP(t, server.URL+path, "GET", "", "", nil)
		require.Equal(t, 200, status)
	}
	status, _ := callHTTP(t, server.URL+"/admin/metrics", "GET", agentToken, "", nil)
	require.Equal(t, 403, status)
	request := prRequest()
	request.Args["body"] = "SECRET_PAYLOAD_MUST_NOT_LOG"
	status, _ = callHTTP(t, server.URL+"/v1/actions?token=SECRET_QUERY_MUST_NOT_LOG", "POST", agentToken, "log", request)
	require.Equal(t, 200, status)
	status, data := callHTTP(t, server.URL+"/admin/metrics", "GET", adminToken, "", nil)
	require.Equal(t, 200, status)
	require.Contains(t, string(data), `circuit_actions{state="succeeded"} 1`)
	require.NotContains(t, string(data), "acme/app")
	require.NotContains(t, logs.String(), agentToken)
	require.NotContains(t, logs.String(), "SECRET_")
	require.Contains(t, logs.String(), "POST /v1/actions")
	h.Drain()
	status, _ = callHTTP(t, server.URL+"/readyz", "GET", "", "", nil)
	require.Equal(t, 503, status)
	status, _ = callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "drain", prRequest())
	require.Equal(t, 503, status)
	status, _ = callHTTP(t, server.URL+"/healthz", "GET", "", "", nil)
	require.Equal(t, 200, status)
}

func TestSnapshotRoundTripAndCorruption(t *testing.T) {
	cfg := testConfig(t, `limits:
- id: single-call
  actions: [create_pr]
  scope: agent
  window: 1h
  max_calls: 1
`)
	e := &countingExecutor{}
	s, _ := testService(t, cfg, e)
	a, err := s.Submit(context.Background(), "agent", "original", prRequest())
	require.NoError(t, err)
	f, err := s.store.backupTemp()
	require.NoError(t, err)
	defer removeTemp(f)
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	require.NoError(t, RestoreSnapshot(f.Name(), restoredPath))
	require.Error(t, RestoreSnapshot(f.Name(), restoredPath))
	info, err := os.Stat(restoredPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	restored, err := OpenStore(restoredPath)
	require.NoError(t, err)
	defer restored.Close()
	recoveredExecutor := &countingExecutor{}
	recovered, err := NewService(cfg, restored, recoveredExecutor)
	require.NoError(t, err)
	_, err = recovered.Submit(context.Background(), "agent", "original", prRequest())
	require.ErrorIs(t, err, ErrRestorePending)
	require.NoError(t, restored.Close())
	require.Error(t, AcknowledgeRestore(restoredPath, "too short"))
	require.NoError(t, AcknowledgeRestore(restoredPath, "Provider fixture reviewed: no writes were accepted after this snapshot."))
	restored, err = OpenStore(restoredPath)
	require.NoError(t, err)
	defer restored.Close()
	recovered, err = NewService(cfg, restored, recoveredExecutor)
	require.NoError(t, err)
	retry, err := recovered.Submit(context.Background(), "agent", "original", prRequest())
	require.NoError(t, err)
	require.Equal(t, a.ID, retry.ID)
	require.Zero(t, recoveredExecutor.calls.Load())
	request := prRequest()
	request.Args["title"] = "Different payload"
	denied, err := recovered.Submit(context.Background(), "agent", "new", request)
	require.NoError(t, err)
	require.Equal(t, "denied", denied.State)
	require.NoError(t, restored.Close())
	// Structural bbolt validity is insufficient: action digests must also match.
	db, err := bolt.Open(restoredPath, 0600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		a.Digest = "tampered"
		value, err := json.Marshal(a)
		if err != nil {
			return err
		}
		return tx.Bucket(actionsBucket).Put([]byte(a.ID), value)
	}))
	require.NoError(t, db.Close())
	require.Error(t, VerifySnapshot(restoredPath))
	_, err = OpenStore(restoredPath)
	require.Error(t, err)
	truncated := filepath.Join(t.TempDir(), "truncated.db")
	require.NoError(t, os.WriteFile(truncated, []byte("not a database"), 0600))
	require.Error(t, VerifySnapshot(truncated))
	require.Error(t, SaveSnapshot(strings.NewReader(strings.Repeat("x", 100)), filepath.Join(t.TempDir(), "too-large.db"), 10))
}

func TestMountedCredentialsAndProductionProfile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent-token")
	require.NoError(t, os.WriteFile(file, []byte(agentToken+"\n"), 0600))
	cfg := operatorConfig(t)
	cfg.Agents[0].TokenEnv = ""
	cfg.Agents[0].TokenFile = file
	t.Setenv("ALICE_TOKEN", adminToken)
	t.Setenv("BOB_TOKEN", reviewerToken)
	t.Setenv("AUDIT_TOKEN", observerToken)
	t.Setenv("CIRCUIT_ADMIN_TOKEN", "must-not-load-legacy")
	tokens, err := LoadTokens(cfg)
	require.NoError(t, err)
	require.Empty(t, tokens.Admin)
	require.Equal(t, agentToken, tokens.Agents["agent"])
	require.Error(t, cfg.ValidateProduction())
	cfg.GitHubApp = &GitHubAppConfig{AppID: 123, PrivateKeyFile: "/private/key.pem"}
	require.NoError(t, cfg.ValidateProduction())
	cfg.Simulation = true
	require.Error(t, cfg.ValidateProduction())
	for _, extra := range []string{`operators: [{id: bad, role: root, token_env: BAD}]`, `operators: [{id: bad, role: admin, token_env: BAD, token_file: /bad}]`} {
		_, err := ParseConfig(strings.NewReader("name: invalid\nagents: [{id: agent, token_env: TOKEN, repositories: [acme/app], actions: [get_pr]}]\n" + extra))
		require.Error(t, err)
	}
}

func TestRequestCapacityPreservesHealthAccess(t *testing.T) {
	s, _ := testService(t, testConfig(t, ""), &countingExecutor{})
	h, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	for i := 0; i < cap(h.slots); i++ {
		h.slots <- struct{}{}
	}
	server := httptest.NewServer(h)
	defer server.Close()
	status, _ := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "over-capacity", prRequest())
	require.Equal(t, 503, status)
	status, _ = callHTTP(t, server.URL+"/healthz", "GET", "", "", nil)
	require.Equal(t, 200, status)
	status, _ = callHTTP(t, server.URL+"/readyz", "GET", "", "", nil)
	require.Equal(t, 200, status)
}
