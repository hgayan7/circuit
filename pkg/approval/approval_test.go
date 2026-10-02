package approval_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/himshikhargayan/si-shield/pkg/approval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockProvider_Approve(t *testing.T) {
	mock := approval.NewMockProvider(true, "operator@company.com", "Confirmed safe")
	req := &approval.ApprovalRequest{
		ID:        "appr_1",
		Tool:      "stripe.refunds",
		Args:      map[string]any{"amount": 15000},
		Reason:    "Amount exceeds $100 threshold",
		Timeout:   5 * time.Second,
	}

	resp, err := mock.RequestApproval(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, resp.Approved)
	assert.Equal(t, "operator@company.com", resp.DecidedBy)
	assert.Equal(t, "Confirmed safe", resp.Reason)
}

func TestMockProvider_Reject(t *testing.T) {
	mock := approval.NewMockProvider(false, "security-lead", "Suspicious volume")
	req := &approval.ApprovalRequest{
		ID:     "appr_2",
		Tool:   "database.truncate",
		Reason: "Prohibited in prod",
	}

	resp, err := mock.RequestApproval(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Approved)
	assert.Equal(t, "security-lead", resp.DecidedBy)
	assert.Equal(t, "Suspicious volume", resp.Reason)
}

func TestCLIProvider_Approve(t *testing.T) {
	in := strings.NewReader("y\n")
	var out bytes.Buffer

	cli := approval.NewCLIProvider(in, &out)
	req := &approval.ApprovalRequest{
		ID:     "appr_3",
		Tool:   "postgres.query",
		Args:   map[string]any{"sql": "ALTER TABLE users ADD COLUMN active boolean;"},
		Reason: "Schema mutation",
	}

	resp, err := cli.RequestApproval(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, resp.Approved)
	assert.Contains(t, out.String(), "APPROVAL REQUIRED")
	assert.Contains(t, out.String(), "ALTER TABLE")
}

func TestCLIProvider_Reject(t *testing.T) {
	in := strings.NewReader("n\n")
	var out bytes.Buffer

	cli := approval.NewCLIProvider(in, &out)
	req := &approval.ApprovalRequest{
		ID:     "appr_4",
		Tool:   "postgres.query",
		Args:   map[string]any{"sql": "DROP TABLE temp_data;"},
		Reason: "Drop table",
	}

	resp, err := cli.RequestApproval(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Approved)
}
