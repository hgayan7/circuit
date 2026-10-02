package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/himshikhargayan/si-shield/pkg/approval"
	"github.com/himshikhargayan/si-shield/pkg/audit"
	"github.com/himshikhargayan/si-shield/pkg/config"
	"github.com/himshikhargayan/si-shield/pkg/policy"
)

// HandlerOption configures the HTTP proxy handler.
type HandlerOption func(*Handler)

// WithAuditRecorder sets the audit recorder.
func WithAuditRecorder(r audit.Recorder) HandlerOption {
	return func(h *Handler) {
		h.recorder = r
	}
}

// WithApprovalProvider sets the human approval provider.
func WithApprovalProvider(a approval.Provider) HandlerOption {
	return func(h *Handler) {
		h.approver = a
	}
}

// WithInjectedToken sets the real downstream credential to inject.
func WithInjectedToken(token string) HandlerOption {
	return func(h *Handler) {
		h.injectedToken = token
	}
}

// Handler intercepts HTTP requests from AI agents and enforces policies.
type Handler struct {
	engine        *policy.Engine
	targetURL     *url.URL
	recorder      audit.Recorder
	approver      approval.Provider
	injectedToken string
	reverseProxy  *httputil.ReverseProxy
}

// NewHandler creates a new policy-enforcing HTTP reverse proxy handler.
func NewHandler(engine *policy.Engine, targetURL *url.URL, opts ...HandlerOption) *Handler {
	h := &Handler{
		engine:    engine,
		targetURL: targetURL,
	}

	for _, opt := range opts {
		opt(h)
	}

	h.reverseProxy = httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := h.reverseProxy.Director
	h.reverseProxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		if h.injectedToken != "" {
			req.Header.Set("Authorization", "Bearer "+h.injectedToken)
		}
	}

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// Read and buffer request body for payload evaluation
	var bodyBytes []byte
	var args map[string]any

	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		if err == nil && len(bodyBytes) > 0 {
			_ = json.Unmarshal(bodyBytes, &args)
		}
		// Restore body for downstream forwarding
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	endpoint := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	sessionID := r.Header.Get("X-Session-ID")
	agentID := r.Header.Get("X-Agent-ID")

	evalCtx := &policy.EvaluationContext{
		Endpoint:  endpoint,
		Method:    r.Method,
		Path:      r.URL.Path,
		Args:      args,
		SessionID: sessionID,
		AgentID:   agentID,
		Timestamp: startTime,
	}

	evalRes, err := h.engine.Evaluate(r.Context(), evalCtx)
	duration := time.Since(startTime)

	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "EvaluationError", err.Error(), "")
		return
	}

	// 1. Action: DENY
	if evalRes.Action == config.ActionDeny {
		if h.recorder != nil {
			_ = h.recorder.Record(&audit.Entry{
				ID:        fmt.Sprintf("tx_http_%d", time.Now().UnixNano()),
				Timestamp: startTime,
				Endpoint:  endpoint,
				Method:    r.Method,
				Path:      r.URL.Path,
				Args:      args,
				SessionID: sessionID,
				AgentID:   agentID,
				Decision:  config.ActionDeny,
				RuleID:    evalRes.RuleID,
				Reason:    evalRes.Reason,
				Duration:  duration,
			})
		}
		h.writeError(w, http.StatusForbidden, "PolicyViolation", evalRes.Reason, evalRes.RuleID)
		return
	}

	// 2. Action: REQUIRE_APPROVAL
	if evalRes.Action == config.ActionRequireApproval {
		if h.approver == nil {
			h.writeError(w, http.StatusForbidden, "ApprovalRequired", "No approver configured to authorize this action", evalRes.RuleID)
			return
		}

		reqID := fmt.Sprintf("appr_http_%d", time.Now().UnixNano())
		apprResp, apprErr := h.approver.RequestApproval(r.Context(), &approval.ApprovalRequest{
			ID:        reqID,
			Timestamp: startTime,
			Endpoint:  endpoint,
			Args:      args,
			SessionID: sessionID,
			AgentID:   agentID,
			Reason:    evalRes.Reason,
		})

		if apprErr != nil || !apprResp.Approved {
			reason := "Rejected by human approver"
			if apprResp != nil && apprResp.Reason != "" {
				reason = fmt.Sprintf("Rejected by human approver: %s", apprResp.Reason)
			}
			if h.recorder != nil {
				_ = h.recorder.Record(&audit.Entry{
					ID:         reqID,
					Timestamp:  startTime,
					Endpoint:   endpoint,
					Method:     r.Method,
					Path:       r.URL.Path,
					Args:       args,
					SessionID:  sessionID,
					AgentID:    agentID,
					Decision:   config.ActionDeny,
					RuleID:     evalRes.RuleID,
					Reason:     reason,
					Duration:   time.Since(startTime),
					ApprovedBy: "rejected",
				})
			}
			h.writeError(w, http.StatusForbidden, "ApprovalRejected", reason, evalRes.RuleID)
			return
		}

		// Approved!
		if h.recorder != nil {
			_ = h.recorder.Record(&audit.Entry{
				ID:         reqID,
				Timestamp:  startTime,
				Endpoint:   endpoint,
				Method:     r.Method,
				Path:       r.URL.Path,
				Args:       args,
				SessionID:  sessionID,
				AgentID:    agentID,
				Decision:   config.ActionAllow,
				RuleID:     evalRes.RuleID,
				Reason:     "Approved by operator",
				Duration:   time.Since(startTime),
				ApprovedBy: apprResp.DecidedBy,
			})
		}
	}

	// 3. Action: ALLOW (or Approved) -> Forward downstream
	if evalRes.Action == config.ActionAllow && h.recorder != nil {
		_ = h.recorder.Record(&audit.Entry{
			ID:        fmt.Sprintf("tx_http_%d", time.Now().UnixNano()),
			Timestamp: startTime,
			Endpoint:  endpoint,
			Method:    r.Method,
			Path:      r.URL.Path,
			Args:      args,
			SessionID: sessionID,
			AgentID:   agentID,
			Decision:  config.ActionAllow,
			RuleID:    evalRes.RuleID,
			Reason:    evalRes.Reason,
			Duration:  duration,
		})
	}

	h.reverseProxy.ServeHTTP(w, r)
}

func (h *Handler) writeError(w http.ResponseWriter, statusCode int, errType, reason, ruleID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   errType,
		"reason":  reason,
		"rule_id": ruleID,
	})
}
