package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/himshikhargayan/si-shield/pkg/approval"
	"github.com/himshikhargayan/si-shield/pkg/audit"
	"github.com/himshikhargayan/si-shield/pkg/config"
	"github.com/himshikhargayan/si-shield/pkg/policy"
)

// JSONRPCMessage is a standard JSON-RPC 2.0 message frame.
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError defines the JSON-RPC error payload.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ToolCallParams defines the schema of params in "tools/call".
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// PipeOption configures optional pipe settings.
type PipeOption func(*Pipe)

// WithAuditRecorder sets the audit recorder.
func WithAuditRecorder(r audit.Recorder) PipeOption {
	return func(p *Pipe) {
		p.recorder = r
	}
}

// WithApprovalProvider sets the human approval provider.
func WithApprovalProvider(a approval.Provider) PipeOption {
	return func(p *Pipe) {
		p.approver = a
	}
}

// WithToolPrefix prepends a prefix (e.g. "postgres") to MCP tool names.
func WithToolPrefix(prefix string) PipeOption {
	return func(p *Pipe) {
		p.toolPrefix = prefix
	}
}

// Pipe intercepts and filters MCP stdio JSON-RPC streams.
type Pipe struct {
	engine     *policy.Engine
	recorder   audit.Recorder
	approver   approval.Provider
	toolPrefix string
}

