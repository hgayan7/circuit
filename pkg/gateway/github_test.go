package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubAdapterCompleteWorkflowThroughHTTP(t *testing.T) {
	const secret = "github-upstream-secret-held-only-by-gateway"
	var mutations atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
		require.Equal(t, "2026-03-10", r.Header.Get("X-GitHub-Api-Version"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /repos/acme/app/git/refs":
			mutations.Add(1)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "refs/heads/circuit/fix", body["ref"])
			fmt.Fprint(w, `{"ref":"refs/heads/circuit/fix"}`)
		case "PUT /repos/acme/app/contents/src/fix.go":
			mutations.Add(1)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.NotContains(t, body, "path")
			require.Equal(t, "circuit/fix", body["branch"])
			fmt.Fprintf(w, `{"commit":{"sha":%q}}`, DemoSHA)
		case "POST /repos/acme/app/pulls":
			mutations.Add(1)
			fmt.Fprintf(w, `{"number":9,"head":{"sha":%q}}`, DemoSHA)
		case "GET /repos/acme/app/pulls/9":
			fmt.Fprintf(w, `{"head":{"sha":%q}}`, DemoSHA)
		case "GET /repos/acme/app/pulls/9/files":
			fmt.Fprint(w, `[{"filename":"src/fix.go"}]`)
		case "PUT /repos/acme/app/pulls/9/merge":
			mutations.Add(1)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, DemoSHA, body["sha"])
			fmt.Fprint(w, `{"merged":true}`)
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL)
			http.Error(w, `{"error":"unexpected"}`, 404)
		}
	}))
	defer origin.Close()
	github := NewGitHub(secret)
	github.base = origin.URL
	cfg := testConfig(t, `rules:
- id: file-review
  actions: [put_file]
  action: REQUIRE_APPROVAL
limits:
- id: write-cap
  actions: [create_branch, put_file, create_pr, merge_pr]
  scope: agent_repository
  window: 1h
  max_calls: 4
`)
	service, _ := testService(t, cfg, github)
	handler, err := NewHTTPHandler(service, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()
	requests := []Request{
		{Operation: "create_branch", Repository: "acme/app", Args: map[string]any{"branch": "circuit/fix", "sha": DemoSHA}},
		{Operation: "put_file", Repository: "acme/app", Args: map[string]any{"branch": "circuit/fix", "path": "src/fix.go", "content": "cGFja2FnZSBtYWluCg==", "message": "Fix"}},
		prRequest(),
		{Operation: "merge_pr", Repository: "acme/app", Args: map[string]any{"number": float64(9), "sha": DemoSHA}},
	}
	for j, req := range requests {
		key := fmt.Sprint("workflow-", j)
		status, data := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, key, req)
		require.Contains(t, []int{200, 202}, status)
		require.NotContains(t, string(data), secret)
		var a Action
		require.NoError(t, json.Unmarshal(data, &a))
		if req.Operation == "put_file" || req.Operation == "merge_pr" {
			require.Equal(t, "pending", a.State)
			status, data = callHTTP(t, server.URL+"/admin/actions/"+a.ID+"/decision", "POST", adminToken, "", map[string]string{"decision": "approve", "digest": a.Digest})
			require.Equal(t, 200, status)
			require.NoError(t, json.Unmarshal(data, &a))
		}
		require.Equal(t, "succeeded", a.State)
		status, _ = callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, key, req)
		require.Equal(t, 200, status)
	}
	require.EqualValues(t, 4, mutations.Load())
	status, _ := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "over-budget", prRequest())
	require.Equal(t, 403, status)
	require.EqualValues(t, 4, mutations.Load())
}
func TestMergeRejectsChangedSHAAndWorkflowFiles(t *testing.T) {
	for _, scenario := range []string{"changed-sha", "workflow", "renamed-workflow"} {
		t.Run(scenario, func(t *testing.T) {
			var writes atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" {
					writes.Add(1)
					fmt.Fprint(w, `{"merged":true}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/files") {
					if scenario == "renamed-workflow" {
						fmt.Fprint(w, `[{"filename":"src/new.go","previous_filename":".github/workflows/ci.yml"}]`)
					} else {
						fmt.Fprint(w, `[{"filename":".github/workflows/ci.yml"}]`)
					}
				} else {
					sha := DemoSHA
					if scenario == "changed-sha" {
						sha = strings.Repeat("b", 40)
					}
					fmt.Fprintf(w, `{"head":{"sha":%q}}`, sha)
				}
			}))
			defer origin.Close()
			g := NewGitHub("private-token")
			g.base = origin.URL
			out := g.Execute(context.Background(), mergeRequest())
			require.NotEmpty(t, out.Error)
			require.False(t, out.Uncertain)
			require.Zero(t, writes.Load())
		})
	}
}
func TestGitHubTransportAndResponseRedaction(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"message":"secret-token"}`) }))
	g := NewGitHub("secret-token")
	g.base = origin.URL
	out := g.Execute(context.Background(), prRequest())
	require.Empty(t, out.Error)
	require.NotContains(t, string(out.Body), "secret-token")
	origin.Close()
	out = g.Execute(context.Background(), prRequest())
	require.True(t, out.Uncertain)
	require.NotContains(t, out.Error, "secret-token")
}
