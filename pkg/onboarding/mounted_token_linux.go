//go:build linux

package onboarding

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// Scoped container credentials may be readable by the non-root workload UID,
// but must be mounted read-only. Host credentials keep the stricter mode check.
func mountedToken(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("mounted agent credential unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	var fs syscall.Statfs_t
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 || syscall.Fstatfs(fd, &fs) != nil || fs.Flags&1 == 0 {
		return nil, fmt.Errorf("mounted agent credential must be a regular file on a read-only mount")
	}
	return io.ReadAll(io.LimitReader(f, (1<<20)+1))
}