// NewPipe initializes an MCP interception pipe.
func NewPipe(engine *policy.Engine, opts ...PipeOption) *Pipe {
	p := &Pipe{
		engine: engine,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run connects the client (Cursor, Claude Desktop) and downstream MCP server streams.
func (p *Pipe) Run(ctx context.Context, clientIn io.Reader, clientOut io.Writer, downstreamOut io.Reader, downstreamIn io.Writer) error {
	var clientMu sync.Mutex
	writeToClient := func(data []byte) error {
		clientMu.Lock()
		defer clientMu.Unlock()
		_, err := clientOut.Write(data)
		return err
	}

	var downstreamMu sync.Mutex
	writeToDownstream := func(data []byte) error {
		downstreamMu.Lock()
		defer downstreamMu.Unlock()
		_, err := downstreamIn.Write(data)
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// 1. Forward downstream responses straight back to client
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(downstreamOut)
		for scanner.Scan() {
			line := scanner.Bytes()
			payload := append(line, '\n')
			if err := writeToClient(payload); err != nil {
				return
			}
		}
	}()

	// 2. Intercept and evaluate client requests before passing downstream
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(clientIn)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var msg JSONRPCMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				payload := append(line, '\n')
				if err := writeToDownstream(payload); err != nil {
					return
				}
				continue
			}

			// Non tools/call messages pass through unconditionally
			if msg.Method != "tools/call" {
				payload := append(line, '\n')
				if err := writeToDownstream(payload); err != nil {
					return
				}
				continue
			}

			// Intercept and evaluate tools/call
			var callParams ToolCallParams
			if err := json.Unmarshal(msg.Params, &callParams); err != nil {
				p.sendRPCError(writeToClient, msg.ID, -32602, "Invalid params: failed to parse tool call")
				continue
			}

			toolName := callParams.Name
			if p.toolPrefix != "" && !strings.Contains(toolName, ".") {
				toolName = p.toolPrefix + "." + toolName
			}

			startTime := time.Now()
			evalCtx := &policy.EvaluationContext{
				Tool:      toolName,
				Args:      callParams.Arguments,
				Timestamp: startTime,
			}

			evalRes, err := p.engine.Evaluate(ctx, evalCtx)
			duration := time.Since(startTime)

			if err != nil {
				p.sendRPCError(writeToClient, msg.ID, -32000, fmt.Sprintf("Policy evaluation error: %v", err))
				continue
			}

			// Action: ALLOW
			if evalRes.Action == config.ActionAllow {
				if p.recorder != nil {
					_ = p.recorder.Record(&audit.Entry{
						ID:        fmt.Sprintf("tx_%d", time.Now().UnixNano()),
						Timestamp: startTime,
						Tool:      toolName,
						Args:      callParams.Arguments,
						Decision:  config.ActionAllow,
						RuleID:    evalRes.RuleID,
						Reason:    evalRes.Reason,
						Duration:  duration,
					})
				}
				payload := append(line, '\n')
				if err := writeToDownstream(payload); err != nil {
					return
				}
				continue
			}

			// Action: DENY
			if evalRes.Action == config.ActionDeny {
				if p.recorder != nil {
					_ = p.recorder.Record(&audit.Entry{
						ID:        fmt.Sprintf("tx_%d", time.Now().UnixNano()),
						Timestamp: startTime,
						Tool:      toolName,
						Args:      callParams.Arguments,
						Decision:  config.ActionDeny,
						RuleID:    evalRes.RuleID,
						Reason:    evalRes.Reason,
						Duration:  duration,
					})
				}
				p.sendRPCError(writeToClient, msg.ID, -32000, fmt.Sprintf("Policy Violation: %s", evalRes.Reason))
				continue
			}

			// Action: REQUIRE_APPROVAL
			if evalRes.Action == config.ActionRequireApproval {
				if p.approver == nil {
					p.sendRPCError(writeToClient, msg.ID, -32000, "Action requires human approval, but no approval provider is configured")
					continue
				}

				reqID := fmt.Sprintf("appr_%d", time.Now().UnixNano())
				apprResp, apprErr := p.approver.RequestApproval(ctx, &approval.ApprovalRequest{
					ID:        reqID,
					Timestamp: startTime,
					Tool:      toolName,
					Args:      callParams.Arguments,
					Reason:    evalRes.Reason,
				})

				if apprErr != nil || !apprResp.Approved {
					reason := "Rejected by human approver"
					if apprResp != nil && apprResp.Reason != "" {
						reason = fmt.Sprintf("Rejected by human approver: %s", apprResp.Reason)
					}
					if p.recorder != nil {
						_ = p.recorder.Record(&audit.Entry{
							ID:         reqID,
							Timestamp:  startTime,
							Tool:       toolName,
							Args:       callParams.Arguments,
							Decision:   config.ActionDeny,
							RuleID:     evalRes.RuleID,
							Reason:     reason,
							Duration:   time.Since(startTime),
							ApprovedBy: "rejected",
						})
					}
					p.sendRPCError(writeToClient, msg.ID, -32000, fmt.Sprintf("Policy Violation: %s", reason))
					continue
				}

				// Approved! Forward downstream
				if p.recorder != nil {
					_ = p.recorder.Record(&audit.Entry{
						ID:         reqID,
						Timestamp:  startTime,
						Tool:       toolName,
						Args:       callParams.Arguments,
						Decision:   config.ActionAllow,
						RuleID:     evalRes.RuleID,
						Reason:     "Approved by operator",
						Duration:   time.Since(startTime),
						ApprovedBy: apprResp.DecidedBy,
					})
				}

				payload := append(line, '\n')
				if err := writeToDownstream(payload); err != nil {
					return
				}
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (p *Pipe) sendRPCError(writeFn func([]byte) error, id any, code int, message string) {
	errResp := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
	bytes, _ := json.Marshal(errResp)
	bytes = append(bytes, '\n')
	_ = writeFn(bytes)
}

// WrapSubprocess launches the downstream command and wraps its stdio with the Pipe.
func (p *Pipe) WrapSubprocess(ctx context.Context, stdin io.Reader, stdout io.Writer, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)

	downstreamIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create child stdin pipe: %w", err)
	}

	downstreamOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create child stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start downstream MCP command: %w", err)
	}

	pipeErr := p.Run(ctx, stdin, stdout, downstreamOut, downstreamIn)

	_ = downstreamIn.Close()
	_ = cmd.Wait()
	return pipeErr
}
