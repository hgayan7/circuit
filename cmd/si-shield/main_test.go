package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommand(t *testing.T) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"--help"})

	err := rootCmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "si-shield")
	assert.Contains(t, buf.String(), "wrap")
	assert.Contains(t, buf.String(), "serve")
	assert.Contains(t, buf.String(), "validate")
}

func TestVersionCommand(t *testing.T) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"version"})

	err := rootCmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "si-shield v0.1.0")
}

func TestValidateCommand_ValidFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "policy-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := `
name: "valid-cli-policy"
rules:
  - id: "r1"
    match:
      tool: "git.push"
    condition: "args.branch == 'main'"
    action: DENY
    reason: "Direct push to main blocked"
`
	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	_ = tmpFile.Close()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"validate", "--policy", tmpFile.Name()})

	err = rootCmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "compiled successfully")
}

func TestValidateCommand_InvalidFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "bad-policy-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := `
name: "bad-cli-policy"
rules:
  - id: "r1"
    match:
      tool: "git.push"
    condition: "args.syntax error ==="
    action: DENY
`
	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	_ = tmpFile.Close()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"validate", "--policy", tmpFile.Name()})

	err = rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy compilation failed")
}
