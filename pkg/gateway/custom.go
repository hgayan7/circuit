package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CustomToolTarget represents a governed external REST or MCP service.
type CustomToolTarget struct {
	ID              string            `yaml:"id" json:"id"`
	Name            string            `yaml:"name" json:"name"`
	Endpoint        string            `yaml:"endpoint" json:"endpoint"`
	Method          string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers         map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Operations      []string          `yaml:"operations,omitempty" json:"operations,omitempty"`
	RequireApproval bool              `yaml:"require_approval,omitempty" json:"require_approval,omitempty"`
	Timeout         time.Duration

	mockHandler func(req Request) (int, any, error)
	simMu       sync.RWMutex
	callHistory []map[string]any
	callSeq     int
	httpClient  *http.Client
	plugin      *CustomToolConfig
	pluginToken string
}

// NewCustomToolTarget creates a CustomToolTarget.
func NewCustomToolTarget(id, name, endpoint, method string, headers map[string]string, operations []string, reqApproval bool, timeout time.Duration) (*CustomToolTarget, error) {
	if id == "" {
		return nil, fmt.Errorf("custom tool ID is required")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("custom tool endpoint is required; use mock: explicitly for simulation")
	}
	if method == "" {
		method = "POST"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &CustomToolTarget{
		ID:              id,
		Name:            name,
		Endpoint:        endpoint,
		Method:          strings.ToUpper(method),
		Headers:         headers,
		Operations:      operations,
		RequireApproval: reqApproval,
		Timeout:         timeout,
		callHistory:     make([]map[string]any, 0),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

// SetMockHandler attaches a custom handler for testing or air-gapped simulation.
func (t *CustomToolTarget) SetMockHandler(h func(req Request) (int, any, error)) {
	t.mockHandler = h
}

// CustomToolExecutor executes governed calls on a CustomToolTarget.
type CustomToolExecutor struct {
	target *CustomToolTarget
}

// NewCustomToolExecutor creates a CustomToolExecutor.
func NewCustomToolExecutor(target *CustomToolTarget) *CustomToolExecutor {
	return &CustomToolExecutor{target: target}
}

// Execute executes an action request on the custom tool target.
func (e *CustomToolExecutor) Execute(ctx context.Context, r Request) Outcome {
	if e.target.plugin != nil {
		if e.target.plugin.Protocol == MCPForwardProtocol || e.target.plugin.Protocol == RESTForwardProtocol {
			return e.executeForward(ctx, r)
		}
		return e.executePlugin(ctx, r)
	}
	if e.target.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.target.Timeout)
		defer cancel()
	}

	// 1. If a custom mock handler is attached, use it.
	if e.target.mockHandler != nil {
		status, body, err := e.target.mockHandler(r)
		if err != nil {
			return Outcome{Status: status, Error: err.Error()}
		}
		data, _ := json.Marshal(body)
		return Outcome{Status: status, Body: data}
	}

	// 2. If endpoint is empty or marked as simulator/mock, use built-in simulator.
	if strings.HasPrefix(e.target.Endpoint, "mock:") || strings.HasPrefix(e.target.Endpoint, "sim:") {
		e.target.simMu.Lock()
		defer e.target.simMu.Unlock()

		e.target.callSeq++
		callID := fmt.Sprintf("call_%05d", e.target.callSeq)

		record := map[string]any{
			"call_id":   callID,
			"tool":      e.target.ID,
			"operation": r.Operation,
			"args":      r.Args,
			"status":    "executed",
			"simulated": true,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}
		e.target.callHistory = append(e.target.callHistory, record)

		data, _ := json.Marshal(record)
		return Outcome{Status: 200, Body: data}
	}

	// 3. Live HTTP call to Endpoint
	endpoint := e.target.Endpoint
	method := e.target.Method

	if m := text(r.Args, "method"); m != "" {
		method = strings.ToUpper(m)
	}
	if subPath := text(r.Args, "endpoint"); subPath != "" {
		endpoint = strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(subPath, "/")
	}

	var reqBody io.Reader
	payload, hasPayload := r.Args["payload"]
	if !hasPayload {
		payload = r.Args
	}

	if method != "GET" && method != "HEAD" && payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return Outcome{Status: 400, Error: fmt.Sprintf("failed to encode custom tool payload: %v", err)}
		}
		reqBody = bytes.NewReader(encoded)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return Outcome{Status: 400, Error: fmt.Sprintf("invalid custom tool request: %v", err)}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Circuit-CustomTool-Gateway/1.0")

	for k, v := range e.target.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := e.target.httpClient.Do(httpReq)
	if err != nil {
		return Outcome{Status: 502, Error: fmt.Sprintf("custom tool request failed: %v", err), Uncertain: method != "GET"}
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Outcome{Status: resp.StatusCode, Error: fmt.Sprintf("reading response: %v", err), Uncertain: method != "GET" && method != "HEAD"}
	}

	if resp.StatusCode >= 400 {
		return Outcome{
			Status:    resp.StatusCode,
			Body:      respData,
			Error:     fmt.Sprintf("custom tool upstream returned HTTP %d", resp.StatusCode),
			Uncertain: resp.StatusCode >= 500 && method != "GET",
		}
	}

	return Outcome{
		Status: resp.StatusCode,
		Body:   respData,
	}
}
