package initwizard_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hgayan7/circuit/pkg/initwizard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWizard_OpenAI_WithBudget(t *testing.T) {
	// Simulate user answering:
	// 1. Mode: 1 (circuit run — HTTP agent)
	// 2. Stack: 1 (OpenAI)
	// 3. Budget: y
	// 4. Max actions: 100
	// 5. Max spend: 5.00
	// 6. Audit: y
	// 7. HITL: n
	input := "1\n1\ny\n100\n5.00\ny\nn\n"
	outDir := t.TempDir()

	var stdout bytes.Buffer
	err := initwizard.Run(strings.NewReader(input), &stdout, outDir)
	require.NoError(t, err)

	// File must exist
	outPath := filepath.Join(outDir, "circuit.yaml")
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "openai")
	assert.Contains(t, content, "max_actions: 100")
	assert.Contains(t, content, "max_spend: 5")
	assert.Contains(t, content, "circuit.audit.ndjson")
	assert.NotContains(t, content, "require_approval: true")

	// Stdout must show next-step instructions
	out := stdout.String()
	assert.Contains(t, out, "circuit.yaml")
	assert.Contains(t, out, "circuit run")
}

func TestWizard_MCP_Postgres_WithHITL(t *testing.T) {
	// Mode: 2 (mcp wrap), Stack: 3 (Postgres MCP), Budget: n, Audit: n, HITL: y
	input := "2\n3\nn\nn\ny\n"
	outDir := t.TempDir()

	var stdout bytes.Buffer
	err := initwizard.Run(strings.NewReader(input), &stdout, outDir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(outDir, "circuit.yaml"))
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "postgres")
	assert.Contains(t, content, "require_approval: true")
	assert.NotContains(t, content, "max_actions")

	out := stdout.String()
	assert.Contains(t, out, "circuit mcp wrap")
}

func TestWizard_CustomHTTP_NoOptionals(t *testing.T) {
	// Mode: 1, Stack: 5 (Custom), Budget: n, Audit: n, HITL: n
	input := "1\n5\nn\nn\nn\n"
	outDir := t.TempDir()

	var stdout bytes.Buffer
	err := initwizard.Run(strings.NewReader(input), &stdout, outDir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(outDir, "circuit.yaml"))
	require.NoError(t, err)

	content := string(data)
	// Should have a minimal catch-all allow rule
	assert.Contains(t, content, "allow")
}

func TestWizard_InvalidModeInput_ReturnsError(t *testing.T) {
	// EOF immediately — simulates Ctrl-C / no input
	err := initwizard.Run(strings.NewReader(""), &bytes.Buffer{}, t.TempDir())
	assert.Error(t, err)
}

func TestWizard_DoesNotOverwriteExisting(t *testing.T) {
	outDir := t.TempDir()
	existing := filepath.Join(outDir, "circuit.yaml")
	require.NoError(t, os.WriteFile(existing, []byte("original"), 0644))

	// Mode 1, OpenAI, no budget, no audit, no hitl
	input := "1\n1\nn\nn\nn\n"
	var stdout bytes.Buffer
	err := initwizard.Run(strings.NewReader(input), &stdout, outDir)
	require.NoError(t, err)

	// Should warn but NOT overwrite
	data, _ := os.ReadFile(existing)
	assert.Equal(t, "original", string(data))
	assert.Contains(t, stdout.String(), "already exists")
}
