package approval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ApprovalRequest contains the details of an action suspended for human verification.
type ApprovalRequest struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Tool      string         `json:"tool,omitempty"`
	Endpoint  string         `json:"endpoint,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Timeout   time.Duration  `json:"timeout,omitempty"`
}

// ApprovalResponse contains the decision rendered by a human operator.
type ApprovalResponse struct {
	Approved  bool   `json:"approved"`
	DecidedBy string `json:"decided_by,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Provider defines the interface for collecting human approvals.
type Provider interface {
	RequestApproval(ctx context.Context, req *ApprovalRequest) (*ApprovalResponse, error)
}

// MockProvider is used for testing and deterministic evaluation.
type MockProvider struct {
	mu        sync.Mutex
	approved  bool
	decidedBy string
	reason    string
}

// NewMockProvider returns a configured mock approval provider.
func NewMockProvider(approved bool, decidedBy, reason string) *MockProvider {
	return &MockProvider{
		approved:  approved,
		decidedBy: decidedBy,
		reason:    reason,
	}
}

func (m *MockProvider) RequestApproval(ctx context.Context, req *ApprovalRequest) (*ApprovalResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &ApprovalResponse{
		Approved:  m.approved,
		DecidedBy: m.decidedBy,
		Reason:    m.reason,
	}, nil
}

// CLIProvider renders a visual diff/summary on terminal and prompts for approval.
type CLIProvider struct {
	in  *bufio.Scanner
	out io.Writer
	mu  sync.Mutex
}

// NewCLIProvider creates an interactive terminal approval prompt.
func NewCLIProvider(in io.Reader, out io.Writer) *CLIProvider {
	return &CLIProvider{
		in:  bufio.NewScanner(in),
		out: out,
	}
}

func (c *CLIProvider) RequestApproval(ctx context.Context, req *ApprovalRequest) (*ApprovalResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	target := req.Tool
	if target == "" {
		target = req.Endpoint
	}

	argsJSON, _ := json.MarshalIndent(req.Args, "", "  ")

	fmt.Fprintf(c.out, "\n========================================\n")
	fmt.Fprintf(c.out, "🛑 [si-shield] HUMAN APPROVAL REQUIRED\n")
	fmt.Fprintf(c.out, "ID:      %s\n", req.ID)
	fmt.Fprintf(c.out, "Target:  %s\n", target)
	fmt.Fprintf(c.out, "Reason:  %s\n", req.Reason)
	fmt.Fprintf(c.out, "Payload:\n%s\n", string(argsJSON))
	fmt.Fprintf(c.out, "========================================\n")
	fmt.Fprintf(c.out, "Authorize execution? [y/N]: ")

	type scanResult struct {
		text string
		err  error
	}

	ch := make(chan scanResult, 1)
	go func() {
		if c.in.Scan() {
			ch <- scanResult{text: c.in.Text()}
		} else {
			ch <- scanResult{err: c.in.Err()}
		}
	}()

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		fmt.Fprintf(c.out, "\n[si-shield] Approval timed out after %s. Request rejected.\n", timeout)
		return &ApprovalResponse{
			Approved:  false,
			DecidedBy: "system:timeout",
			Reason:    "Approval request timed out",
		}, nil
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		ans := strings.TrimSpace(strings.ToLower(res.text))
		if ans == "y" || ans == "yes" {
			return &ApprovalResponse{
				Approved:  true,
				DecidedBy: "cli:operator",
				Reason:    "Approved via CLI prompt",
			}, nil
		}
		return &ApprovalResponse{
			Approved:  false,
			DecidedBy: "cli:operator",
			Reason:    "Rejected via CLI prompt",
		}, nil
	}
}
