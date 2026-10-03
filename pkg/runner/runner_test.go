package runner_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hgayan7/circuit/pkg/audit"
	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/policy"
	"github.com/hgayan7/circuit/pkg/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunner_ExecutesCommandWithProxyEnv(t *testing.T) {
	yamlPolicy := `
name: "runner-test-policy"
default_action: ALLOW
rules:
  - id: "block-test-action"
    match:
      endpoint: "GET /blocked"
    action: DENY
    reason: "Blocked by runner test policy"
`
	pol, err := config.ParsePolicy(strings.NewReader(yamlPolicy))
	require.NoError(t, err)

	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)

	var auditBuf bytes.Buffer
	rec := audit.NewJSONRecorder(&auditBuf)

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	}))
	defer upstreamServer.Close()

	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)

	r := runner.New(engine,
		runner.WithTargetURL(upstreamURL),
		runner.WithAuditRecorder(rec),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// Run "env" command to verify HTTP_PROXY environment variables are injected
	err = r.RunCommand(ctx, strings.NewReader(""), &stdout, &stderr, "env")
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "HTTP_PROXY=http://127.0.0.1:")
	assert.Contains(t, output, "HTTPS_PROXY=http://127.0.0.1:")
}

// Exercise a real proxy-aware child, not merely the presence of env variables.
func TestRunner_ForwardProxyEnforcesChildHTTP(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, "ok") }))
	defer origin.Close()
	pol, err := config.ParsePolicy(strings.NewReader("name: child-forward\nrules:\n- id: block\n  match:\n    endpoint: GET /blocked\n  action: DENY\n"))
	require.NoError(t, err)
	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)
	r := runner.New(engine)
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, r.RunCommand(ctx, strings.NewReader(""), &stdout, &stderr, "curl", "--silent", "--show-error", "--fail", "--max-time", "5", origin.URL+"/allowed"))
	assert.Equal(t, "ok", stdout.String())
	stdout.Reset()
	stderr.Reset()
	require.Error(t, r.RunCommand(ctx, strings.NewReader(""), &stdout, &stderr, "curl", "--silent", "--show-error", "--fail", "--max-time", "5", origin.URL+"/blocked"))
	assert.EqualValues(t, 1, calls.Load())
}
