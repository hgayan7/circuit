//go:build linux || darwin

package telemetry

import (
	"fmt"
	"io"
	"syscall"
)

// Filesystem exports capacity visible to the service UID, without exposing paths.
func Filesystem(w io.Writer, path, volume string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		fmt.Fprintf(w, "circuit_filesystem_probe_success{volume=%q} 0\n", volume)
		return
	}
	fmt.Fprintf(w, "circuit_filesystem_probe_success{volume=%q} 1\ncircuit_filesystem_available_bytes{volume=%q} %.0f\ncircuit_filesystem_size_bytes{volume=%q} %.0f\n",
		volume, volume, float64(stat.Bavail)*float64(stat.Bsize), volume, float64(stat.Blocks)*float64(stat.Bsize))
}
