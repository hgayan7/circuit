package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/hgayan7/circuit/pkg/approval"
	"github.com/hgayan7/circuit/pkg/audit"
	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/policy"
	"github.com/hgayan7/circuit/pkg/safety"
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

	if targetURL == nil {
		return h
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

	h.reverseProxy.ModifyResponse = h.InspectResponse
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Authorize(w, r) {
		h.reverseProxy.ServeHTTP(w, r)
	}
}

// Authorize enforces policy without forwarding; also used by the forward proxy.
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) bool {
	startTime := time.Now()

	// Read and buffer request body for payload evaluation
	var bodyBytes []byte
	var args map[string]any

	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, safety.MaxInspectionBytes+1))
		if err != nil || len(bodyBytes) > safety.MaxInspectionBytes {
			h.writeError(w, http.StatusRequestEntityTooLarge, "InspectionError", "Request body unreadable or exceeds 2 MiB", "")
			return false
		}
		if len(bodyBytes) > 0 {
			contentType := r.Header.Get("Content-Type")
			switch {
			case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
				values, parseErr := url.ParseQuery(string(bodyBytes))
				if parseErr != nil {
					h.writeError(w, 400, "InvalidPayload", parseErr.Error(), "")
					return false
				}
				args = map[string]any{}
				for key, vs := range values {
					if len(vs) == 1 {
						args[key] = vs[0]
					} else {
						items := make([]any, len(vs))
						for j, v := range vs {
							items[j] = v
						}
						args[key] = items
					}
				}
			case strings.HasPrefix(contentType, "application/json") || contentType == "":
				if err := json.Unmarshal(bodyBytes, &args); err != nil {
					h.writeError(w, 400, "InvalidPayload", "Expected a JSON object", "")
					return false
				}
			case strings.HasPrefix(contentType, "text/"):
				args = map[string]any{"text": string(bodyBytes)}
			default:
				if h.engine.InspectionEnabled() {
					h.writeError(w, 415, "UnsupportedPayload", "Inspection supports JSON objects, form data, and text", "")
					return false
				}
			}
		}
		// Restore body for downstream forwarding
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	endpoint := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	sessionID := r.Header.Get("X-Session-ID")
	agentID := r.Header.Get("X-Agent-ID")

	evalCtx := &policy.EvaluationContext{
		Host:      r.URL.Hostname(),
		Endpoint:  endpoint,
		Method:    r.Method,
		Path:      r.URL.Path,
		Args:      args,
		SessionID: sessionID,
		AgentID:   agentID,
		Timestamp: startTime,
	}

	if evalCtx.Host == "" {
		evalCtx.Host = r.Host
	}
	evalRes, err := h.engine.Evaluate(r.Context(), evalCtx)
	duration := time.Since(startTime)

	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "EvaluationError", err.Error(), "")
		return false
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
		return false
	}

	// 2. Action: REQUIRE_APPROVAL
	if evalRes.Action == config.ActionRequireApproval {
		if h.approver == nil {
			h.writeError(w, http.StatusForbidden, "ApprovalRequired", "No approver configured to authorize this action", evalRes.RuleID)
			return false
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
			return false
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

	return true
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

// InspectResponse buffers inspectable responses before any content reaches the client.
func (h *Handler) InspectResponse(resp *http.Response) error {
	if !h.engine.InspectResponses() || resp.Body == nil || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || (resp.Request != nil && resp.Request.Method == http.MethodHead) {
		return nil
	}
	originalBody := resp.Body
	defer originalBody.Close()
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") || resp.StatusCode == http.StatusSwitchingProtocols {
		return fmt.Errorf("streaming responses are unsupported while prompt inspection is enabled")
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return fmt.Errorf("encoded responses cannot be inspected")
	}
	if ct != "" && !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return fmt.Errorf("unsupported response content type for prompt inspection")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, safety.MaxInspectionBytes+1))
	if err != nil {
		return fmt.Errorf("response inspection read failed: %w", err)
	}
	if len(data) > safety.MaxInspectionBytes {
		return fmt.Errorf("response exceeds 2 MiB inspection limit")
	}
	var content any
	if strings.Contains(ct, "json") {
		if err := json.Unmarshal(data, &content); err != nil {
			return fmt.Errorf("invalid JSON response")
		}
	}
	var check func(any) error
	check = func(v any) error {
		switch x := v.(type) {
		case string:
			if finding := h.engine.InspectText(x); finding != nil {
				return fmt.Errorf("%s", finding.Reason)
			}
		case map[string]any:
			for _, val := range x {
				if err := check(val); err != nil {
					return err
				}
			}
		case []any:
			for _, val := range x {
				if err := check(val); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if content != nil {
		if err := check(content); err != nil {
			return err
		}
	} else if finding := h.engine.InspectText(string(data)); finding != nil {
		return fmt.Errorf("%s", finding.Reason)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header.Set("Content-Length", fmt.Sprint(len(data)))
	return nil
}
