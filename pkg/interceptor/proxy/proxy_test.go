package proxy_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/himshikhargayan/si-shield/pkg/approval"
	"github.com/himshikhargayan/si-shield/pkg/audit"
	"github.com/himshikhargayan/si-shield/pkg/config"
	"github.com/himshikhargayan/si-shield/pkg/interceptor/proxy"
	"github.com/himshikhargayan/si-shield/pkg/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupProxyTestEngine(t *testing.T) *policy.Engine {
	yamlPolicy := `
name: "proxy-test-policy"
default_action: ALLOW
rules:
  - id: "block-bulk-deletion"
    match:
      endpoint: "DELETE /api/users"
    action: DENY
    reason: "Bulk user deletion is prohibited"

  - id: "stripe-high-refund"
    match:
      endpoint: "POST /v1/refunds"
    condition: "args.amount > 10000"
    action: REQUIRE_APPROVAL
    reason: "Refund over $100 requires sign-off"
`
	pol, err := config.ParsePolicy(strings.NewReader(yamlPolicy))
	require.NoError(t, err)

	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)
	return engine
}

func TestProxy_Allowed_ForwardsRequest(t *testing.T) {
	upstreamCalled := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		assert.Equal(t, "/v1/refunds", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"re_123","status":"succeeded"}`))
	}))
	defer upstreamServer.Close()

	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)

	engine := setupProxyTestEngine(t)
	var auditBuf bytes.Buffer
	rec := audit.NewJSONRecorder(&auditBuf)

	pHandler := proxy.NewHandler(engine, upstreamURL,
		proxy.WithAuditRecorder(rec),
	)

	proxyServer := httptest.NewServer(pHandler)
	defer proxyServer.Close()

	// Safe refund of $50 (5000 cents) -> ALLOW
	body := `{"amount":5000,"reason":"requested_by_customer"}`
	resp, err := http.Post(proxyServer.URL+"/v1/refunds", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, upstreamCalled)

	respBody, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(respBody), "re_123")
	assert.Contains(t, auditBuf.String(), `"decision":"ALLOW"`)
}

func TestProxy_Denied_BlocksRequest(t *testing.T) {
	upstreamCalled := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer upstreamServer.Close()

	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)

	engine := setupProxyTestEngine(t)
	var auditBuf bytes.Buffer
	rec := audit.NewJSONRecorder(&auditBuf)

	pHandler := proxy.NewHandler(engine, upstreamURL,
		proxy.WithAuditRecorder(rec),
	)

	proxyServer := httptest.NewServer(pHandler)
	defer proxyServer.Close()

	// Destructive DELETE /api/users -> DENY
	req, err := http.NewRequest(http.MethodDelete, proxyServer.URL+"/api/users", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, upstreamCalled, "Downstream service must NOT be called on DENY")

	var errResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	assert.Equal(t, "block-bulk-deletion", errResp["rule_id"])
	assert.Contains(t, errResp["reason"], "Bulk user deletion is prohibited")
	assert.Contains(t, auditBuf.String(), `"decision":"DENY"`)
}

func TestProxy_TokenVirtualization(t *testing.T) {
	receivedAuthHeader := ""
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()

	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)

	engine := setupProxyTestEngine(t)
	pHandler := proxy.NewHandler(engine, upstreamURL,
		proxy.WithInjectedToken("sk_live_real_secret_token_12345"),
	)

	proxyServer := httptest.NewServer(pHandler)
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodGet, proxyServer.URL+"/v1/balance", nil)
	require.NoError(t, err)
	// Agent passes virtual mock token
	req.Header.Set("Authorization", "Bearer mock-agent-token")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	// Upstream received the real vaulted secret!
	assert.Equal(t, "Bearer sk_live_real_secret_token_12345", receivedAuthHeader)
}

func TestProxy_RequireApproval_ApprovedAndRejected(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()

	upstreamURL, _ := url.Parse(upstreamServer.URL)
	engine := setupProxyTestEngine(t)

	// Subtest 1: Approved
	approver1 := approval.NewMockProvider(true, "sec-ops", "Signoff granted")
	pHandler1 := proxy.NewHandler(engine, upstreamURL,
		proxy.WithApprovalProvider(approver1),
	)
	s1 := httptest.NewServer(pHandler1)
	defer s1.Close()

	refundBody := `{"amount":25000}`
	resp1, err := http.Post(s1.URL+"/v1/refunds", "application/json", strings.NewReader(refundBody))
	require.NoError(t, err)
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	// Subtest 2: Rejected
	approver2 := approval.NewMockProvider(false, "sec-ops", "Limit too high")
	pHandler2 := proxy.NewHandler(engine, upstreamURL,
		proxy.WithApprovalProvider(approver2),
	)
	s2 := httptest.NewServer(pHandler2)
	defer s2.Close()

	resp2, err := http.Post(s2.URL+"/v1/refunds", "application/json", strings.NewReader(refundBody))
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode)
}
