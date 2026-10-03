package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupWizardCancellationAndNonInteractiveConfiguration(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	out := filepath.Join(dir, "setup")
	cmd := newSetupCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(strings.NewReader("workspace\n" + workspace + "\nread-only\nno\n"))
	cmd.SetArgs([]string{"--out", out})
	require.NoError(t, cmd.Execute())
	_, err := os.Stat(out)
	require.True(t, os.IsNotExist(err))
	require.Contains(t, output.String(), "No files created")
	cmd = newSetupCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--non-interactive", "--integration", "workspace", "--workspace", workspace, "--out", out})
	require.NoError(t, cmd.Execute())
	doctor := newDoctorCommand()
	doctor.SetOut(&output)
	doctor.SetErr(&output)
	doctor.SetArgs([]string{"--dir", out, "--offline"})
	require.NoError(t, doctor.Execute())
	require.Contains(t, output.String(), "Offline only")
	data, err := os.ReadFile(filepath.Join(out, "secrets", "agent-token"))
	require.NoError(t, err)
	require.NotContains(t, output.String(), strings.TrimSpace(string(data)))
}
func TestSetupMissingInputsAndExistingDestinationFail(t *testing.T) {
	cmd := newSetupCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(""))
	require.Error(t, cmd.Execute())
	dir := t.TempDir()
	cmd = newSetupCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--non-interactive", "--integration", "workspace", "--workspace", dir, "--out", dir})
	require.Error(t, cmd.Execute())
}
