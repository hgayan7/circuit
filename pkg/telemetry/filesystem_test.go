//go:build linux || darwin

package telemetry

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilesystemMetrics(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	Filesystem(&output, dir, "state")
	require.Contains(t, output.String(), `circuit_filesystem_probe_success{volume="state"} 1`)
	require.Contains(t, output.String(), `circuit_filesystem_available_bytes{volume="state"}`)
	require.NotContains(t, output.String(), dir)
	output.Reset()
	Filesystem(&output, filepath.Join(dir, "missing"), "state")
	require.Equal(t, "circuit_filesystem_probe_success{volume=\"state\"} 0\n", output.String())
}
