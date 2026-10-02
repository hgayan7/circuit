package audit_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/himshikhargayan/circuit/pkg/audit"
	"github.com/himshikhargayan/circuit/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRecorder_Record(t *testing.T) {
	var buf bytes.Buffer
	rec := audit.NewJSONRecorder(&buf)

	entry := &audit.Entry{
		ID:        "tx_12345",
		Timestamp: time.Now().UTC(),
		Tool:      "postgres.query",
		Args: map[string]any{
			"sql": "SELECT 1;",
		},
		SessionID: "sess_abc",
		AgentID:   "agent_xyz",
		Decision:  config.ActionAllow,
		RuleID:    "allow-safe-sql",
		Reason:    "Query permitted",
		Duration:  150 * time.Microsecond,
	}

	err := rec.Record(entry)
	require.NoError(t, err)

	// Verify NDJSON line output
	var decoded audit.Entry
	err = json.Unmarshal(buf.Bytes(), &decoded)
	require.NoError(t, err)

	assert.Equal(t, "tx_12345", decoded.ID)
	assert.Equal(t, "postgres.query", decoded.Tool)
	assert.Equal(t, config.ActionAllow, decoded.Decision)
	assert.Equal(t, "allow-safe-sql", decoded.RuleID)
	assert.Equal(t, "sess_abc", decoded.SessionID)
}

func TestJSONRecorder_FileCreation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := tmpDir + "/audit.log"

	rec, err := audit.NewFileRecorder(logPath)
	require.NoError(t, err)
	defer rec.Close()

	entry := &audit.Entry{
		ID:       "tx_file_1",
		Tool:     "stripe.refunds",
		Decision: config.ActionDeny,
	}

	err = rec.Record(entry)
	require.NoError(t, err)
}
