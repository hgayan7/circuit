//go:build !linux && !darwin

package telemetry

import (
	"fmt"
	"io"
)

// Unsupported hosts report a failed probe instead of fabricating capacity.
func Filesystem(w io.Writer, _ string, volume string) {
	fmt.Fprintf(w, "circuit_filesystem_probe_success{volume=%q} 0\n", volume)
}
